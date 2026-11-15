package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parallelPost(b *brain, dir, text string) map[string]any {
	body, _ := json.Marshal(map[string]string{"text": text})
	rec := httptest.NewRecorder()
	b.parallelHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/parallel?cwd="+url.QueryEscape(dir), bytes.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// A /parallel typed while a turn runs starts at once, beside that turn, with
// the running turn's conversation - it no longer waits in the TUI's queue
// for the turn to end (live 2026-11-15). With no turn running it does
// nothing, so the message runs as an ordinary turn.
func TestParallelTypedDuringATurnStartsBesideIt(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	dir := t.TempDir()

	idle := parallelPost(b, dir, "research the pricing page")
	assert.Equal(t, false, idle["acted"], "nothing runs: the plugin lets the turn through")

	release := make(chan struct{})
	var mu sync.Mutex
	var prompts []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		first := len(prompts) == 1
		mu.Unlock()
		if first {
			<-release // the long turn the user is waiting on
		}
		return leg, captaincode.Result{Text: "done", DurationMs: 10}, nil
	}

	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		body, _ := json.Marshal(map[string]any{"model": "claude", "stream": false, "messages": []map[string]string{
			{"role": "user", "content": "what does the envelope sweep measure"},
			{"role": "assistant", "content": "it times every profile"},
			{"role": "user", "content": "implement the parser"},
		}})
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		r.Header.Set(workspaceHeader, dir)
		r.Header.Set(turnHeader, "ses_parallel_oob_0001")
		b.chatCompletions(httptest.NewRecorder(), r)
	}()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(prompts) == 1
	}, 5*time.Second, 10*time.Millisecond, "the first turn is running")

	started := parallelPost(b, dir, "research the pricing page")
	require.Equal(t, true, started["acted"], started)
	assert.Contains(t, started["result"], "parallel run pl_")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(prompts) == 2
	}, 5*time.Second, 10*time.Millisecond, "the parallel run reaches a worker while the first turn still runs")
	mu.Lock()
	second := prompts[1]
	mu.Unlock()
	assert.Contains(t, second, "research the pricing page")
	assert.Contains(t, second, "it times every profile", "the run replays the running turn's conversation")

	status := parallelPost(b, dir, "status")
	assert.Equal(t, true, status["acted"])
	assert.True(t, strings.Contains(status["result"].(string), "pl_"), status)

	close(release)
	<-turnDone
	assert.Equal(t, false, parallelPost(b, dir, "status")["acted"], "the turn ended: status runs as a turn again")
}
