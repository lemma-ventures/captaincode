package main

// Partial salvage on the SOLO path (2026-08-24). Team and workflow runs treat
// "timed out WITH text" as a degraded success, but a solo run discarded it -
// live: a 15-minute claude codebase review died at the cap and every minute of
// its output was thrown away (r0824-2121d5). A capped solo run with real text
// must deliver that text, clearly marked partial; a capped run with NO text
// stays a real error.

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func timeoutErr() error {
	return fmt.Errorf("claude -p did not finish within 15m0s: %w", captaincode.ErrWorkerTimeout)
}

func TestSoloTimeoutWithTextIsSalvaged(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "REVIEW SO FAR: the auth seam leaks tokens", DurationMs: 900_000}, timeoutErr()
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "review the codebase, improve it"))
	require.Equal(t, 200, rec.Code, "timeout WITH text is a degraded success, not an error: %s", rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "REVIEW SO FAR: the auth seam leaks tokens")
	assert.Contains(t, body, "partial", "the user must know it was cut short")
	assert.NotContains(t, body, `"error"`)
}

func TestSoloTimeoutWithTextIsSalvaged_Streamed(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if onDelta != nil {
			onDelta("REVIEW SO FAR: the auth seam leaks tokens")
		}
		return leg, captaincode.Result{Text: "REVIEW SO FAR: the auth seam leaks tokens", Streamed: true, DurationMs: 900_000}, timeoutErr()
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(true, "review the codebase, improve it"))
	require.Equal(t, 200, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "REVIEW SO FAR")
	assert.Contains(t, body, "partial", "already-streamed text gets a trailing partial marker")
	assert.Contains(t, body, "[DONE]", "stream must close cleanly")
	assert.NotContains(t, body, "error -", "no inline error on a salvaged stream")
}

func TestSoloTimeoutWithNoTextStaysAnError(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{}, timeoutErr()
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "review the codebase"))
	assert.NotEqual(t, 200, rec.Code, "no text to salvage → real error")
	assert.True(t, strings.Contains(rec.Body.String(), "did not finish"))
}

// The fork replays the whole conversation each turn; a long session's request
// body legitimately exceeds 1MB (live 2026-08-27: every request 400'd). The
// brain must accept multi-MB bodies - windowing happens after parse.
func TestLargeConversationBodyIsAccepted(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	big := strings.Repeat("conversation history chunk. ", 80_000) // ~2.2MB
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, big, "noted", "quick follow-up"))
	require.Equal(t, 200, rec.Code, rec.Body.String()[:200])
}
