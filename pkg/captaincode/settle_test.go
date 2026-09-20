package captaincode

import (
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
		Checks: []CheckResult{{Command: "director:assess", Passed: false, ExitCode: 1, At: now}}})
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
