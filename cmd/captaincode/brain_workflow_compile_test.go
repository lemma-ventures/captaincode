package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The skill: the user describes the workflow in English, the director compiles
// it, the plan is PREVIEWED, and only a confirmation runs it (spec §4.4).

func TestWorkflowControlWords(t *testing.T) {
	intent, ok := workflowIntent("/wf grok analyses this then cursor reviews it")
	assert.True(t, ok)
	assert.Equal(t, "grok analyses this then cursor reviews it", intent)

	_, ok = workflowIntent("/workflow do the thing")
	assert.True(t, ok)
	_, ok = workflowIntent("just a normal prompt")
	assert.False(t, ok)
	_, ok = workflowIntent("/wf")
	assert.False(t, ok, "the bare word is not an intent")

	id, ok := workflowRunID("/run wf_7fa2")
	assert.True(t, ok)
	assert.Equal(t, "wf_7fa2", id)
	_, ok = workflowRunID("/run the tests")
	assert.False(t, ok, "only wf_ ids are workflow runs")
}

func compileBrain(t *testing.T, expr string) *brain {
	t.Helper()
	b := teamBrain()
	b.compileFn = func(intent, convo string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats) (captaincode.CompiledWorkflow, error) {
		wf, err := captaincode.ParseWorkflow(expr)
		require.NoError(t, err)
		return captaincode.CompiledWorkflow{Expression: wf.String(), Workflow: wf,
			Rationale: "user named the legs", Warnings: []string{"glm benched 6m"}}, nil
	}
	return b
}

func TestCompilePreviewsAndDoesNotExecute(t *testing.T) {
	b := compileBrain(t, "/grok analyse @queue.ts > /cursor review it > /codex red-team it + /claude red-team it")
	ran := false
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = true
		return leg, captaincode.Result{Text: "x"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/wf grok analyses the queue file, cursor reviews, codex and claude red-team"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	preview := answerOf(t, rec)
	assert.False(t, ran, "compiling must never execute anything")
	assert.Contains(t, preview, "3 stages")
	assert.Contains(t, preview, "4 runs + review")
	assert.Contains(t, preview, "1 output")
	assert.Contains(t, preview, "parallel")
	assert.Contains(t, preview, "why: user named the legs")
	assert.Contains(t, preview, "⚠ glm benched 6m")
	assert.Contains(t, preview, "/run wf_", "the run line is the confirmation")
	assert.Contains(t, preview, "/grok analyse @queue.ts > /cursor review it",
		"the raw expression is always printed so a plan can be edited, not re-explained")
}

func TestConfirmationRunsTheCompiledWorkflow(t *testing.T) {
	b := compileBrain(t, "/free draft it > /grok tighten it")
	var legs []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		legs = append(legs, string(leg))
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 5}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "FINAL"}, nil
	}

	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/wf draft with the cheap model then have grok tighten it"))
	require.Equal(t, 200, rec.Code)
	preview := answerOf(t, rec)
	i := strings.Index(preview, "/run wf_")
	require.Greater(t, i, 0)
	id := strings.Fields(preview[i+len("/run "):])[0]

	rec2 := httptest.NewRecorder()
	b.chatCompletions(rec2, wfReq(false, "/run "+id))
	require.Equal(t, 200, rec2.Code, rec2.Body.String())
	assert.Equal(t, []string{"free", "grok"}, legs)
	assert.Contains(t, answerOf(t, rec2), "FINAL")

	// Take-once: the same id cannot be replayed.
	rec3 := httptest.NewRecorder()
	b.chatCompletions(rec3, wfReq(false, "/run "+id))
	require.Equal(t, 200, rec3.Code)
	assert.Contains(t, answerOf(t, rec3), "unknown or expired")
}

func TestCompileFailureExplainsTheManualPath(t *testing.T) {
	b := teamBrain()
	b.compileFn = func(intent, convo string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats) (captaincode.CompiledWorkflow, error) {
		return captaincode.CompiledWorkflow{}, assertErr("director unavailable")
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/wf do something clever"))
	require.Equal(t, 200, rec.Code)
	out := answerOf(t, rec)
	assert.Contains(t, out, "could not compile")
	assert.Contains(t, out, "/grok analyse", "offer the syntax so the user is not stuck")
}

// Routing must not pay for a director plan just to be intercepted later.
func TestWorkflowControlSkipsTheDirector(t *testing.T) {
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		t.Fatal("the director must not be called for a workflow control word")
		return captaincode.Plan{}, nil
	}
	for _, task := range []string{"/wf grok then cursor", "/run wf_abc123"} {
		rec := httptest.NewRecorder()
		b.route(rec, jsonReq("/v1/route", map[string]any{"task": task}))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "workflow")
	}
}

func jsonReq(path string, body map[string]any) *http.Request {
	raw, _ := json.Marshal(body)
	return httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

// R3 (PRIME_AGENT_NOTES): recurring topologies become named artifacts - the
// dogfood data shows the same review workflow retyped for days.
func TestSavedWorkflows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := teamBrain()
	var ran []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = append(ran, string(leg))
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "SAVED-AGG"}, nil
	}

	// Save with an explicit expression.
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/wf save paper-review /grok analyse the paper > /claude audit it"))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, answerOf(t, rec), "saved", rec.Body.String())
	assert.Empty(t, ran, "saving must not execute")

	// List shows it.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/wf list"))
	assert.Contains(t, answerOf(t, rec), "paper-review")
	assert.Contains(t, answerOf(t, rec), "grok>claude")

	// Run executes it with the CURRENT conversation as context.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/wf run paper-review"))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"grok", "claude"}, ran)
	assert.Contains(t, answerOf(t, rec), "SAVED-AGG")

	// Unknown name says what exists.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/wf run nope"))
	assert.Contains(t, answerOf(t, rec), "paper-review", "the error lists what IS saved")

	// Bad expressions are rejected at save time, not run time.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/wf save broken /gpt5 do it > /claude review"))
	assert.Contains(t, answerOf(t, rec), "unknown leg")

	// Names are constrained.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/wf save Bad_Name! /grok a > /claude b"))
	assert.Contains(t, answerOf(t, rec), "name")
}

// Saved workflows keep their gates.
func TestSavedWorkflowKeepsGates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveNamedWorkflow("gated", "/codex fix it gate: true > /claude review")
	wf, ok := loadNamedWorkflow("gated")
	require.True(t, ok)
	assert.Equal(t, "true", wf.Stages[0].Legs[0].Gate)
}
