package main

import (
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
