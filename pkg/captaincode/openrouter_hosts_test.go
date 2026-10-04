package captaincode

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenRouterHostsRoutesEachBandOfALeg(t *testing.T) {
	t.Setenv("CAPTAIN_GLM_MODEL", "")
	t.Setenv("CAPTAIN_GLM_PROVIDER", "")
	t.Setenv("CAPTAIN_GLM_CHEAP_MODEL", "")
	t.Setenv("CAPTAIN_CHEAP_TIER", "")
	quality := map[string]any{"order": []any{"z-ai"}, "quantizations": []any{"fp8", "bf16"}}
	cheap := map[string]any{"sort": "price"}
	path := filepath.Join(t.TempDir(), "legs.json")
	require.NoError(t, SaveRegistryOverlay(path, registryOverlay{Legs: []LegSpec{
		{ID: LegGLM, Provider: "openrouter", Model: "z-ai/glm-5.3", Hosts: map[Tier]map[string]any{TierQuality: quality, TierCheap: cheap}},
		{ID: LegKimi, Provider: "openrouter", Model: "moonshotai/kimi-k3", Hosts: map[Tier]map[string]any{TierQuality: quality}},
	}}))
	_, err := LoadRegistry(path)
	t.Cleanup(func() { LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	require.NoError(t, err)

	assert.True(t, AnyOpenRouterHosts())
	assert.Equal(t, quality, OpenRouterHosts("z-ai/glm-5.3"))
	assert.Equal(t, cheap, OpenRouterHosts("Z-AI/GLM-5.3-Flash"), "the cheap band's own model, case-insensitive")
	assert.Equal(t, quality, OpenRouterHosts("moonshotai/kimi-k3"), "one model in every band: quality's hosts")
	assert.Nil(t, OpenRouterHosts("minimax/minimax-m3"), "a leg without hosts keeps OpenRouter's default")
	assert.Nil(t, OpenRouterHosts(""))
}

func TestOpenRouterHostsIgnoresLegsOffOpenRouter(t *testing.T) {
	t.Setenv("CAPTAIN_GLM_MODEL", "")
	t.Setenv("CAPTAIN_GLM_PROVIDER", "")
	path := filepath.Join(t.TempDir(), "legs.json")
	require.NoError(t, SaveRegistryOverlay(path, registryOverlay{Legs: []LegSpec{
		{ID: LegGLM, Hosts: map[Tier]map[string]any{TierQuality: {"order": []any{"z-ai"}}}},
	}}))
	_, err := LoadRegistry(path)
	t.Cleanup(func() { LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	require.NoError(t, err)
	assert.Nil(t, OpenRouterHosts("z-ai/glm-5.3"), "the compiled glm leg runs on NIM, where a provider block means nothing")
}
