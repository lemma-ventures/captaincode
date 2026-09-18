package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captain/auto exists so the terminal never has to decide a leg BEFORE the
// user's message renders. A plugin (or any OpenAI-compatible client) sends
// model=auto; the brain triages, asks the director only when triage cannot
// decide, and dispatches - all inside the turn, where progress already streams.
// Without it, a router that thinks freezes the UI for its whole decision
// (measured p90 25s on our own traffic).

func autoReq(task string) *http.Request {
	body, _ := json.Marshal(map[string]any{
		"model": "auto", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": task}},
	})
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
}

func TestAutoIsAdvertisedAsAModel(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.models(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assert.Contains(t, rec.Body.String(), `"auto"`, "clients validate model ids before sending")
}

func TestAutoRoutesAndRunsWithoutTheCallerChoosingALeg(t *testing.T) {
	b := teamBrain()
	var ran captaincode.Leg
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = leg
		return leg, captaincode.Result{Text: "done", Tokens: 5, DurationMs: 10}, nil
	}
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 8, Verdict: "good"}, nil
	}

	rec := httptest.NewRecorder()
	b.chatCompletions(rec, autoReq("fix the typo in the README title"))

	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "done", "the answer comes back on the same turn")
	assert.NotEmpty(t, ran, "a leg actually ran")
	assert.NotEqual(t, captaincode.Leg("auto"), ran, "auto is a pseudo-model, never a worker")
}

func TestAutoHonorsATeamDecision(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0") // force the director path this fixture exercises
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "splits cleanly",
			Workers: []captaincode.Worker{
				{Leg: captaincode.LegCodex, Brief: "part A"},
				{Leg: captaincode.LegGLM, Brief: "part B"},
			}}, nil
	}
	// The team's workers run in parallel, so the count must be atomic: a plain
	// ++ loses one of the two increments often enough to fail this test three
	// runs in five.
	var workers atomic.Int64
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		workers.Add(1)
		return leg, captaincode.Result{Text: "output of " + string(leg), Tokens: 10, DurationMs: 10}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "SYNTHESIZED"}, nil
	}

	rec := httptest.NewRecorder()
	b.chatCompletions(rec, autoReq("rewrite the storage layer and migrate every caller"))

	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "SYNTHESIZED", "a team decision runs the team, not one worker")
	assert.Equal(t, int64(2), workers.Load(), "both planned workers ran")
}

func TestAutoNeverRunsForATitleRequest(t *testing.T) {
	b := teamBrain()
	var ran captaincode.Leg
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = leg
		return leg, captaincode.Result{Text: "A Title", Tokens: 2, DurationMs: 5}, nil
	}
	body, _ := json.Marshal(map[string]any{
		"model": "auto", "stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a title generator. Name this conversation."},
			{"role": "user", "content": "fix the typo"},
		},
	})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))

	require.Equal(t, 200, rec.Code, rec.Body.String())
	// Naming a session is a one-line job: it must never pay for routing.
	assert.Equal(t, b.titleLeg(), ran, "a title request goes straight to the cheapest leg")
}
