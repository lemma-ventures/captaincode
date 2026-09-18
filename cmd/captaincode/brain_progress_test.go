package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live 2026-07-29: a worker was demonstrably running and the TUI showed
// nothing at all - no tool activity, no elapsed time, no sign of life - so a
// healthy 4-minute run was indistinguishable from a wedged one. Progress now
// rides the OpenAI `reasoning_content` delta, which the fork's
// openai-compatible provider turns into a reasoning part (rendered as the
// thinking block) - visible, and structurally OUTSIDE the deliverable.

// sseDeltas splits an SSE body into the answer text and the reasoning text.
func sseDeltas(body string) (content, reasoning string) {
	var c, r strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		c.WriteString(chunk.Choices[0].Delta.Content)
		r.WriteString(chunk.Choices[0].Delta.ReasoningContent)
	}
	return c.String(), r.String()
}

func streamReq(model string) *http.Request {
	body, _ := json.Marshal(oaiChatReq{Model: model, Stream: true,
		Messages: []oaiMessage{{Role: "user", Content: json.RawMessage(`"do the thing"`)}}})
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
}

func fastProgress(t *testing.T) {
	t.Helper()
	oldFirst, oldEvery := progressFirstDelay, progressEvery
	progressFirstDelay, progressEvery = 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { progressFirstDelay, progressEvery = oldFirst, oldEvery })
}

func TestWorkerToolActivityStreamsAsReasoning(t *testing.T) {
	fastProgress(t)
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		onStatus("⚙ Read pkg/x.go")
		onStatus("⚙ Bash go test ./...")
		onDelta("the answer")
		return leg, captaincode.Result{Text: "the answer", Streamed: true}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, streamReq("claude"))

	content, reasoning := sseDeltas(rec.Body.String())
	assert.Equal(t, "the answer", content, "progress must never contaminate the deliverable")
	assert.Contains(t, reasoning, "Read pkg/x.go")
	assert.Contains(t, reasoning, "go test ./...")
}

func TestQuietWorkerStillShowsItIsAlive(t *testing.T) {
	fastProgress(t)
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		time.Sleep(150 * time.Millisecond) // thinking: no deltas, no tools
		onDelta("done")
		return leg, captaincode.Result{Text: "done", Streamed: true}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, streamReq("claude"))

	content, reasoning := sseDeltas(rec.Body.String())
	assert.Equal(t, "done", content)
	assert.Contains(t, reasoning, "claude", "the heartbeat names the leg that is working")
	assert.Contains(t, reasoning, "working")
	assert.GreaterOrEqual(t, strings.Count(reasoning, "working"), 2, "the heartbeat repeats while the run is quiet")
}

// Progress writes the SSE header, after which a failure can only be surfaced
// inline. Worker auth/quota/outage failures land in the first seconds (claude
// "Not logged in" in 0.7s, 2026-07-29), so nothing may be emitted before
// progressFirstDelay - the fork must still see a real 429/503/400.
func TestEarlyWorkerErrorKeepsItsHTTPStatus(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{}, captaincode.ErrRateLimited
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, streamReq("claude"))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotContains(t, rec.Body.String(), "reasoning_content")
}

func TestCompletionWriterStatusIsReasoningOnly(t *testing.T) {
	rec := httptest.NewRecorder()
	emit, status, finish := newCompletionWriter(rec, oaiChatReq{Stream: true}, "team")
	status("⚙ grok drafting")
	emit("result")
	finish()
	content, reasoning := sseDeltas(rec.Body.String())
	assert.Equal(t, "result", content)
	assert.Contains(t, reasoning, "grok drafting")
}

func TestCompletionWriterBuffersOnlyTheAnswerWhenNotStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	emit, status, finish := newCompletionWriter(rec, oaiChatReq{Stream: false}, "team")
	status("⚙ noise")
	emit("result")
	finish()
	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Choices, 1)
	assert.Equal(t, "result", got.Choices[0].Message.Content)
}

// The brain SIGSEGV'd and took every in-flight run with it: an error path
// returned without calling finish(), so the keepalive goroutine kept writing to
// a ResponseWriter whose handler had returned - which panics inside net/http
// (live 2026-07-31, twice: brain_team.go:322 and :398).
func TestWriterTeardownStopsTheKeepaliveAndSilencesLateWrites(t *testing.T) {
	old := sseKeepaliveEvery
	sseKeepaliveEvery = 10 * time.Millisecond
	defer func() { sseKeepaliveEvery = old }()

	rec := httptest.NewRecorder()
	emit, status, finish := newCompletionWriter(rec, oaiChatReq{Stream: true}, "workflow")
	emit("partial")
	finish()
	after := rec.Body.Len()

	// Whatever happens next must never touch the writer again.
	time.Sleep(60 * time.Millisecond) // several keepalive ticks
	emit("late text")
	status("late status")
	finish() // idempotent: handlers both defer it and call it explicitly
	assert.Equal(t, after, rec.Body.Len(), "nothing may be written after teardown")
	assert.Equal(t, 1, strings.Count(rec.Body.String(), "[DONE]"), "exactly one terminator")
	assert.NotContains(t, rec.Body.String(), "late text")
}

func TestBufferedWriterFinishIsIdempotent(t *testing.T) {
	rec := httptest.NewRecorder()
	emit, _, finish := newCompletionWriter(rec, oaiChatReq{Stream: false}, "workflow")
	emit("answer")
	finish()
	finish() // a second call must not write a second JSON body
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
}
