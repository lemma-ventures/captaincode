package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rate-limit error must demote the director IMMEDIATELY: the director's
// window is closed, so waiting for directorFallbackAfter (3) consecutive
// failures wastes full timeout cycles on a leg that cannot answer.
func TestDirectorRateLimit_DemotesImmediately(t *testing.T) {
	ladder := []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok}
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}, dirLadder: ladder}
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector(), "healthy: first choice directs")

	rl := captaincode.NewRateLimitError(captaincode.LegClaude, "You've hit your session limit", time.Now().Add(2*time.Hour))
	b.noteDirectorOutcome(rl)

	assert.Equal(t, captaincode.LegCodex, b.effectiveDirector(), "rate-limit → immediate demotion to second choice")
	assert.True(t, time.Now().Before(b.dirFallbackUntil), "fallback window is open")
}

// A non-rate-limit error still needs directorFallbackAfter consecutive
// failures before demoting.
func TestDirectorRateLimit_GenericErrorStillNeedsStreak(t *testing.T) {
	ladder := []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok}
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}, dirLadder: ladder}

	b.noteDirectorOutcome(errors.New("json parse failed"))
	b.noteDirectorOutcome(errors.New("network hiccup"))
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector(), "two generic failures: not yet demoted")
}

// A rate-limit error with a reset time should set the fallback window to
// match the provider's reset, not the flat default.
func TestDirectorRateLimit_UsesProviderResetTime(t *testing.T) {
	ladder := []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok}
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}, dirLadder: ladder}

	reset := time.Now().Add(3 * time.Hour)
	rl := captaincode.NewRateLimitError(captaincode.LegClaude, "resets at 5:00am", reset)
	b.noteDirectorOutcome(rl)

	remaining := time.Until(b.dirFallbackUntil)
	assert.Greater(t, remaining, 2*time.Hour, "fallback window should track the provider's reset time")
	assert.Less(t, remaining, 4*time.Hour)
}

// setDirector at runtime rebuilds the ladder and resets fallback state.
func TestSetDirector_RebuildsLadderAndResetsFallback(t *testing.T) {
	b := &brain{
		ledger:           &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirLadder:        []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok},
		dirIdx:           1,
		dirFailStreak:    2,
		dirFallbackUntil: time.Now().Add(10 * time.Minute),
	}
	b.setDirector(captaincode.LegGrok)

	assert.Equal(t, captaincode.LegGrok, b.ladder()[0], "new director is first on the ladder")
	assert.Equal(t, 0, b.dirIdx, "rung reset to 0")
	assert.Equal(t, 0, b.dirFailStreak, "fail streak reset")
	assert.True(t, b.dirFallbackUntil.IsZero(), "fallback window cleared")
	assert.Equal(t, captaincode.LegGrok, captaincode.Director, "global Director updated")
}

func TestDirectorLadderFromEnvWith_PrimaryFirst(t *testing.T) {
	got := directorLadderFromEnvWith(captaincode.LegGLM)
	assert.Equal(t, captaincode.LegGLM, got[0], "explicit primary is first")
	assert.Contains(t, got, captaincode.LegCodex, "codex is a fallback")
	assert.Contains(t, got, captaincode.LegGrok, "grok is a fallback")
}

// HTTP GET /v1/director returns the current director state.
func TestDirectorHTTP_Get(t *testing.T) {
	b := &brain{
		ledger:    &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirLadder: []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok},
	}
	rec := httptest.NewRecorder()
	b.directorHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/director", nil))
	assert.Equal(t, 200, rec.Code)
	var out struct {
		Director string   `json:"director"`
		Ladder   []string `json:"ladder"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Equal(t, "claude", out.Director)
	assert.Equal(t, []string{"claude", "codex", "grok"}, out.Ladder)
}

// HTTP POST /v1/director switches the director at runtime.
func TestDirectorHTTP_Post(t *testing.T) {
	b := &brain{
		ledger:    &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirLadder: []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok},
	}
	rec := httptest.NewRecorder()
	b.directorHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/director", bytes.NewReader([]byte(`{"director":"grok"}`))))
	assert.Equal(t, 200, rec.Code)
	var out struct {
		Director string   `json:"director"`
		Ladder   []string `json:"ladder"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Equal(t, "grok", out.Director)
	assert.Equal(t, "grok", out.Ladder[0])
}

// HTTP POST /v1/director rejects an unknown leg.
func TestDirectorHTTP_PostUnknownLeg(t *testing.T) {
	b := &brain{
		ledger:    &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirLadder: []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok},
	}
	rec := httptest.NewRecorder()
	b.directorHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/director", bytes.NewReader([]byte(`{"director":"nope"}`))))
	assert.Equal(t, 400, rec.Code)
}

// effectiveDirector skips legs that are cooling in the ledger, even when no
// director fallback window is open. This is the "piggyback to next best"
// behaviour: if Claude is rate-limited by any call path, the director uses the
// next leg in the ladder automatically.
func TestEffectiveDirector_SkipsLedgerCooldown(t *testing.T) {
	ladder := []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok}
	b := &brain{
		ledger:    &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirLadder: ladder,
	}
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector(), "healthy: claude directs")

	b.ledger.Cooldown(captaincode.LegClaude, 2*time.Hour)
	assert.Equal(t, captaincode.LegCodex, b.effectiveDirector(), "claude cooling → codex directs")
}

// After the fallback window expires, effectiveDirector still skips the leg if
// its ledger cooldown is still active (set by a different call path — e.g. a
// worker rate-limit that benched the leg).
func TestEffectiveDirector_StillSkipsAfterFallbackExpires(t *testing.T) {
	ladder := []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok}
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}, dirLadder: ladder}

	b.ledger.Cooldown(captaincode.LegClaude, 3*time.Hour)
	b.dirFallbackUntil = time.Now().Add(-time.Second)
	assert.Equal(t, captaincode.LegCodex, b.effectiveDirector(),
		"fallback window expired but ledger cooldown still active → codex directs")
}

// HTTP POST /v1/director with "reset" resets the director to the env default.
func TestDirectorHTTP_PostReset(t *testing.T) {
	t.Setenv("CAPTAIN_DIRECTOR", "grok")
	b := &brain{
		ledger:           &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirLadder:        []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok},
		dirIdx:           1,
		dirFallbackUntil: time.Now().Add(10 * time.Minute),
	}
	rec := httptest.NewRecorder()
	b.directorHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/director", bytes.NewReader([]byte(`{"director":"reset"}`))))
	assert.Equal(t, 200, rec.Code)
	var out struct {
		Director string   `json:"director"`
		Ladder   []string `json:"ladder"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Equal(t, "grok", out.Director, "reset to env default (grok)")
	assert.Equal(t, 0, b.dirIdx, "rung reset to 0")
	assert.True(t, b.dirFallbackUntil.IsZero(), "fallback window cleared")
}

// resetDirector restores the env/default director and clears fallback state.
func TestResetDirector(t *testing.T) {
	b := &brain{
		ledger:           &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirLadder:        []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok},
		dirIdx:           1,
		dirFailStreak:    3,
		dirFallbackUntil: time.Now().Add(10 * time.Minute),
	}
	b.resetDirector()
	assert.Equal(t, 0, b.dirIdx, "rung reset to 0")
	assert.Equal(t, 0, b.dirFailStreak, "fail streak reset")
	assert.True(t, b.dirFallbackUntil.IsZero(), "fallback window cleared")
}

// ── availability (live 2026-09-16: a machine without grok held it as helm) ──
//
// The compiled default director is grok; a fresh machine with no xAI login
// listed grok as director, failed to reach it on every plan, and fell back to
// the heuristic ladder. A director that cannot run must never hold the helm.

// firstRunnableDirector walks the preference ladder and returns the first leg
// that can actually run here, not the compiled default.
func TestFirstRunnableDirector_SkipsUnconfigured(t *testing.T) {
	t.Setenv("CAPTAIN_DIRECTOR", "")
	b := &brain{
		ledger:       &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirAvailable: map[captaincode.Leg]bool{captaincode.LegClaude: true},
	}
	assert.Equal(t, captaincode.LegClaude, b.firstRunnableDirector(),
		"grok is the compiled default but not runnable; claude is")
}

// effectiveDirector skips a leg that is unavailable here exactly like one that
// is cooling: the helm moves to the next rung that can run.
func TestEffectiveDirector_SkipsUnavailable(t *testing.T) {
	b := &brain{
		ledger:       &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirLadder:    []captaincode.Leg{captaincode.LegGrok, captaincode.LegCodex, captaincode.LegClaude},
		dirAvailable: map[captaincode.Leg]bool{captaincode.LegClaude: true},
	}
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector(),
		"grok and codex are unavailable → claude directs")
}

// A brain that starts with no persisted helm picks the first runnable
// preference, so health and the worker ladder name the leg that will run
// instead of the compiled default the machine cannot reach.
func TestStartupDirector_AvoidsUnconfiguredDefault(t *testing.T) {
	t.Setenv("CAPTAIN_DIRECTOR", "")
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := &brain{
		ledger:       &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirAvailable: map[captaincode.Leg]bool{captaincode.LegClaude: true},
	}
	b.setDirector(b.firstRunnableDirector())

	assert.Equal(t, captaincode.LegClaude, captaincode.Director, "health and /v1/director report the runnable leg")
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector())
	assert.NotContains(t, captaincode.Rungs, captaincode.LegClaude, "the director is never a worker")
}

// A policy mode (frontier) ranks by quality and does not know what is
// installed; when its pick cannot run, the next runnable leg is named instead.
func TestResolveDirectorMode_ReplacesUnavailablePick(t *testing.T) {
	t.Setenv("CAPTAIN_DIRECTOR", "")
	b := &brain{
		ledger:       &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		dirMode:      captaincode.DirectorFrontier,
		dirAvailable: map[captaincode.Leg]bool{captaincode.LegGLM: true},
	}
	pick, ok := b.resolveDirectorMode(captaincode.DirectorFrontier)
	require.True(t, ok)
	assert.Equal(t, captaincode.LegGLM, pick.Leg, "the frontier pick is unavailable → the runnable leg is named")
	assert.Contains(t, pick.Reason, "not runnable here")
}
