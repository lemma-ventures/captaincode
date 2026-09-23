package captaincode

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The routing stages (2026-09-22): the per-task effort decision, the wider
// tier-0 medium margin, the routing journal, the success estimator, the
// expected-cost ranking, the bandit gate, the objective settlement signals,
// the typed director pick and the solo verification helpers.

func TestDecideEffortRungs(t *testing.T) {
	assert.Equal(t, EffortMax, DecideEffort("frontier", ClassTrivial, LegClaude, false, 1), "a stated /frontier is max whatever the class")
	assert.Equal(t, EffortLow, DecideEffort("", ClassTrivial, LegGLM, false, 1))
	assert.Equal(t, EffortMedium, DecideEffort("", ClassMedium, LegGLM, false, 1))
	assert.Equal(t, EffortHigh, DecideEffort("", ClassHigh, LegGLM, false, 1), "high on a mid-tier leg thinks hard")
	assert.Equal(t, EffortMedium, DecideEffort("", ClassHigh, LegClaude, false, 1), "frontier work defaults to medium on a frontier-class leg")
	assert.Equal(t, EffortHigh, DecideEffort("", ClassHigh, LegClaude, true, 1), "irreversible work climbs a rung")
	assert.Equal(t, EffortXHigh, DecideEffort("", ClassHigh, LegClaude, true, 2), "a second attempt climbs another")
	assert.Equal(t, EffortXHigh, DecideEffort("", ClassHigh, LegClaude, true, 5), "…and stops at the ceiling; only /frontier reaches max")
	t.Setenv("CAPTAIN_EFFORT_CEILING", "high")
	assert.Equal(t, EffortHigh, DecideEffort("", ClassHigh, LegClaude, true, 3), "the ceiling is configurable")
	assert.Equal(t, EffortMedium, NextEffort(""), "no effort climbs to medium")
	assert.Equal(t, EffortMax, NextEffort(EffortMax), "max stays max")
	assert.Greater(t, EffortXHigh.CostMultiplier(), EffortMedium.CostMultiplier(), "reasoning tokens cost")
}

func TestTriageMediumEarnsConfidenceAndNamesItself(t *testing.T) {
	anchored := TriageTask("implement the retry loop for the webhook client and make it back off on 429")
	assert.Equal(t, ClassMedium, anchored.Class)
	assert.GreaterOrEqual(t, anchored.Confidence, 0.6, "an anchored, ordinary-length medium task no longer pays the free-leg classify: %s", anchored.Why)
	assert.Equal(t, TriageByHeuristic, anchored.By)
	bare := TriageTask("hmm the thing")
	assert.Less(t, bare.Confidence, 0.6, "a bare fragment is still a guess")
	assert.False(t, anchored.Irreversible)
	assert.True(t, TriageTask("run the migration against prod and drop the old table").Irreversible, "no cheap undo")
	assert.False(t, TriageTask("explain how the migration framework works").Irreversible, "mentioning a migration is not performing one")
}

func TestJevTriageAnswersCarryTheTwoNewPoints(t *testing.T) {
	resp := S1Response{Model: "jev-1.13.0", Answers: map[string]S1Answer{
		PointClass:        {Type: "choice", Choice: "medium", Confidence: 0.9},
		PointDomain:       {Type: "choice", Choice: "code", Confidence: 0.8},
		PointIrreversible: {Type: "noul", Noul: 0.85},
		PointMidTier:      {Type: "noul", Noul: 0.3},
	}}
	tr, err := triageFromAnswers(resp, Result{DurationMs: 300})
	require.NoError(t, err)
	assert.Equal(t, TriageByJev, tr.By)
	assert.True(t, tr.Irreversible)
	assert.InDelta(t, 0.3, tr.MidTierP, 1e-9)
	assert.InDelta(t, 0.8, tr.Confidence, 1e-9, "the lower of class and domain")
}

func journalLedger(t *testing.T) (*Ledger, string) {
	t.Helper()
	dir := t.TempDir()
	l := &Ledger{Sessions: map[string]string{}, Threads: map[string]ThreadRef{}, Cooldowns: map[Leg]time.Time{}, path: filepath.Join(dir, "state.json")}
	return l, filepath.Join(dir, "routing.jsonl")
}

func TestRoutingJournalAppendsDecisionsEventsAndSettledOutcomes(t *testing.T) {
	l, path := journalLedger(t)
	l.RecordDecision(Decision{TaskID: "t1", Task: "fix the parser", Class: ClassMedium, Chosen: LegGLM, Path: PathValue, Effort: EffortMedium})
	l.Record(Event{TaskID: "t1", Task: "fix the parser", Leg: LegGLM, Outcome: "ok", Effort: EffortMedium})
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	l.NoteDelivery("t1", "", nil, 500, LegGLM, EffortMedium, "glm-5.3", 1)
	require.NoError(t, l.RecordTaskReview("t1", ReviewAccept, "me", "", false))
	recs := ReadRoutingLog(path, 0)
	kinds := map[string]int{}
	for _, r := range recs {
		kinds[r.Kind]++
	}
	assert.Equal(t, 1, kinds[RoutingKindDecision])
	assert.Equal(t, 1, kinds[RoutingKindEvent])
	assert.Equal(t, 1, kinds[RoutingKindOutcome], "the review settled it: journaled")
	samples := JoinRouting(recs)
	require.Len(t, samples, 1)
	assert.True(t, samples[0].Labeled())
	assert.True(t, samples[0].Success())
	assert.Equal(t, ModelIDAt(LegGLM, EffortMedium), samples[0].Events[0].Model, "the event was stamped with the model it ran as")
	assert.Equal(t, 1, LabeledCount(RoutingHistory(l)))

	mem := &Ledger{}
	mem.RecordDecision(Decision{TaskID: "t2", Chosen: LegGLM})
	assert.Empty(t, mem.journalPath(), "an in-memory ledger journals nowhere")
	t.Setenv(RoutingLogEnv, "0")
	assert.Empty(t, l.journalPath(), "CAPTAIN_ROUTING_LOG=0 turns it off")
}

func TestSuccessEstimatorLearnsFromNeighboursAndDiscountsOtherVersions(t *testing.T) {
	now := time.Now()
	mk := func(id, task string, leg Leg, model string, ok bool) RoutingSample {
		st := AcceptanceRejected
		if ok {
			st = AcceptanceAccepted
		}
		return RoutingSample{
			Decision: Decision{TaskID: id, Task: task, Class: ClassMedium, Domain: DomainCode, Chosen: leg, Effort: EffortMedium, At: now.Add(-time.Hour)},
			Events:   []Event{{TaskID: id, Leg: leg, Outcome: "ok", Effort: EffortMedium, Model: model}},
			Outcome:  &OutcomeEvidence{TaskID: id, Status: st, DecidedBy: DecidedByChecks},
		}
	}
	cur := ModelIDAt(LegGLM, EffortMedium)
	var hist []RoutingSample
	for i := 0; i < 8; i++ {
		hist = append(hist, mk("ok"+string(rune('a'+i)), "fix the webhook retry loop in client.go", LegGLM, cur, true))
	}
	hist = append(hist, mk("pending", "fix the webhook retry loop", LegGLM, cur, false))
	hist[len(hist)-1].Outcome = &OutcomeEvidence{TaskID: "pending", Status: AcceptancePending}
	e := NewSuccessEstimator(hist)
	assert.Equal(t, 8, e.Samples(), "pending outcomes are not weak successes")

	est := e.Estimate("fix the webhook retry in the client", ClassMedium, DomainCode, LegGLM, EffortMedium, 0, now)
	assert.False(t, est.PriorOnly)
	assert.Greater(t, est.N, 3)
	assert.Greater(t, est.P, EffortPrior(LegGLM, EffortMedium), "eight accepted neighbours pull P above the prior")

	far := e.Estimate("rewrite the abstract of the paper in my style", ClassMedium, DomainEditorial, LegGLM, EffortMedium, 0, now)
	assert.LessOrEqual(t, far.Weight, est.Weight, "unrelated tasks weigh less")

	other := NewSuccessEstimator([]RoutingSample{mk("old", "fix the webhook retry loop in client.go", LegGLM, "glm-4.9", false)})
	old := other.Estimate("fix the webhook retry loop in client.go", ClassMedium, DomainCode, LegGLM, EffortMedium, 0, now)
	assert.Greater(t, old.P, 0.3, "one failure on an older model version is discounted, not damning")

	none := e.Estimate("anything", ClassMedium, DomainCode, LegKimi, EffortMedium, 0.9, now)
	assert.True(t, none.PriorOnly, "a leg with no history is a prior")
	assert.Greater(t, none.P, EffortPrior(LegKimi, EffortMedium), "jev's mid-tier estimate lifts a mid-tier leg's prior")
	assert.InDelta(t, EffortPrior(LegClaude, EffortMedium), e.Estimate("anything", ClassMedium, DomainCode, LegClaude, EffortMedium, 0.9, now).P, 1e-9, "…but not a frontier leg's")
}

func TestExpectedRankPricesTheRepair(t *testing.T) {
	t.Setenv("CAPTAIN_LATENCY_TOL", "90s,10m,30m")
	stats := map[Leg]LegStats{LegGLM: {AvgDurationMs: 60_000}, LegKimi: {AvgDurationMs: 40 * 60_000}}
	rows := ExpectedRank(ExpectedInput{Task: "fix it", Class: ClassMedium, Domain: DomainCode, Legs: []Leg{LegGLM, LegKimi},
		Stats: stats, Tokens: 20_000, Now: time.Now(), BaseEffort: EffortMedium})
	require.Len(t, rows, 2)
	for _, r := range rows {
		assert.Equal(t, EffortMedium, r.Effort)
		assert.Greater(t, r.RepairUSD, 0.0, "a redo is never free")
		assert.InDelta(t, r.CostUSD+(1-r.P)*r.RepairUSD, r.Expected/func() float64 {
			if r.LatencyOver {
				return float64(r.LatencyMs) / (10 * 60_000)
			}
			return 1
		}(), 1e-9)
	}
	var kimi ExpectedRow
	for _, r := range rows {
		if r.Leg == LegKimi {
			kimi = r
		}
	}
	assert.True(t, kimi.LatencyOver, "forty minutes on a medium task is over the tolerance")
	assert.Equal(t, LegGLM, rows[0].Leg, "the over-tolerance leg sorts after")

	t.Setenv("CAPTAIN_SUCCESS_FLOOR", "0.99,0.99,0.99")
	rows = ExpectedRank(ExpectedInput{Task: "fix it", Class: ClassMedium, Legs: []Leg{LegGLM}, Stats: stats, Tokens: 1000, Now: time.Now(), BaseEffort: EffortMedium})
	assert.NotEmpty(t, rows[0].Excluded, "under the success floor: kept, marked, not dispatched to")
	assert.Empty(t, EligibleExpected(rows))
	assert.Contains(t, FormatExpected(rows, "", ""), "below the medium floor")
}

func TestBurnPressureReadsTheWindow(t *testing.T) {
	now := time.Now()
	events := []Event{
		{Leg: LegClaude, Duration: 30 * 60_000, At: now.Add(-6 * time.Hour)}, // outside the window
		{Leg: LegClaude, Duration: 45 * 60_000, At: now.Add(-time.Hour)},
		{Leg: LegGLM, Duration: 45 * 60_000, At: now.Add(-time.Hour)},
	}
	p := BurnPressure(events, LegClaude, now, 5*time.Hour, 90*time.Minute)
	assert.InDelta(t, 0.5, p, 1e-9, "45 of 90 minutes burnt inside the window")
	assert.Equal(t, 1.0, BurnPressure(events, LegClaude, now, 5*time.Hour, 10*time.Minute), "clamped at 1")
	assert.Equal(t, 0.0, BurnPressure(events, LegKimi, now, 5*time.Hour, 90*time.Minute))
}

func TestBanditRefusesBelowTheGateAndPicksAboveIt(t *testing.T) {
	rows := []ExpectedRow{{Leg: LegGLM, Effort: EffortMedium, P: 0.65, N: 3, CostUSD: 0.02}, {Leg: LegKimi, Effort: EffortMedium, P: 0.5, N: 3, CostUSD: 0.02}}
	t.Setenv("CAPTAIN_BANDIT_MIN_LABELED", "100")
	pick := ThompsonPick(rows, 10, rand.New(rand.NewSource(1)))
	assert.Contains(t, pick.Refused, "bandit gate")
	wins := map[Leg]int{}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 200; i++ {
		p := ThompsonPick(rows, 500, rng)
		require.Empty(t, p.Refused)
		wins[p.Row.Leg]++
	}
	assert.Greater(t, wins[LegGLM], wins[LegKimi], "the better-supported arm wins most pulls: %v", wins)
	assert.Greater(t, wins[LegKimi], 0, "…and the other still gets explored")
	assert.Contains(t, ThompsonPick(nil, 500, rng).Refused, "no eligible arm")
}

func TestDistillRowsAreTheLabelledHistory(t *testing.T) {
	now := time.Now()
	hist := []RoutingSample{
		{Decision: Decision{TaskID: "a", Task: "fix the parser", Class: ClassMedium, Domain: DomainCode, Chosen: LegGLM, Effort: EffortMedium, Path: PathValue, TriageBy: TriageByHeuristic, Confidence: 0.7, At: now},
			Events:  []Event{{TaskID: "a", Leg: LegGLM, Outcome: "ok", Model: "glm-5.3", CostUSD: 0.03, Duration: 1000, Quality: 8}},
			Outcome: &OutcomeEvidence{TaskID: "a", Status: AcceptanceAccepted, DecidedBy: DecidedByCommit}},
		{Decision: Decision{TaskID: "b", Task: "pending one", Chosen: LegGLM, At: now}, Outcome: &OutcomeEvidence{TaskID: "b", Status: AcceptancePending}},
	}
	rows := DistillRows(hist)
	require.Len(t, rows, 1)
	assert.Equal(t, "a", rows[0].TaskID)
	assert.True(t, rows[0].Success)
	assert.Equal(t, "commit", rows[0].DecidedBy)
	assert.Equal(t, "glm-5.3", rows[0].Model)
	assert.Equal(t, 8.0, rows[0].Quality)
	path := filepath.Join(t.TempDir(), "d", "distill.jsonl")
	n, err := WriteDistill(path, rows)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	raw, _ := os.ReadFile(path)
	assert.Contains(t, string(raw), `"decided_by":"commit"`)
}

func TestSettleFromObjectiveSignals(t *testing.T) {
	t.Setenv(OutcomeSettleEnv, "1h")
	now := time.Now()
	l := &Ledger{}

	l.RecordOutcome(OutcomeEvidence{TaskID: "commit", Status: AcceptancePending})
	l.NoteDelivery("commit", "/repo", []string{"a.go"}, 900, LegGLM, EffortMedium, "glm-5.3", 1)
	l.RecordCommitEvidence("commit", CommitRecord{SHA: "abc", Subject: "keep it"})

	l.RecordOutcome(OutcomeEvidence{TaskID: "reprompt", Status: AcceptancePending})
	l.NoteDelivery("reprompt", "/repo", nil, 900, LegGLM, EffortMedium, "glm-5.3", 1)
	l.RecordReprompt("reprompt", "no, that's not what I asked", now)

	l.RecordOutcome(OutcomeEvidence{TaskID: "silence", Status: AcceptancePending})
	l.NoteDelivery("silence", "/repo", nil, 900, LegGLM, EffortMedium, "glm-5.3", 1)
	l.OutcomeFor("silence").DeliveredAt = now.Add(-2 * time.Hour)

	l.RecordOutcome(OutcomeEvidence{TaskID: "fresh", Status: AcceptancePending})
	l.NoteDelivery("fresh", "/repo", nil, 900, LegGLM, EffortMedium, "glm-5.3", 1)

	l.RecordOutcome(OutcomeEvidence{TaskID: "nothing", Status: AcceptancePending})
	l.OutcomeFor("nothing").UpdatedAt = now.Add(-2 * time.Hour)

	l.RecordOutcome(OutcomeEvidence{TaskID: "failed-test", Status: AcceptancePending})
	l.NoteDelivery("failed-test", "/repo", []string{"a.go"}, 900, LegGLM, EffortMedium, "glm-5.3", 1)
	l.RecordCommitEvidence("failed-test", CommitRecord{SHA: "def"})
	l.RecordCheckResult("failed-test", CheckResult{Command: "go test ./...", ExitCode: 1, Passed: false, Source: "tests"})

	assert.Equal(t, 4, l.SettleOutcomes(now))
	status := func(id string) (AcceptanceStatus, OutcomeDecider) {
		o := l.OutcomeFor(id)
		return o.Status, o.DecidedBy
	}
	s, by := status("commit")
	assert.Equal(t, AcceptanceAccepted, s)
	assert.Equal(t, DecidedByCommit, by, "a commit that kept the files is acceptance, at once")
	s, by = status("reprompt")
	assert.Equal(t, AcceptanceRejected, s)
	assert.Equal(t, DecidedByReprompt, by)
	s, by = status("silence")
	assert.Equal(t, AcceptanceAccepted, s)
	assert.Equal(t, DecidedBySilence, by, "delivered, nothing came back for the window: the weakest honest acceptance")
	s, _ = status("fresh")
	assert.Equal(t, AcceptancePending, s, "the window has not elapsed")
	s, _ = status("nothing")
	assert.Equal(t, AcceptancePending, s, "nothing delivered, nothing checked: nothing to settle")
	s, by = status("failed-test")
	assert.Equal(t, AcceptanceRejected, s)
	assert.Equal(t, DecidedByChecks, by, "a failed check is an objective fact and outranks the commit")

	assert.True(t, CorrectiveReprompt("No, revert that and use the other approach"))
	assert.True(t, CorrectiveReprompt("still broken after your change"))
	assert.False(t, CorrectiveReprompt("now add a unit test for the parser"), "the next task is not a rejection")
}

func TestPickPromptAndParse(t *testing.T) {
	menu := []Scored{{Leg: LegGLM, Value: 0.41, Quality: 8.2, ScoredRuns: 12, CostUSD: 0.03, LatencyMs: 90_000},
		{Leg: LegClaude, Value: 0.35, Quality: 9.5, ScoredRuns: 40, CostUSD: 0, LatencyMs: 300_000}}
	p := BuildPickPrompt("debug the race in the worker drain", ClassHigh, "", menu, LegClaude)
	assert.Contains(t, p, "1. glm: value +0.41")
	assert.Contains(t, p, "2. claude:")
	assert.Contains(t, p, "this is you - allowed for high-class work")
	assert.Contains(t, p, `{"leg":"<a leg from the menu>","class":"<trivial|medium|high>"}`)
	assert.NotContains(t, p, "brief", "no brief: the task is the brief")

	pick, err := parsePick(`Sure. {"leg":"Claude","class":"high"}`, menu)
	require.NoError(t, err)
	assert.Equal(t, LegClaude, pick.Leg)
	assert.Equal(t, ClassHigh, pick.Class)
	_, err = parsePick(`{"leg":"kimi","class":"high"}`, menu)
	assert.ErrorContains(t, err, "not on the menu")
	_, err = parsePick(`I would pick glm because…`, menu)
	assert.ErrorContains(t, err, "no JSON")
	assert.Equal(t, 8*time.Second, DirectorPickTimeout())
}

func TestEffortIndexReadsThePerEffortRow(t *testing.T) {
	idx, ok := EffortIndex(LegClaude, EffortMedium)
	if !ok {
		t.Skip("the compiled feed carries no per-effort row for claude's model")
	}
	top, _ := EffortIndex(LegClaude, EffortMax)
	assert.Greater(t, top, idx, "the plain row is the model at its top setting")
	assert.Greater(t, EffortPrior(LegClaude, EffortMax), EffortPrior(LegClaude, EffortLow))
	assert.LessOrEqual(t, EffortPrior(LegFree, EffortLow), 0.92)
}

func TestSoloVerifyHelpers(t *testing.T) {
	assert.True(t, HasEffortKnob(LegClaude))
	assert.True(t, HasEffortKnob(LegCursor))
	assert.False(t, HasEffortKnob(LegFree), "nothing to climb on the free leg")

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		require.NoError(t, exec_git(dir, args...))
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	run("add", "a.go")
	run("commit", "-q", "-m", "init")
	assert.Empty(t, ChangedFiles(context.Background(), dir), "a clean tree changed nothing")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // edited\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o644))
	assert.Equal(t, []string{"a.go", "b.go"}, ChangedFiles(context.Background(), dir), "tracked and untracked, sorted")

	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t", Status: AcceptancePending})
	l.NoteDelivery("t", dir, []string{"a.go"}, 10, LegGLM, EffortMedium, "glm", 1)
	l.OutcomeFor("t").DeliveredAt = time.Now().Add(time.Second) // after the init commit, at git's second resolution
	assert.Equal(t, 0, l.SweepCommits(context.Background(), time.Now()), "no commit since the delivery")
	time.Sleep(2500 * time.Millisecond)
	run("add", "a.go")
	run("commit", "-q", "-m", "keep the worker's change")
	assert.Equal(t, 1, l.SweepCommits(context.Background(), time.Now()))
	require.NotNil(t, l.OutcomeFor("t").Commit)
	assert.True(t, strings.HasPrefix(l.OutcomeFor("t").Commit.Subject, "keep the worker"))
	assert.Equal(t, 1, l.SettleOutcomes(time.Now()))
	assert.Equal(t, DecidedByCommit, l.OutcomeFor("t").DecidedBy)
}
