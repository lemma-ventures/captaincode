package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The compiled snapshot ranks the roster the same way everywhere: the
// frontier section, the /frontier chain, the upgrade flags.

func TestPerfFamilyStripsVersionsAndEffort(t *testing.T) {
	assert.Equal(t, "claude-fable", perfFamily("claude-fable-5-1"))
	assert.Equal(t, "claude-fable", perfFamily("claude-fable-5-1-xhigh"))
	assert.Equal(t, "glm", perfFamily("glm-5-3"))
	assert.Equal(t, "glm-flash", perfFamily("glm-5-3-flash"), "flash is its own family")
	assert.Equal(t, "grok-build", perfFamily("grok-build-0-1-06-16"))
	assert.NotEqual(t, perfFamily("grok-4-6"), perfFamily("grok-build-0-1-06-16"))
}

func TestFrontierLegsAreRankedByPerf(t *testing.T) {
	fl := FrontierLegs()
	require.Equal(t, []Leg{LegClaude, LegCodexCLI, LegGrokMax}, fl, "fable, astra, grok-4.6 - the index order")
	chain := FrontierChain(LegFrontier)
	assert.Equal(t, fl, chain[:3], "the chain opens with the frontier legs")
	assert.NotContains(t, chain, LegFrontier)
	assert.Equal(t, LegGLM, chain[3], "then the next most capable model by index - the best open-weights one")
	assert.Equal(t, LegGPTOSS, chain[len(chain)-1], "gpt-oss-120b ranks under the free leg's nemotron on the compiled index")
	assert.NotContains(t, FrontierChain(LegClaude), LegClaude, "the failed leg is left out")
}

func TestUpgradeFlagsANewerFamilyMember(t *testing.T) {
	u, ok := UpgradeFor(LegGemini) // registry pins gemini-3-7-flash; the snapshot lists 3-8-flash above it
	require.True(t, ok)
	assert.Equal(t, "gemini-3-8-flash", u.Slug)
	_, ok = UpgradeFor(LegClaude)
	assert.False(t, ok, "fable 5.1 is the newest of its family")
	_, ok = UpgradeFor(LegGLM)
	assert.False(t, ok, "glm-5.3 is current; 5.3-flash is a different family and scores lower")
}
