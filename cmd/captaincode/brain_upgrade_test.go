package main

import (
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"

	"github.com/stretchr/testify/assert"
)

func TestResolveProviderModelMatchesBenchmarkSlugs(t *testing.T) {
	cat := map[string][]string{
		"openrouter": {"google/gemini-3.7-flash", "google/gemini-3.8-flash", "google/gemini-3.8-flash-lite", "z-ai/glm-5.3", "z-ai/glm-5.3-flash", "moonshotai/kimi-k3"},
		"xai":        {"grok-4.6", "grok-4.5", "grok-build-0.1"},
	}
	id, ok := resolveProviderModel(cat, "openrouter", "gemini-3-8-flash")
	assert.True(t, ok)
	assert.Equal(t, "google/gemini-3.8-flash", id, "the base variant, not -lite")
	id, _ = resolveProviderModel(cat, "openrouter", "glm-5-3")
	assert.Equal(t, "z-ai/glm-5.3", id)
	id, _ = resolveProviderModel(cat, "xai", "grok-4-6")
	assert.Equal(t, "grok-4.6", id)
	_, ok = resolveProviderModel(cat, "openrouter", "muse-spark-1-3")
	assert.False(t, ok, "not served by this provider")
}

func TestCursorUsageLimitOnStderrIsARateLimit(t *testing.T) {
	assert.True(t, captaincode.CursorUsageLimit("ActionRequiredError: You've hit your usage limit Get Cursor Pro for more Agent usage"))
	assert.False(t, captaincode.CursorUsageLimit("Error: workspace not trusted"))
}
