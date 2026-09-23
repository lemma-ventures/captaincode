package captaincode

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTierOfFollowsEffort(t *testing.T) {
	assert.Equal(t, TierCheap, TierOf(EffortLow))
	assert.Equal(t, TierFrontier, TierOf(EffortMax))
	for _, e := range []Effort{"", EffortMedium, EffortHigh, EffortXHigh} {
		assert.Equal(t, TierQuality, TierOf(e), "effort %q runs the leg's own model", e)
	}
}

func TestEveryLegResolvesThreeTiers(t *testing.T) {
	// The operator's own pins (CAPTAIN_GLM_MODEL, …) are read at init; the
	// table is the compiled one.
	rebuild := func() { resetRegistry(); applyRegistry() }
	t.Cleanup(rebuild)
	for _, s := range Registry() {
		t.Setenv(s.EnvPrefix()+"_MODEL", "")
		t.Setenv(s.EnvPrefix()+"_PROVIDER", "")
	}
	rebuild()
	cases := []struct {
		leg                      Leg
		cheap, quality, frontier string
	}{
		{LegClaude, "claude-sonnet", "claude-opus", "claude-opus-frontier"},
		{LegCodexCLI, "gpt-6-sol", "gpt-6-astra", "gpt-6-astra"},
		{LegCodex, "gpt-6-luna", "gpt-6-sol-fast", "gpt-6-astra"},
		{LegLuna, "gpt-6-luna", "gpt-6-luna", "gpt-6-luna"},
		{LegCursor, "composer-2.5", "grok-4.7-medium", "grok-4.7-xhigh"},
		{LegGrok, "grok-build-0.1", "grok-build-0.1", "grok-4.7"},
		{LegGrokMax, "grok-4.7", "grok-4.7", "grok-4.7"},
		{LegGemini, "google/gemini-3.5-flash-lite", "google/gemini-3.7-flash", "google/gemini-3.8-flash"},
		{LegDeepSeek, "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"},
		{LegGLM, "z-ai/glm-5.3-flash", "z-ai/glm-5.3", "z-ai/glm-5.3"},
		{LegQwen, "qwen/qwen3.6-35b-a3b", "qwen/qwen3.5-397b-a17b", "qwen/qwen3.5-397b-a17b"},
		{LegKimi, "moonshotai/kimi-k3", "moonshotai/kimi-k3", "moonshotai/kimi-k3"},
	}
	for _, c := range cases {
		got := TierModels(c.leg)
		assert.Equal(t, c.cheap, got[TierCheap], "%s cheap", c.leg)
		assert.Equal(t, c.quality, got[TierQuality], "%s quality", c.leg)
		assert.Equal(t, c.frontier, got[TierFrontier], "%s frontier", c.leg)
	}
}

func TestTierEnvPinsAndCheapSwitch(t *testing.T) {
	t.Setenv("CAPTAIN_CODEX_CHEAP_MODEL", "gpt-5.6-luna-fast")
	mm, _ := modelForEffort(LegCodex, false, EffortLow)
	assert.Equal(t, "gpt-5.6-luna-fast", mm.Model, "<PREFIX>_CHEAP_MODEL pins the band")
	t.Setenv("CAPTAIN_CLAUDE_FRONTIER_MODEL", "fable")
	assert.Equal(t, "fable", FrontierModel())
	t.Setenv("CAPTAIN_FRONTIER_MODEL", "claude-opus-5-5")
	assert.Equal(t, "claude-opus-5-5", FrontierModel(), "the legacy name still wins")

	t.Setenv("CAPTAIN_CHEAP_TIER", "0")
	mm, _ = modelForEffort(LegCodex, false, EffortLow)
	assert.Equal(t, "gpt-6-sol-fast", mm.Model, "CAPTAIN_CHEAP_TIER=0 keeps the leg's own model at low")
	assert.Equal(t, "grok-4.7-low", cursorModel(EffortLow), "cursor falls back to the family's low rung")
	assert.Empty(t, claudeModel(EffortLow))
}

func TestClaudeAndCodexCLIArgsCarryTheTier(t *testing.T) {
	args := codexCLICmdArgs("", "x", EffortLow)
	assert.Equal(t, "gpt-6-sol", args[slices.Index(args, "-m")+1])
	args = codexCLICmdArgs("", "x", EffortHigh)
	assert.Equal(t, "gpt-6-astra", args[slices.Index(args, "-m")+1])
	assert.Equal(t, "sonnet", claudeModel(EffortLow))
	assert.Empty(t, claudeModel(EffortHigh), "quality leaves Claude Code's own default")
}

func TestRegistryOverlayMergesTiers(t *testing.T) {
	base := LegSpec{ID: "x", Tiers: map[Tier]string{TierCheap: "a", TierFrontier: "b"}}
	got := mergeSpec(base, LegSpec{Tiers: map[Tier]string{TierFrontier: "c"}})
	assert.Equal(t, map[Tier]string{TierCheap: "a", TierFrontier: "c"}, got.Tiers)
	assert.Equal(t, "b", base.Tiers[TierFrontier], "the compiled spec is not mutated")
}
