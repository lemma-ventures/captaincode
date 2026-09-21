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

// Five prompts queued behind one turn arrive as one request with five
// trailing user messages (opencode 1.18). Each runs as its own turn, in
// order, on its own head, seeing the answers before it - not all at once as
// one worker's context, not only the last one (live 2026-09-21).
func TestQueuedPromptsRunOneAfterTheOther(t *testing.T) {
	b := teamBrain()
	var mu sync.Mutex
	var runs []string
	var prompts []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		runs = append(runs, string(leg)+": "+lastUserTurn(prompt))
		prompts = append(prompts, prompt)
		mu.Unlock()
		if onDelta != nil {
			onDelta("answer to " + lastUserTurn(prompt))
		}
		return leg, captaincode.Result{Text: "answer to " + lastUserTurn(prompt), DurationMs: 5}, nil
	}
	msgs := []map[string]string{
		{"role": "user", "content": "/cursor convert md to pdf"},
		{"role": "assistant", "content": "converted"},
		{"role": "user", "content": "/codex-cli remove the self-references"},
		{"role": "user", "content": "/codex-cli drop the author talk"},
		{"role": "user", "content": "/cursor generate the pdf for my review"},
		{"role": "user", "content": queueNudge},
	}
	// The plugin forces the model from the LAST prompt typed.
	body, _ := json.Marshal(map[string]any{"model": "cursor", "stream": true, "messages": msgs})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	text, errText := sseAnswer(rec.Body.String(), rec.Code)
	require.Empty(t, errText)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{
		"codex-cli: remove the self-references",
		"codex-cli: drop the author talk",
		"cursor: generate the pdf for my review",
	}, runs, "in order, each on its own head; the nudge is not a prompt")
	assert.Contains(t, prompts[1], "[assistant]\nanswer to remove the self-references", "the second sees the first's answer")
	assert.NotContains(t, prompts[0], "drop the author talk", "the first does not see what came after it")
	assert.Contains(t, text, "queued 1/3")
	assert.Contains(t, text, "queued 3/3")
	assert.Contains(t, text, "answer to generate the pdf for my review")
	assert.Less(t, strings.Index(text, "answer to remove"), strings.Index(text, "answer to drop"), "answers in order")
}

func TestASinglePendingPromptIsNotAQueue(t *testing.T) {
	msgs := []oaiMessage{
		{Role: "user", Content: jsonString("a")},
		{Role: "assistant", Content: jsonString("b")},
		{Role: "user", Content: jsonString("c")},
	}
	assert.Nil(t, queuedPrompts(msgs))
	msgs = append(msgs, oaiMessage{Role: "user", Content: jsonString(queueNudge)})
	assert.Nil(t, queuedPrompts(msgs), "the nudge behind one prompt is not a second prompt")
	msgs = append(msgs, oaiMessage{Role: "user", Content: jsonString("d")})
	assert.Equal(t, []int{2, 4}, queuedPrompts(msgs))
}
