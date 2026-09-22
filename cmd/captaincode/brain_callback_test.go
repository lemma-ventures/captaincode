package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A worker is one turn: work it backgrounds cannot report by itself, so the
// prompt names the callback that can (live 2026-09-22: a frontier turn armed
// a test gate and a k24 pair, promised to report when they landed, and the
// user waited for a report no worker could ever send).
func TestWorkerPromptArmsTheCallbackInsteadOfPromisingToReport(t *testing.T) {
	ws := captaincode.Workspace{Dir: "/Users/rpellerin/Gits/arc"}
	c := callbackContract(ws, captaincode.LegClaude)
	assert.Contains(t, c, "captain send --cwd /Users/rpellerin/Gits/arc --from claude")
	assert.Contains(t, c, "Never end a turn promising to report later")

	t.Setenv("CAPTAIN_WORKER_CALLBACK", "0")
	assert.Empty(t, callbackContract(ws, captaincode.LegClaude))
}

// …on every path that dispatches a worker: solo, team and workflow.
func TestEveryWorkerPathCarriesTheCallback(t *testing.T) {
	dir := "/Users/rpellerin/Gits/arc"
	ws := captaincode.Workspace{Dir: dir}
	b := teamBrain()
	assert.Contains(t, b.teamWorkerPrompt(ws, "[user]\nship it", "review the patch", captaincode.LegGrok),
		"captain send --cwd "+dir+" --from grok")
	assert.Contains(t, b.workflowStagePrompt(ws, "[user]\nship it", 1, 2, nil, "review the patch", captaincode.LegCodexCLI),
		"captain send --cwd "+dir+" --from codex-cli")

	var mu sync.Mutex
	var seen string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		seen = prompt
		mu.Unlock()
		return leg, captaincode.Result{Text: "done", DurationMs: 5}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "grok", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/grok run the long benchmark"}}})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set(workspaceHeader, dir)
	b.chatCompletions(rec, r)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, seen, "captain send --cwd "+dir+" --from grok")
	assert.True(t, strings.Index(seen, "[captain] Work that outlives this turn") > strings.Index(seen, "run the long benchmark"),
		"the contract trails the task")
}

// A session title gets no contract at all - it is six words of housekeeping.
func TestTitlePromptHasNoCallback(t *testing.T) {
	b := teamBrain()
	var seen string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		seen = prompt
		return leg, captaincode.Result{Text: "a title", DurationMs: 1}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "auto", "stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": captaincode.TitleMarker},
			{"role": "user", "content": "name this conversation"},
		}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.NotContains(t, seen, "captain send")
}
