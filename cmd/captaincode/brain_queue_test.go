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

// A /repeat whose reply ended in an APIError on 27 September looked
// unanswered in the transcript the fork replayed, and ran again first in
// the queue when a new prompt was typed four days later (2026-10-01). A
// prompt the brain already ran is history - across a restart too - while
// the newest prompt, and the same words typed again, always run.
func TestQueueSkipsAPromptAlreadyRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	var runs []string
	worker := func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		runs = append(runs, lastUserTurn(prompt))
		mu.Unlock()
		return leg, captaincode.Result{Text: "answer to " + lastUserTurn(prompt), DurationMs: 5}, nil
	}
	send := func(b *brain, msgs []map[string]string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"model": "cursor", "stream": false, "messages": msgs})
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
		require.Equal(t, 200, rec.Code, rec.Body.String())
	}
	history := []map[string]string{
		{"role": "user", "content": "/cursor where are we at"},
		{"role": "assistant", "content": "here"},
	}
	old := map[string]string{"role": "user", "content": "/cursor address the old plan"}

	b := teamBrain()
	b.runWorkerFn = worker
	send(b, append(append([]map[string]string{}, history...), old)) // its reply never reaches the transcript

	b2 := teamBrain() // a restarted brain
	b2.runWorkerFn = worker
	send(b2, append(append([]map[string]string{}, history...), old,
		map[string]string{"role": "user", "content": "/cursor a note"},
		map[string]string{"role": "user", "content": "/cursor what's left to build"}))
	mu.Lock()
	assert.Equal(t, []string{"address the old plan", "a note", "what's left to build"}, runs, "the stale prompt is not run again")
	runs = nil
	mu.Unlock()

	// The same words typed again, after an answer, are a new prompt.
	send(b2, append(append([]map[string]string{}, history...), old,
		map[string]string{"role": "assistant", "content": "done"},
		old,
		map[string]string{"role": "user", "content": "/cursor then this"}))
	mu.Lock()
	assert.Equal(t, []string{"address the old plan", "then this"}, runs)
	mu.Unlock()
}
