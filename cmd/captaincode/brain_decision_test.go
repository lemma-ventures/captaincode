package main

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ROADMAP M2.1: a routing choice must leave behind the field it was made
// against, not only its winner.

func TestFastPathRecordsDecisionEvidence(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "")
	t.Setenv("CAPTAIN_EXPLORE", "0")
	t.Setenv("CAPTAIN_VALUE_TAU", "5,7")
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGemini: true}
	b.ledger.Cooldown(captaincode.LegGemini, time.Hour)

	task := "tighten the wording of this paragraph and keep the argument intact"
	resp, _ := routeLeg(t, b, task)

	// The decision is pending until the turn that runs it has an identity:
	// a fast-pathed turn asks no model and must not mint a task row early.
	require.Empty(t, b.ledger.Decisions, "a routing choice minted a ledger row before the turn existed")
	taskID := b.openTask(task)

	d, ok := b.ledger.DecisionFor(taskID)
	require.True(t, ok, "the decision never reached the task identity its charges hang off")
	assert.Equal(t, captaincode.PathValue, d.Path)
	assert.Equal(t, captaincode.Leg(resp.Leg), d.Chosen)
	assert.Contains(t, d.Policy.Version, "tau5.00", "the fingerprint must carry the bar that actually ranked - the trivial τ, not the medium one")

	// The cooled leg is not absent from the record - it is excluded, with the
	// reason. "Not chosen" and "not eligible" are different answers.
	var cooled string
	for _, c := range d.Excluded() {
		if c.Leg == captaincode.LegGemini {
			cooled = c.Excluded
		}
	}
	assert.Contains(t, cooled, "cooling down", "the cooled leg left no trace in the decision")
	for _, c := range d.Considered() {
		assert.Empty(t, c.Excluded)
		assert.NotZero(t, c.Prior, "%s: the quality estimate arrived without its prior", c.Leg)
	}
}

// Exploration must read as a deliberate departure from the ranking, naming
// what it passed over - otherwise the record shows a router that preferred
// the runner-up and no one can reproduce the ranking.
func TestExploredDecisionNamesThePassedOverLeg(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "")
	t.Setenv("CAPTAIN_VALUE_TAU", "5,7")
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGemini: true}
	b.exploreFn = func(captaincode.Class) bool { return true }

	task := "tighten the wording of this paragraph and keep the argument intact"
	resp, _ := routeLeg(t, b, task)
	d, ok := b.ledger.DecisionFor(b.openTask(task))
	require.True(t, ok)
	assert.True(t, d.Explored)
	assert.NotEqual(t, d.PassedOver, d.Chosen)
	assert.Equal(t, captaincode.Leg(resp.Leg), d.Chosen)
	assert.Equal(t, d.PassedOver, d.Considered()[0].Leg, "the passed-over leg must be the top of the ranking")
}

// A second turn of the same prompt is a new decision: re-attaching the first
// would credit it with a ranking it never ran (same rule as the route-time
// task identity it rides beside).
func TestDecisionIsConsumedNotReused(t *testing.T) {
	b := teamBrain()
	b.recordDecision("some task", captaincode.Decision{Chosen: captaincode.LegGLM, Path: captaincode.PathValue})
	first := b.openTask("some task")
	second := b.openTask("some task")
	_, ok := b.ledger.DecisionFor(first)
	require.True(t, ok)
	_, ok = b.ledger.DecisionFor(second)
	assert.False(t, ok, "the second turn inherited the first turn's decision")
}

// A decision older than the wrapper turn that follows it is dropped rather
// than used to explain an unrelated turn.
func TestStaleDecisionIsDiscarded(t *testing.T) {
	b := teamBrain()
	b.recordDecision("some task", captaincode.Decision{Chosen: captaincode.LegGLM, Path: captaincode.PathValue})
	b.rtmu.Lock()
	p := b.pendingDecisions[truncate("some task", 120)]
	p.at = time.Now().Add(-2 * decisionTTL)
	b.pendingDecisions[truncate("some task", 120)] = p
	b.rtmu.Unlock()
	_, ok := b.ledger.DecisionFor(b.openTask("some task"))
	assert.False(t, ok, "a stale decision explained a later turn")
}

// `captain why` must print the field, not only the winner: the report is the
// deliverable of M2.1, so its shape is tested rather than assumed.
func TestWhyPrintsTheDecisionEvidence(t *testing.T) {
	l := &captaincode.Ledger{}
	l.RecordDecision(captaincode.Decision{
		TaskID: "task_1", Task: "tidy the README", Class: captaincode.ClassTrivial, Domain: captaincode.DomainEditorial,
		Path: captaincode.PathValue, Chosen: captaincode.LegGemini, DecidedMs: 3,
		Explored: true, PassedOver: captaincode.LegGLM,
		Policy: captaincode.PolicyFor(captaincode.ClassTrivial, 16000, 0.1),
		Candidates: []captaincode.Scored{
			{Leg: captaincode.LegGLM, Quality: 8.2, Prior: 8.2, Value: 0.21, LatencyMs: 4200, Samples: 9, ScoredRuns: 3, LastRun: time.Now().Add(-2 * time.Hour)},
			{Leg: captaincode.LegGemini, Quality: 7.8, Prior: 7.8, Value: 0.19, CostUSD: 0.006},
			{Leg: captaincode.LegCodexCLI, Excluded: "frontier-class: reserved for /frontier, /quality and the director"},
		},
	})

	out := captureStdout(t, func() { printDecision(l, "task_1") })

	assert.Contains(t, out, "value → gemini (trivial/editorial)")
	assert.Contains(t, out, "policy: value/1")
	assert.Contains(t, out, "EXPLORE: passed over the top-ranked glm")
	assert.Contains(t, out, "prior 8.2 + 3 scored of 9 runs")
	assert.Contains(t, out, "2h ago")
	assert.Contains(t, out, "no local scores")
	assert.Contains(t, out, "no observed duration")
	assert.Contains(t, out, "✗ codex-cli frontier-class")
	assert.NotContains(t, out, "✗ gemini", "an eligible candidate was printed as an exclusion")
}

func TestWhyShareExcludesExplicitChoices(t *testing.T) {
	now := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.UTC)
	l := &captaincode.Ledger{}
	for _, d := range []captaincode.Decision{
		{TaskID: "auto-frontier", At: now.Add(-24 * time.Hour), Path: captaincode.PathValue, Chosen: captaincode.LegCodexCLI, TiePolicy: "cheap-capable-v1"},
		{TaskID: "auto-cheap", At: now.Add(-48 * time.Hour), Path: captaincode.PathValue, Chosen: captaincode.LegGLM, TiePolicy: "cheap-capable-v1"},
		{TaskID: "forced", At: now.Add(-time.Hour), Path: captaincode.PathForced, Chosen: captaincode.LegCodexCLI},
		{TaskID: "lane", At: now.Add(-time.Hour), Path: captaincode.PathLane, Chosen: captaincode.LegGLM},
		{TaskID: "preference", At: now.Add(-time.Hour), Path: captaincode.PathValue, Chosen: captaincode.LegGLM, Preference: "quality"},
		{TaskID: "month-frontier", At: now.Add(-20 * 24 * time.Hour), Path: captaincode.PathDirector, Chosen: captaincode.LegCodexCLI},
		{TaskID: "first-rule", At: now.Add(-40 * 24 * time.Hour), Path: captaincode.PathValue, Chosen: captaincode.LegGLM, TiePolicy: "cheap-capable-v1"},
	} {
		l.RecordDecision(d)
	}

	out := formatFrontierShare(l, []int{7, 30}, now)
	assert.Contains(t, out, "7 days: 1/2 automatic decisions on frontier legs (50.0%)")
	assert.Contains(t, out, "30 days: 2/3 automatic decisions on frontier legs (66.7%)")
	assert.Contains(t, out, "cheap-capable-v1 first recorded: 2026-10-06")
}

func BenchmarkFormatFrontierShare(b *testing.B) {
	now := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.UTC)
	l := &captaincode.Ledger{}
	for i := 0; i < 120; i++ {
		leg := captaincode.LegGLM
		if i%3 == 0 {
			leg = captaincode.LegCodexCLI
		}
		l.RecordDecision(captaincode.Decision{TaskID: fmt.Sprintf("task-%d", i), At: now.Add(-time.Duration(i%60) * 24 * time.Hour), Path: captaincode.PathValue, Chosen: leg, TiePolicy: "cheap-capable-v1"})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = formatFrontierShare(l, []int{7, 30}, now)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	saved := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = saved
	require.NoError(t, w.Close())
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(b)
}

// SCORING.md Phase 0: Explored meant "not the value ranking's first row",
// which marked 51 of 81 expected-policy decisions as explored when that
// policy simply ranked differently. It now means the exploration draw.
func TestExploredMeansTheExplorationDraw(t *testing.T) {
	b := teamBrain()
	rows := []captaincode.Scored{{Leg: captaincode.LegGLM, Quality: 8, Value: 0.5}, {Leg: captaincode.LegGemini, Quality: 8, Value: 0.4}}
	tr := captaincode.TriageResult{Class: captaincode.ClassMedium, Domain: captaincode.DomainCode}
	d := b.valueDecision(tr, captaincode.LegGemini, rows, 1, "the expected policy ranked gemini first", false, "")
	assert.False(t, d.Explored, "a policy that ranks differently is not exploring")
	d = b.valueDecision(tr, captaincode.LegGemini, rows, 1, "explored", true, captaincode.LegGLM)
	assert.True(t, d.Explored)
	assert.Equal(t, captaincode.LegGLM, d.PassedOver)
}

// Evidence a router collected is only comparable across legs when weighted
// by how likely each leg was to be tried: every decision says.
func TestDecisionsRecordPickProbabilities(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "")
	t.Setenv("CAPTAIN_VALUE_TAU", "5,7")
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGemini: true}
	b.exploreFn = func(captaincode.Class) bool { return true }

	task := "tighten the wording of this paragraph and keep the argument intact"
	routeLeg(t, b, task)
	d, ok := b.ledger.DecisionFor(b.openTask(task))
	require.True(t, ok)
	require.NotEmpty(t, d.Propensities)
	var sum float64
	for _, p := range d.Propensities {
		sum += p
	}
	assert.InDelta(t, 1.0, sum, 1e-9, "the probabilities cover the policy: %v", d.Propensities)
	assert.Greater(t, d.Propensities[d.Chosen], 0.0, "the explored leg had a chance: %v", d.Propensities)

	b.recordDecision("a forced task", captaincode.Decision{Chosen: captaincode.LegGLM, Path: captaincode.PathForced})
	d, ok = b.ledger.DecisionFor(b.openTask("a forced task"))
	require.True(t, ok)
	assert.Equal(t, map[captaincode.Leg]float64{captaincode.LegGLM: 1}, d.Propensities, "a deterministic pick")
}

// Two checkouts can run the same prompt at once. Each keeps the ranking
// that was made for it.
func TestSamePromptKeepsOneDecisionPerCheckout(t *testing.T) {
	b := teamBrain()
	oak := t.TempDir() + "/oak"
	elm := t.TempDir() + "/elm"
	task := "define what should be the next items to work on"
	b.recordDecisionAt(oak, task, captaincode.Decision{Chosen: captaincode.LegClaude, Path: captaincode.PathValue})
	b.recordDecisionAt(elm, task, captaincode.Decision{Chosen: captaincode.LegCodex, Path: captaincode.PathValue})
	oakID := b.openTaskAt(oak, task)
	elmID := b.openTaskAt(elm, task)
	require.NotEqual(t, oakID, elmID)
	oakDec, ok := b.ledger.DecisionFor(oakID)
	require.True(t, ok)
	elmDec, ok := b.ledger.DecisionFor(elmID)
	require.True(t, ok)
	assert.Equal(t, captaincode.LegClaude, oakDec.Chosen)
	assert.Equal(t, captaincode.LegCodex, elmDec.Chosen)
	assert.Equal(t, oak, oakDec.Workspace)
	assert.Equal(t, elm, elmDec.Workspace)
	_, ok = b.ledger.DecisionFor(b.openTaskAt(oak, task))
	assert.False(t, ok, "the oak decision was already used")
}

func TestWorkspaceLabelRefusesParentAndRelative(t *testing.T) {
	assert.Equal(t, "", workspaceLabel("oak"))
	assert.Equal(t, "", workspaceLabel("/tmp/../etc"))
	assert.Equal(t, "", workspaceLabel("/tmp/oak\n"))
	assert.Equal(t, "", workspaceLabel("/"))
	assert.Equal(t, "/tmp/oak", workspaceLabel("  /tmp/oak  "))
	assert.Equal(t, "/tmp/oak", workspaceLabel("/tmp/oak/"))
	assert.Equal(t, "/tmp/oak", workspaceLabel("/tmp//oak/."))
}

// The same directory is one checkout. A trailing slash is not a second
// ranking, and a different directory with the same last component is not
// this one.
func TestCheckoutSpellingSharesOneDecision(t *testing.T) {
	b := teamBrain()
	task := "define what should be the next items to work on"
	b.recordDecisionAt("/checkouts/oak/", task, captaincode.Decision{Chosen: captaincode.LegClaude, Path: captaincode.PathValue})
	b.recordDecisionAt("/other/oak", task, captaincode.Decision{Chosen: captaincode.LegCodex, Path: captaincode.PathValue})
	oakID := b.openTaskAt("/checkouts//oak/.", task)
	otherID := b.openTaskAt("/other/oak/", task)
	require.NotEqual(t, oakID, otherID)
	oakDec, ok := b.ledger.DecisionFor(oakID)
	require.True(t, ok)
	otherDec, ok := b.ledger.DecisionFor(otherID)
	require.True(t, ok)
	assert.Equal(t, captaincode.LegClaude, oakDec.Chosen)
	assert.Equal(t, "/checkouts/oak", oakDec.Workspace)
	assert.Equal(t, captaincode.LegCodex, otherDec.Chosen)
	assert.Equal(t, "/other/oak", otherDec.Workspace)
}

func BenchmarkTurnKey(b *testing.B) {
	dir := "/checkouts/oak"
	task := "define what should be the next items to work on based on previous rounds"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if turnKey(dir, task) == "" {
			b.Fatal("empty key")
		}
	}
}
