package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Routing's own calls - tier-1 classification and the director's plan - run
// BEFORE the worker, in a different request from the one that records it.
// They must land on the same task as the worker they routed, or the baseline
// report omits what routing itself costs (ROADMAP M1.2).

func TestRouteCallsAndTheWorkerShareOneTask(t *testing.T) {
	b := teamBrain()
	task := "audit the concurrency in the ledger"

	b.chargeRoute(task)(captaincode.LegFree, "classify", captaincode.Result{Tokens: 400, DurationMs: 4200}, nil)
	b.chargeRoute(task)(captaincode.LegGrok, "director", captaincode.Result{Tokens: 3000, DurationMs: 17500}, nil)
	taskID, _ := b.chargeTurn("", captaincode.LegClaude, task, captaincode.CallUsage(captaincode.LegClaude, 9000, 0.31, nil), 60000, false, "")

	labels := map[string]bool{}
	for _, c := range captaincode.ChargeTree(b.ledger.Charges, taskID) {
		if c.Kind == captaincode.KindCall {
			labels[c.Label] = true
		}
	}
	assert.Equal(t, map[string]bool{"classify": true, "director": true, "worker": true}, labels,
		"the turn's bill is classification + plan + worker, under one task")

	tasks := 0
	for _, c := range b.ledger.Charges {
		if c.Kind == captaincode.KindTask {
			tasks++
		}
	}
	assert.Equal(t, 1, tasks, "routing and recording must not mint two tasks for one turn")
	assert.Equal(t, 3, b.ledger.TaskTotals(taskID).Calls)
}

// A turn the deterministic triage fast-paths asks no model at all. Minting is
// lazy precisely so it leaves no empty task row for a report to count.
func TestRouteMintsNoTaskWhenItAsksNoModel(t *testing.T) {
	b := teamBrain()
	taskID, _ := b.chargeTurn("", captaincode.LegFree, "fix typo in README", captaincode.CallUsage(captaincode.LegFree, 20, 0, nil), 900, false, "")
	require.NotEmpty(t, taskID)
	assert.Equal(t, 1, b.ledger.TaskTotals(taskID).Calls, "only the worker call")
}

// The identity is consumed, not read: the same prompt text sent again is a
// new task and must not be charged onto the first one.
func TestRouteIdentityIsConsumedByTheTurnItRouted(t *testing.T) {
	b := teamBrain()
	task := "review this"

	b.chargeRoute(task)(captaincode.LegGrok, "director", captaincode.Result{Tokens: 2000}, nil)
	first, _ := b.chargeTurn("", captaincode.LegClaude, task, captaincode.Usage{}, 1000, false, "")
	second, _ := b.chargeTurn("", captaincode.LegClaude, task, captaincode.Usage{}, 1000, false, "")

	assert.NotEqual(t, first, second, "a second turn of the same text is a second task")
	assert.Equal(t, 2, b.ledger.TaskTotals(first).Calls, "the director call belongs to the turn it routed")
	assert.Equal(t, 1, b.ledger.TaskTotals(second).Calls)
}

// A stale identity is never adopted: the wrapper call follows its route
// within seconds, so anything older is a different turn.
func TestStaleRouteIdentityIsNotAdopted(t *testing.T) {
	b := teamBrain()
	task := "something asked long ago"
	b.chargeRoute(task)(captaincode.LegGrok, "director", captaincode.Result{Tokens: 2000}, nil)

	b.rtmu.Lock()
	b.routeTurns[truncate(task, 120)] = routeTurn{id: b.routeTurns[truncate(task, 120)].id, at: time.Now().Add(-2 * routeTurnTTL)}
	b.rtmu.Unlock()

	taskID, _ := b.chargeTurn("", captaincode.LegClaude, task, captaincode.Usage{}, 1000, false, "")
	assert.Equal(t, 1, b.ledger.TaskTotals(taskID).Calls, "a fresh turn does not inherit an hour-old plan")
}

// A director call that FAILED still spent quota; charging only successes
// understates routing's cost.
func TestFailedRouteCallIsStillCharged(t *testing.T) {
	b := teamBrain()
	task := "plan this"
	b.chargeRoute(task)(captaincode.LegGrok, "director", captaincode.Result{Tokens: 1200, DurationMs: 120000}, assert.AnError)

	taskID, _ := b.chargeTurn("", captaincode.LegClaude, task, captaincode.Usage{}, 1000, false, "")
	assert.Equal(t, 2, b.ledger.TaskTotals(taskID).Calls, "a timed-out plan is a charge, not a silence")
}

// ── fan-out: one turn, one task (ROADMAP M1.2) ──
//
// A team turn answers one prompt with several workers. Before the stage row
// existed each worker minted its own task, and the bill for one accepted task
// read as three unrelated tasks - the one number M1 exists to produce.

func TestTeamTurnIsOneTaskWithOneStage(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if leg == captaincode.LegGLM {
			return leg, captaincode.Result{}, errors.New("rate limited")
		}
		return leg, captaincode.Result{Text: "output of " + string(leg), Tokens: 1000, DurationMs: 500}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "SYNTHESIZED"}, nil
	}
	// Routing already paid for the plan that chose this team: the fan-out must
	// adopt that identity rather than leave the director's call orphaned.
	b.mu.Lock()
	b.chargeRoute("do A and B")(captaincode.LegGrok, "director", captaincode.Result{Tokens: 2000, DurationMs: 9000}, nil)
	b.mu.Unlock()
	b.storeTeamPlan("do A and B", captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "split",
		Workers: []captaincode.Worker{
			{Leg: captaincode.LegCodex, Brief: "part A"},
			{Leg: captaincode.LegGLM, Brief: "part B"},
		}})

	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "do A and B"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	tasks, stages := chargesOfKind(b, captaincode.KindTask), chargesOfKind(b, captaincode.KindStage)
	require.Len(t, tasks, 1, "two workers, one task")
	require.Len(t, stages, 1, "the fan-out is one stage")
	assert.Equal(t, "team:codex+glm", stages[0].Label)
	assert.Equal(t, tasks[0].ID, stages[0].Parent)

	taskID := tasks[0].ID
	assert.Equal(t, 3, b.ledger.TaskTotals(taskID).Calls,
		"the director's plan plus BOTH workers - the failed one spent quota too")
	for _, c := range b.ledger.Charges {
		if c.Kind == captaincode.KindAttempt && c.Label == "worker" {
			assert.Equal(t, stages[0].ID, c.Parent, "a team worker hangs off the stage, not the task")
		}
	}
	for _, ev := range b.ledger.Events {
		if ev.Leg != "" {
			assert.Equal(t, taskID, ev.TaskID, "every worker event points at the shared task")
			assert.NotEmpty(t, ev.AttemptID)
		}
	}
}

// A workflow is one task with one stage row per stage: a three-stage pipeline
// with a parallel terminal stage is four attempts, not four tasks.
func TestWorkflowTurnIsOneTaskWithAStagePerStage(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "output of " + string(leg), Tokens: 500, DurationMs: 100}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "REVIEWED"}, nil
	}

	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false,
		"/grok analyse the queue > /cursor review the analysis > /codex red-team it + /claude red-team it"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	tasks, stages := chargesOfKind(b, captaincode.KindTask), chargesOfKind(b, captaincode.KindStage)
	require.Len(t, tasks, 1, "one workflow turn is one task")
	require.Len(t, stages, 3, "one stage row per stage")
	for i, s := range stages {
		assert.Equal(t, tasks[0].ID, s.Parent)
		assert.Equal(t, fmt.Sprintf("stage %d/3", i+1), s.Label)
	}
	assert.Equal(t, 4, b.ledger.TaskTotals(tasks[0].ID).Calls, "4 worker runs, one task")
	assert.Len(t, captaincode.ChargeTree(b.ledger.Charges, tasks[0].ID), 12,
		"the tree is 1 task + 3 stages + 4 attempts + 4 calls")
}

func chargesOfKind(b *brain, kind captaincode.ChargeKind) []captaincode.Charge {
	var out []captaincode.Charge
	for _, c := range b.ledger.Charges {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// Compaction and the /repeat round digest are provider calls the TURN pays
// for. They run before (or after) the worker, from code that never sees the
// routing hook, and used to cost the user quota that appeared nowhere.

func TestOverheadCallsShareTheTurnsTask(t *testing.T) {
	b := teamBrain()
	task := "rewrite the installer section"

	b.chargeOverhead(task)(captaincode.LegGemini, "compaction", captaincode.Result{Tokens: 12000, DurationMs: 6400}, nil)
	taskID, _ := b.chargeTurn("", captaincode.LegClaude, task, captaincode.CallUsage(captaincode.LegClaude, 9000, 0.31, nil), 60000, false, "")

	labels := map[string]bool{}
	for _, c := range captaincode.ChargeTree(b.ledger.Charges, taskID) {
		if c.Kind == captaincode.KindCall {
			labels[c.Label] = true
		}
	}
	assert.Equal(t, map[string]bool{"compaction": true, "worker": true}, labels,
		"compaction belongs to the turn whose conversation it folded")

	tasks := 0
	for _, c := range b.ledger.Charges {
		if c.Kind == captaincode.KindTask {
			tasks++
		}
	}
	assert.Equal(t, 1, tasks, "overhead must not mint a second task for the same turn")
}

// A failed compaction is still charged: the fold spent quota and then fell
// back to windowing, which is exactly the overhead the M1 report must see.
func TestFailedCompactionIsStillCharged(t *testing.T) {
	b := teamBrain()
	b.chargeOverhead("long session")(captaincode.LegGemini, "compaction",
		captaincode.Result{Tokens: 8000, DurationMs: 90000}, errors.New("timeout"))
	taskID, _ := b.chargeTurn("", captaincode.LegClaude, "long session", captaincode.CallUsage(captaincode.LegClaude, 100, 0, nil), 1000, false, "")
	assert.Equal(t, 2, b.ledger.TaskTotals(taskID).Calls)
}

// Background memory consolidation belongs to no user turn, so it gets a task
// of its own rather than being charged to whatever turn happened to trigger
// it - or, as before, to nothing at all.
func TestMemoryConsolidationIsItsOwnTask(t *testing.T) {
	b := teamBrain()
	b.chargeOwnTask("memory: distill me@captaincode")(captaincode.LegGemini, "distill",
		captaincode.Result{Tokens: 5000, DurationMs: 12000}, nil)

	var task captaincode.Charge
	for _, c := range b.ledger.Charges {
		if c.Kind == captaincode.KindTask {
			task = c
		}
	}
	require.NotEmpty(t, task.ID, "distillation records a task row")
	assert.Equal(t, "memory: distill me@captaincode", task.Label)
	assert.Equal(t, 1, b.ledger.TaskTotals(task.ID).Calls)
}

// A brain with no ledger (the CLI's throwaway used only to pick a leg) must
// not be asked to charge anything.
func TestChargeHooksAreNilWithoutALedger(t *testing.T) {
	b := &brain{}
	assert.Nil(t, b.chargeOverhead("t"))
	assert.Nil(t, b.chargeOwnTask("t"))
}
