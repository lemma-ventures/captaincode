package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sidebar's activity feed showed every run as
//   ▶ cursor  [system] You are opencode, an interactive CLI tool that helps…
// because the "run" activity took the first line of the whole prompt, and the
// prompt starts with the client's system message. The row exists to say what
// the worker is doing; that is the last user turn.

func TestRunActivityNamesTheTaskNotTheSystemPrompt(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "fixed the multiples on slide 8, matching slide 10"}, nil
	}
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 8, Verdict: "good"}, nil
	}
	body, _ := json.Marshal(map[string]any{
		"model": "free", "stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": "You are opencode, an interactive CLI tool that helps users with software engineering tasks."},
			{"role": "user", "content": "slide 8 is not consistent with slide 10, fix it"},
		},
	})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	var run *activity
	for i := range b.acts {
		if b.acts[i].Kind == "run" {
			run = &b.acts[i]
			break
		}
	}
	require.NotNil(t, run, "a run activity is pushed")
	assert.Contains(t, run.Text, "slide 8 is not consistent", "the feed names the task")
	assert.NotContains(t, run.Text, "You are opencode", "…never the client's system prompt")
}
