package main

import (
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ROADMAP Q5: a workflow gate repair or escalation is one provider call, so
// the task budget settles one attempt for it, at the billed amount. The
// repair used to reserve and reconcile around runWorkerRerouted, which
// reserves and reconciles again: one call counted twice, cost included.

// workflowBudget returns the one budget a workflow turn opened.
func workflowBudget(t *testing.T, b *brain) captaincode.Budget {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Len(t, b.ledger.Budgets, 1, "one workflow turn opens one task budget")
	return b.ledger.Budgets[0]
}

// countingWorker stubs every provider call and counts them.
type countingWorker struct {
	mu    sync.Mutex
	calls int
	res   captaincode.Result
	after func(n int) // runs after call n returns its result
}

func (c *countingWorker) run(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if c.after != nil {
		c.after(n)
	}
	return leg, c.res, nil
}

func (c *countingWorker) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func budgetTestBrain(t *testing.T, w *countingWorker) *brain {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_CWD", home)
	b := teamBrain()
	b.runWorkerFn = w.run
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	return b
}

func TestWorkflowGateRepairChargesOneAttempt(t *testing.T) {
	w := &countingWorker{res: captaincode.Result{Text: "done", CostUSD: 0.01, DurationMs: 5}}
	b := budgetTestBrain(t, w)
	marker := t.TempDir() + "/fixed"
	w.after = func(n int) {
		if n == 2 { // the repair "fixes" it
			require.NoError(t, os.WriteFile(marker, []byte("ok"), 0o644))
		}
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok fix the tests gate: test -f "+marker))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	require.Equal(t, 2, w.count(), "the first attempt and one repair")
	bud := workflowBudget(t, b)
	assert.Equal(t, w.count(), bud.SettledAttempts, "settled attempts equal provider calls")
	assert.Zero(t, bud.ReservedAttempts, "no reservation is left open")
	assert.InDelta(t, 0.02, bud.SettledCostUSD, 1e-9, "each call is charged once")
}

func TestWorkflowEscalationChargesOneAttemptAtBilledAmount(t *testing.T) {
	// No provider bill, only Shield's committed amount: BilledUSD charges the
	// commitment, CostUSD would charge nothing.
	w := &countingWorker{res: captaincode.Result{Text: "claims it is fixed", CostCommitted: 0.05, DurationMs: 5}}
	b := budgetTestBrain(t, w)
	b.escalation = captaincode.EscalationPolicy{MaxRepairs: 1, MaxEscalations: 1}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok fix it gate: false"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	require.Equal(t, 3, w.count(), "the first attempt, one repair and one escalation")
	bud := workflowBudget(t, b)
	assert.Equal(t, w.count(), bud.SettledAttempts, "settled attempts equal provider calls")
	assert.Zero(t, bud.ReservedAttempts, "no reservation is left open")
	assert.InDelta(t, 0.15, bud.SettledCostUSD, 1e-9, "each call is charged once, at its billed amount")

	// The member line uses the same billed amount, labelled as an estimate:
	// a commitment is not a measured bill.
	b.mu.Lock()
	defer b.mu.Unlock()
	var worker *captaincode.Charge
	for i := range b.ledger.Charges {
		c := &b.ledger.Charges[i]
		if c.Kind == captaincode.KindCall && c.Label == "worker" {
			worker = c
		}
	}
	require.NotNil(t, worker, "the stage worker has a member line")
	assert.InDelta(t, 0.05, worker.Usage.CostUSD, 1e-9)
	assert.Equal(t, captaincode.UsageEstimated, worker.Usage.CostStatus)
}

func TestWorkflowEscalationWithNoTargetSettlesNoAttempt(t *testing.T) {
	w := &countingWorker{res: captaincode.Result{Text: "claims it is fixed", CostUSD: 0.01, DurationMs: 5}}
	b := budgetTestBrain(t, w)
	b.escalation = captaincode.EscalationPolicy{MaxRepairs: 1, MaxEscalations: 1}
	// Only the failing leg is allowed, so no stronger leg exists.
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGrok: true}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok fix it gate: false"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	require.Equal(t, 2, w.count(), "the first attempt and one repair; no escalation ran")
	bud := workflowBudget(t, b)
	assert.Equal(t, w.count(), bud.SettledAttempts, "a reservation with no call is released, not settled")
	assert.Zero(t, bud.ReservedAttempts)
	assert.Equal(t, captaincode.StopObjectiveFailed, bud.StopReason)
}

func TestWorkflowGateRepairStopsAtBudget(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "1")
	w := &countingWorker{res: captaincode.Result{Text: "claims it is fixed", CostUSD: 0.01, DurationMs: 5}}
	b := budgetTestBrain(t, w)
	var reviewed string
	b.assessMultiFn = func(_ string, outputs map[string]captaincode.WorkerOutput, _ string) (captaincode.MultiAssessment, error) {
		for _, o := range outputs {
			reviewed = o.Text
		}
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok fix it gate: false"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	assert.Equal(t, 1, w.count(), "the cap allows the first attempt only")
	assert.Contains(t, reviewed, "GATE FAILED - budget exhausted, no retry available.")
	bud := workflowBudget(t, b)
	assert.Equal(t, 1, bud.SettledAttempts)
	assert.Zero(t, bud.ReservedAttempts)
	assert.Equal(t, captaincode.StopAttemptsExhausted, bud.StopReason)
}
