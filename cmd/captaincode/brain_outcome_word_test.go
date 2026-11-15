package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func outcomeWordBrain(t *testing.T) *brain {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "worker output"}, nil
	}
	return b
}

func outcomeWordChat(t *testing.T, b *brain, dir, turn, task string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "claude", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": task}}})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set(workspaceHeader, dir)
	if turn != "" {
		r.Header.Set(turnHeader, turn)
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, r)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec
}

// TestChatCompletionSendsTaskID: every chat completion that creates a task
// carries the task id, and the session's turn is bound to that task so its
// outcome words act on it (Q13).
func TestChatCompletionSendsTaskID(t *testing.T) {
	b := outcomeWordBrain(t)
	rec := outcomeWordChat(t, b, t.TempDir(), "turn_outcome_task1", "/claude fix the parser")
	taskID := rec.Header().Get("X-Captain-Task-ID")
	require.NotEmpty(t, taskID, "the completion must name the task it started")
	got, ok := b.turnTask("turn_outcome_task1")
	require.True(t, ok)
	assert.Equal(t, taskID, got)
	// A different session has no task yet.
	_, ok = b.turnTask("turn_outcome_other1")
	assert.False(t, ok)
}

// TestOutcomePostFromPluginShape: the plugin's out-of-band request records
// the verdict, and its errors say exactly what is wrong.
func TestOutcomePostFromPluginShape(t *testing.T) {
	b := outcomeWordBrain(t)
	rec := outcomeWordChat(t, b, t.TempDir(), "turn_outcome_task2", "/claude fix the parser")
	taskID := rec.Header().Get("X-Captain-Task-ID")
	require.NotEmpty(t, taskID)

	post := func(turn, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/outcome/word?turn="+url.QueryEscape(turn), strings.NewReader(body))
		b.outcomeWordHTTP(w, r)
		return w
	}

	// An accept from the plugin's request changes the task's outcome to
	// accepted, reviewed by the TUI's OS user.
	w := post("turn_outcome_task2", `{"word":"accept","text":"patch landed cleanly"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Result string `json:"result"`
		Task   string `json:"task"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, taskID, out.Task)
	assert.Contains(t, out.Result, "accepted")
	o := b.ledger.OutcomeFor(taskID)
	require.NotNil(t, o)
	assert.Equal(t, captaincode.AcceptanceAccepted, o.Status)
	require.NotNil(t, o.Review)
	assert.Equal(t, captaincode.ReviewAccept, o.Review.Verdict)
	assert.Equal(t, tuiReviewer(), o.Review.Reviewer)
	assert.Equal(t, "patch landed cleanly", o.Review.Note)

	// The shapes the plugin must not send.
	for _, tc := range []struct {
		name, turn, body string
		status           int
	}{
		{"no task", "turn_outcome_none1", `{"word":"accept"}`, 404},
		{"unknown word", "turn_outcome_task2", `{"word":"undo"}`, 400},
		{"reject without reason", "turn_outcome_task2", `{"word":"reject"}`, 400},
		{"correction without minutes", "turn_outcome_task2", `{"word":"correction","text":"slow"}`, 400},
		{"correction bad minutes", "turn_outcome_task2", `{"word":"correction","text":"x 10"}`, 400},
		{"regressed without reason", "turn_outcome_task2", `{"word":"regressed"}`, 400},
		{"unknown field", "turn_outcome_task2", `{"word":"accept","extra":1}`, 400},
		{"trailing object", "turn_outcome_task2", `{"word":"accept"}{}`, 400},
		{"bad turn", "short", `{"word":"accept"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := post(tc.turn, tc.body)
			assert.Equal(t, tc.status, w.Code, w.Body.String())
		})
	}

	// A correction lands on the same task.
	w = post("turn_outcome_task2", `{"word":"correction","text":"12 hand-fixed the tests"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	o = b.ledger.OutcomeFor(taskID)
	require.NotNil(t, o)
	require.Len(t, o.Corrections, 1)
	assert.Equal(t, 12, o.Corrections[0].Minutes)
	assert.Equal(t, "hand-fixed the tests", o.Corrections[0].Reason)
}

// TestOutcomeLastServesTheSidebar: the sidebar reads its session's last task
// and outcome status; a session with no task gets a 404 it can ignore.
func TestOutcomeLastServesTheSidebar(t *testing.T) {
	b := outcomeWordBrain(t)
	rec := outcomeWordChat(t, b, t.TempDir(), "turn_outcome_task3", "/claude fix the parser")
	taskID := rec.Header().Get("X-Captain-Task-ID")
	require.NotEmpty(t, taskID)

	get := func(turn string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		b.outcomeLastHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/outcome/last?turn="+url.QueryEscape(turn), nil))
		return w
	}

	w := get("turn_outcome_task3")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Task   string `json:"task"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, taskID, out.Task)
	assert.Equal(t, "pending", out.Status)

	// After a verdict the status follows.
	w2 := httptest.NewRecorder()
	b.outcomeWordHTTP(w2, httptest.NewRequest(http.MethodPost, "/v1/outcome/word?turn=turn_outcome_task3", strings.NewReader(`{"word":"reject","text":"wrong file"}`)))
	require.Equal(t, http.StatusOK, w2.Code, w2.Body.String())

	w = get("turn_outcome_task3")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, "rejected", out.Status)

	assert.Equal(t, http.StatusNotFound, get("turn_outcome_none2").Code)
	assert.Equal(t, http.StatusBadRequest, get("short").Code)
	assert.Equal(t, http.StatusMethodNotAllowed, func() int {
		w := httptest.NewRecorder()
		b.outcomeLastHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/outcome/last?turn=turn_outcome_task3", nil))
		return w.Code
	}())
}

// TestOutcomeWordAsTurnIsAnsweredNotRun: a copy of an outcome word that
// reaches the brain as a turn is answered in one line and never dispatched.
func TestOutcomeWordAsTurnIsAnsweredNotRun(t *testing.T) {
	b := outcomeWordBrain(t)
	dir := t.TempDir()
	rec := outcomeWordChat(t, b, dir, "turn_outcome_task4", "/claude fix the parser")
	taskID := rec.Header().Get("X-Captain-Task-ID")
	require.NotEmpty(t, taskID)

	b.runWorkerFn = func(leg captaincode.Leg, prompt string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Error("an outcome word must never dispatch a worker")
		return leg, captaincode.Result{}, nil
	}
	rec = outcomeWordChat(t, b, dir, "turn_outcome_task4", "/accept exactly what I wanted")
	assert.Contains(t, rec.Body.String(), "accept recorded")
	assert.Contains(t, rec.Body.String(), "accepted")
	o := b.ledger.OutcomeFor(taskID)
	require.NotNil(t, o)
	assert.Equal(t, captaincode.AcceptanceAccepted, o.Status)

	// No task in the session: the word says so instead of running.
	rec = outcomeWordChat(t, b, dir, "turn_outcome_task5", "/reject not what I asked for")
	assert.Contains(t, rec.Body.String(), "no task started in this session")
}
