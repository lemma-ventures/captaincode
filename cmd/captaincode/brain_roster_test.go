package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionLess(t *testing.T) {
	assert.True(t, versionLess("0.153.4", "0.154.0"))
	assert.True(t, versionLess("2.1.9", "2.1.10"))
	assert.False(t, versionLess("2.1.270", "2.1.270"))
	assert.False(t, versionLess("1.18.30", "1.18.3"))
}

func TestRosterRanksFrontierFirstAndFlagsUpgrades(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.rosterHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/roster", nil))
	require.Equal(t, 200, rec.Code)
	var out struct {
		PerfSource string `json:"perf_source"`
		Legs       []rosterLeg
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "snapshot", out.PerfSource, "no key in tests: the compiled snapshot")
	require.NotEmpty(t, out.Legs)
	assert.Equal(t, "claude", out.Legs[0].Leg, "ranked by perf: fable first")
	assert.True(t, out.Legs[0].Frontier)
	byLeg := map[string]rosterLeg{}
	for _, l := range out.Legs {
		byLeg[l.Leg] = l
		assert.NotEqual(t, "frontier", l.Leg, "the pseudo-leg is a mode, not a roster row")
	}
	assert.True(t, byLeg["grok-max"].Frontier)
	// model × route; effort is per request, never in the name (2026-09-13)
	assert.Equal(t, "codex-cli", byLeg["codex-cli"].Label, "the CLI leg")
	assert.Equal(t, "codex-openai", byLeg["codex"].Label, "the model pin through opencode's openai provider")
	assert.Equal(t, "claude-cli", byLeg["claude"].Label)
	assert.Equal(t, "cursor-cli", byLeg["cursor"].Label)
	assert.Equal(t, "grok-max-xai", byLeg["grok-max"].Label)
	assert.Equal(t, "glm-orouter", byLeg["glm"].Label)
	assert.Equal(t, "kimi-nim", byLeg["kimi"].Label)
	assert.Equal(t, "free-zen", byLeg["free"].Label)
	assert.False(t, byLeg["glm"].Frontier, "glm ranks near grok-max but stays in Models")
	assert.True(t, byLeg["glm"].OpenWeight)
	require.NotNil(t, byLeg["gemini"].Upgrade, "gemini-3-7-flash has 3-8-flash above it")
	assert.Equal(t, "gemini-3-8-flash", byLeg["gemini"].Upgrade.Slug)
}
