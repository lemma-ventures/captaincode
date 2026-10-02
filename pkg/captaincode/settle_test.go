package captaincode

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func settleLedger(o OutcomeEvidence) *Ledger {
	l := &Ledger{}
	l.RecordOutcome(o)
	return l
}

func TestSettleFailedCheckRejectsAtOnce(t *testing.T) {
	now := time.Now()
	l := settleLedger(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending, UpdatedAt: now,
		Checks: []CheckResult{{Command: "go test ./...", Source: "gate", Passed: false, ExitCode: 1, At: now}}})
	if n := l.SettleOutcomes(now); n != 1 {
		t.Fatalf("expected 1 settled, got %d", n)
	}
	o := l.OutcomeFor("t1")
	if o.Status != AcceptanceRejected || o.DecidedBy != DecidedByChecks {
		t.Fatalf("got %s by %s, want rejected by checks", o.Status, o.DecidedBy)
	}
	if o.RejectedAt.IsZero() {
		t.Fatal("rejected outcome carries no rejection time")
	}
}

func TestSettleWaitsOutTheWindowBeforeAccepting(t *testing.T) {
	now := time.Now()
	l := settleLedger(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending, UpdatedAt: now,
		Checks: []CheckResult{{Command: "go test", Passed: true, At: now}}})
	if n := l.SettleOutcomes(now.Add(time.Hour)); n != 0 {
		t.Fatalf("an hour-old passing check settled %d outcome(s); the window is %s", n, OutcomeSettleWindow())
	}
	if l.OutcomeFor("t1").Status != AcceptancePending {
		t.Fatal("outcome accepted inside the settle window")
	}
	if n := l.SettleOutcomes(now.Add(OutcomeSettleWindow() + time.Minute)); n != 1 {
		t.Fatalf("expected 1 settled past the window, got %d", n)
	}
	o := l.OutcomeFor("t1")
	if o.Status != AcceptanceAccepted || o.DecidedBy != DecidedByChecks {
		t.Fatalf("got %s by %s, want accepted by checks", o.Status, o.DecidedBy)
	}
	if !o.CleanlyAccepted() {
		t.Fatal("a passing, uncorrected, unregressed acceptance is not clean")
	}
}

func TestSettleNeverOverwritesAHumanVerdict(t *testing.T) {
	now := time.Now()
	l := &Ledger{}
	l.RecordCheckResult("t1", CheckResult{Command: "go test", Passed: false, ExitCode: 1, At: now})
	if err := l.RecordTaskReview("t1", ReviewAccept, "captain", "checks are wrong here", false); err != nil {
		t.Fatal(err)
	}
	l.SettleOutcomes(now.Add(72 * time.Hour))
	o := l.OutcomeFor("t1")
	if o.Status != AcceptanceAccepted || o.DecidedBy != DecidedByReviewer {
		t.Fatalf("got %s by %s, want accepted by reviewer despite the failed check", o.Status, o.DecidedBy)
	}
}

func TestSettleLeavesCorrectedWorkToAHuman(t *testing.T) {
	now := time.Now()
	l := &Ledger{}
	l.RecordCheckResult("t1", CheckResult{Command: "go test", Passed: true, At: now})
	l.RecordCorrection("t1", 30, "the checks passed and it was still wrong")
	l.SettleOutcomes(now.Add(72 * time.Hour))
	o := l.OutcomeFor("t1")
	if o.Status != AcceptancePending || o.DecidedBy != "" {
		t.Fatalf("got %s by %s, want pending: passing checks plus correction minutes is a "+
			"statement about the checks, not an acceptance", o.Status, o.DecidedBy)
	}
}

func TestSettleFromLifecycleButNotFromCancellation(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		state LifecycleState
		want  AcceptanceStatus
	}{
		{StateFailed, AcceptanceRejected},
		{StateExhausted, AcceptanceRejected},
		{StateCancelled, AcceptancePending},
	} {
		l := settleLedger(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending, UpdatedAt: now})
		l.RecordTaskState(TaskState{TaskID: "t1", State: tc.state, UpdatedAt: now})
		l.SettleOutcomes(now.Add(72 * time.Hour))
		if got := l.OutcomeFor("t1").Status; got != tc.want {
			t.Fatalf("%s task settled %s, want %s", tc.state, got, tc.want)
		}
	}
}

func TestSettleIsIdempotent(t *testing.T) {
	now := time.Now()
	l := settleLedger(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending, UpdatedAt: now,
		Checks: []CheckResult{{Command: "go test", Passed: true, At: now}}})
	later := now.Add(72 * time.Hour)
	if n := l.SettleOutcomes(later); n != 1 {
		t.Fatalf("first sweep settled %d, want 1", n)
	}
	if n := l.SettleOutcomes(later.Add(time.Hour)); n != 0 {
		t.Fatalf("second sweep settled %d, want 0", n)
	}
}

// gateRows builds a screening the way ScreenAction records one: three nouls
// turned into yes/no answers at the confidence their distance from a coin
// flip carries.
func gateRow(taskID string, risk float64) ShadowRecord {
	answers := map[string]S1Answer{}
	for _, p := range GatePoints {
		answers[p] = S1Answer{Type: "noul", Noul: 0.02}
	}
	answers[PointGateDestructive] = S1Answer{Type: "noul", Noul: risk}
	return ShadowRecord{Point: PointGate, Points: GatePoints, TaskID: taskID,
		Shadow: Shadow{Leg: LegJev, Model: "jev-1", Backend: "typesafe", At: time.Now(),
			Answers: nulAnswers(answers)}}
}

func TestSettleGateRowsOnlyFromCleanAcceptance(t *testing.T) {
	outcomes := []OutcomeEvidence{
		{TaskID: "clean", Status: AcceptanceAccepted, DecidedBy: DecidedByChecks},
		{TaskID: "corrected", Status: AcceptanceAccepted, DecidedBy: DecidedByChecks,
			Corrections: []CorrectionRecord{{Minutes: 20}}},
		{TaskID: "rejected", Status: AcceptanceRejected, DecidedBy: DecidedByChecks},
		{TaskID: "pending", Status: AcceptancePending},
	}
	rows := []ShadowRecord{
		gateRow("clean", 0.03), gateRow("corrected", 0.03),
		gateRow("rejected", 0.03), gateRow("pending", 0.03), gateRow("", 0.03),
	}
	if n := SettleGateRows(rows, outcomes); n != 1 {
		t.Fatalf("settled %d row(s), want 1: only a cleanly accepted task settles a screening", n)
	}
	if a := rows[0].Answers[PointGateDestructive]; a.Actual != "false" || !a.Agree || a.By != GateSettledBy {
		t.Fatalf("clean row: actual=%q agree=%v by=%q", a.Actual, a.Agree, a.By)
	}
	for _, i := range []int{1, 2, 3, 4} {
		if a := rows[i].Answers[PointGateDestructive]; a.Actual != "" {
			t.Fatalf("row %d settled to %q; a rejection cannot be attributed to one action", i, a.Actual)
		}
	}
}

func TestSettledGateRowsProduceAFalsePositiveReading(t *testing.T) {
	// A sample shaped the way a real one is: most screenings are confidently
	// harmless and the task's acceptance proves them right; a few nouls fire
	// on actions that turned out fine. The bar is the lowest floor where the
	// gate was still right about the harmless ones - a bound on over-refusal.
	var outcomes []OutcomeEvidence
	var rows []ShadowRecord
	add := func(id string, risk float64) {
		outcomes = append(outcomes, OutcomeEvidence{TaskID: id, Status: AcceptanceAccepted, DecidedBy: DecidedByChecks})
		rows = append(rows, gateRow(id, risk))
	}
	for i := 0; i < 24; i++ {
		add(fmt.Sprintf("low%d", i), 0.03)
	}
	add("high0", 0.96)
	for i := 0; i < 8; i++ {
		add(fmt.Sprintf("mid%d", i), 0.55)
	}
	if n := SettleGateRows(rows, outcomes); n != len(rows) {
		t.Fatalf("settled %d of %d", n, len(rows))
	}
	cal := ShadowCalibration(nil, rows, outcomes)
	var dest PointCalibration
	for _, p := range cal {
		if p.Point == PointGateDestructive {
			dest = p
		}
	}
	if dest.Compared != 33 {
		t.Fatalf("compared %d, want 33", dest.Compared)
	}
	if dest.Agree != 24 {
		t.Fatalf("agreed %d, want 24: nine nouls fired on actions their task proved harmless", dest.Agree)
	}
	if dest.Accepted != 33 || dest.Pending != 0 {
		t.Fatalf("outcome labels: accepted %d pending %d, want 33/0", dest.Accepted, dest.Pending)
	}
	bar, ok := dest.SuggestedBar(0.9, 20)
	if !ok {
		t.Fatal("no bar off 33 settled comparisons")
	}
	if bar != 0.6 {
		t.Fatalf("bar %.2f, want 0.60 - below it the mid-confidence false positives pull "+
			"agreement under the target", bar)
	}
}

// SCORING.md Phase 1: the labels an outcome may settle on.
func TestSettleOnlyOnEvidenceThatLabels(t *testing.T) {
	t.Setenv(OutcomeSettleEnv, "1h")
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	l := &Ledger{}
	add := func(id string, checks ...CheckResult) {
		l.RecordOutcome(OutcomeEvidence{TaskID: id, Status: AcceptancePending})
		l.NoteDelivery(id, "/repo", nil, 900, LegGLM, EffortMedium, "glm-5.3", 1)
		l.OutcomeFor(id).DeliveredAt = old
		for _, c := range checks {
			l.RecordCheckResult(id, c)
		}
	}
	add("judge-poor", CheckResult{Command: JudgeCheck, Passed: false, ExitCode: 1, Source: "solo"})
	add("own-tests", CheckResult{Command: "go test ./...", Passed: true, Source: "tests", TestsEdited: true})
	add("old-failure", CheckResult{Command: "go test ./...", Passed: false, ExitCode: 1, Source: "tests", Baseline: "failing"})
	add("unknown-failure", CheckResult{Command: "go test ./...", Passed: false, ExitCode: 1, Source: "tests"})
	add("real-pass", CheckResult{Command: "go test ./...", Passed: true, Source: "tests"})
	l.SettleOutcomes(now)

	for _, id := range []string{"judge-poor", "own-tests", "old-failure", "unknown-failure"} {
		if s := l.OutcomeFor(id).Status; s != AcceptancePending {
			t.Errorf("%s settled as %s on evidence that does not label", id, s)
		}
	}
	if o := l.OutcomeFor("real-pass"); o.Status != AcceptanceAccepted || o.DecidedBy != DecidedByChecks {
		t.Errorf("a pre-existing suite that passed settles: got %s by %s", o.Status, o.DecidedBy)
	}
}

// Outcomes settled on silence or on the judge's grade alone go back to
// pending, once; a human verdict and objective evidence stand.
func TestStaleSettlementsAreUnsettled(t *testing.T) {
	now := time.Now()
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "silence", Status: AcceptanceAccepted, DecidedBy: DecidedBySilence, SettledAt: now, AcceptedAt: now})
	l.RecordOutcome(OutcomeEvidence{TaskID: "judge", Status: AcceptanceAccepted, DecidedBy: DecidedByChecks, SettledAt: now, AcceptedAt: now,
		Checks: []CheckResult{{Command: JudgeCheck, Passed: true, Source: "solo"}}})
	l.RecordOutcome(OutcomeEvidence{TaskID: "tested", Status: AcceptanceAccepted, DecidedBy: DecidedByChecks, SettledAt: now, AcceptedAt: now,
		Checks: []CheckResult{{Command: "go test ./...", Passed: true, Source: "tests"}}})
	l.RecordOutcome(OutcomeEvidence{TaskID: "reviewed", Status: AcceptanceAccepted, DecidedBy: DecidedBySilence, SettledAt: now, AcceptedAt: now,
		Review: &TaskReview{Verdict: "accept"}})
	l.SettleOutcomes(now)
	if s := l.OutcomeFor("silence").Status; s != AcceptancePending {
		t.Errorf("silence acceptance kept: %s", s)
	}
	if o := l.OutcomeFor("judge"); o.Status != AcceptancePending || !o.AcceptedAt.IsZero() {
		t.Errorf("judge-only acceptance kept: %s", o.Status)
	}
	if s := l.OutcomeFor("tested").Status; s != AcceptanceAccepted {
		t.Errorf("a passed test suite is still acceptance: %s", s)
	}
	if s := l.OutcomeFor("reviewed").Status; s != AcceptanceAccepted {
		t.Errorf("a human verdict is never overwritten: %s", s)
	}
}

// A commit keeps the lines of the last task that wrote them, and its credit
// is shared among the tasks it accepts.
func TestCommitCreditsTheLastWriter(t *testing.T) {
	now := time.Now()
	orig := CommitsTouching
	t.Cleanup(func() { CommitsTouching = orig })
	commitAt := now.Add(-time.Minute)
	CommitsTouching = func(_ context.Context, dir string, since time.Time, files []string) (CommitRecord, bool) {
		return CommitRecord{SHA: "squash", At: commitAt, Files: files}, true
	}
	l := &Ledger{}
	deliver := func(id string, at time.Time, files ...string) {
		l.RecordOutcome(OutcomeEvidence{TaskID: id, Status: AcceptancePending})
		l.NoteDelivery(id, "/repo", files, 900, LegGLM, EffortMedium, "glm-5.3", 1)
		l.OutcomeFor(id).DeliveredAt = at
	}
	deliver("redone", now.Add(-30*time.Minute), "a.go")
	deliver("redo", now.Add(-20*time.Minute), "a.go")
	deliver("other", now.Add(-10*time.Minute), "b.go")
	l.SweepCommits(context.Background(), now)

	if l.OutcomeFor("redone").Commit != nil {
		t.Error("a task whose lines a later task rewrote is not kept by the commit")
	}
	for _, id := range []string{"redo", "other"} {
		c := l.OutcomeFor(id).Commit
		if c == nil {
			t.Fatalf("%s: the commit kept its lines", id)
		}
		if c.Shared != 2 {
			t.Errorf("%s: shared %d, want 2", id, c.Shared)
		}
	}
}

func TestSettledOutcomesRecordTheirCost(t *testing.T) {
	now := time.Now()
	l := &Ledger{}
	start := now.Add(-10 * time.Minute)
	l.RecordTaskState(TaskState{TaskID: "t1", State: StateSucceeded, StartedAt: start})
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending, Attempts: 3})
	l.NoteDelivery("t1", "/repo", []string{"a.go"}, 900, LegGLM, EffortMedium, "glm-5.3", 3)
	l.OutcomeFor("t1").DeliveredAt = now.Add(-4 * time.Minute)
	l.RecordCommitEvidence("t1", CommitRecord{SHA: "abc"})
	l.SettleOutcomes(now)
	o := l.OutcomeFor("t1")
	if o.Rework != 2 {
		t.Errorf("rework %d, want 2 (two attempts past the first)", o.Rework)
	}
	if o.TimeToDeliveredMs != (6 * time.Minute).Milliseconds() {
		t.Errorf("time to delivered %dms, want 6m", o.TimeToDeliveredMs)
	}
}

func TestIsTestFile(t *testing.T) {
	for _, p := range []string{"pkg/a_test.go", "tests/test_x.py", "app/foo_test.py", "src/x.test.ts", "web/a.spec.js", "src/__tests__/a.js", "crates/x/tests/it.rs"} {
		if !IsTestFile(p) {
			t.Errorf("%s is a test", p)
		}
	}
	for _, p := range []string{"pkg/a.go", "src/latest.py", "README.md", "src/contest.ts"} {
		if IsTestFile(p) {
			t.Errorf("%s is not a test", p)
		}
	}
}
