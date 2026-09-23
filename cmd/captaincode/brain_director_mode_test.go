package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captainReq(text string) *http.Request {
	body, _ := json.Marshal(map[string]any{"model": "auto", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": text}}})
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
}

// /captain <word> is the helm from the prompt line: a judge leg pins, a mode
// word picks by policy, and nothing here reaches a worker (2026-09-16).
func TestCaptainWordSwitchesTheDirector(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no real run history in the usage window
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Errorf("a worker ran for a control word: %s", prompt)
		return leg, captaincode.Result{}, nil
	}
	defer captaincode.SetDirector(captaincode.LegGrok)

	rec := httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain claude"))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "Director → claude")
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector())
	assert.Equal(t, "fixed:claude", b.ledger.DirectorMode, "persisted")

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain quality"))
	assert.Contains(t, rec.Body.String(), "tier 2")
	assert.Equal(t, captaincode.LegCodex, b.effectiveDirector(), "the best of tier 2 on the compiled ranking: codex directs as gpt-6-sol, read off 5.6 Sol (47), below the band under Opus 5.5, above grok-4.7 (46)")
	assert.Equal(t, "quality", b.ledger.DirectorMode)

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain frontier"))
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector())

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain auto"))
	assert.Contains(t, rec.Body.String(), "least used")
	assert.True(t, captaincode.DirectorCapable(b.effectiveDirector()))

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain codex-cli"))
	assert.Contains(t, rec.Body.String(), "cannot direct", "an agent CLI is not a judge")

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain director"))
	assert.Contains(t, rec.Body.String(), "Director: **")
	assert.Contains(t, rec.Body.String(), "| judge |")

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain reset"))
	assert.Equal(t, "", b.ledger.DirectorMode)
}

// A restarted brain comes back with the same helm.
func TestDirectorModeSurvivesARestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPTAIN_DIRECTOR", "")
	defer captaincode.SetDirector(captaincode.LegGrok)
	b := teamBrain()
	_, _, err := b.switchDirector("quality")
	require.NoError(t, err)
	saved := b.ledger.DirectorMode

	b2 := teamBrain()
	b2.ledger.DirectorMode = saved
	b2.restoreDirectorMode()
	assert.Equal(t, captaincode.LegCodex, b2.effectiveDirector(), "the best of tier 2 on the compiled ranking")
	assert.Equal(t, "quality", b2.directorModeName())

	// CAPTAIN_DIRECTOR=auto seeds the mode when nothing is persisted.
	t.Setenv("CAPTAIN_DIRECTOR", "auto")
	b3 := teamBrain()
	b3.restoreDirectorMode()
	assert.Equal(t, "auto", b3.directorModeName())
}
