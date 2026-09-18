package captaincode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── priors sync: online benchmarks refresh the COLD-START priors ──
//
// Design boundary (2026-07-19): benchmarks rank base models, not legs-in-
// harness, so synced numbers only replace the hand-written priors that anchor
// routing before local scored runs accumulate - never the live scorecards.

func aaFixture() []AAModel {
	return []AAModel{
		{Name: "Claude Fable", Slug: "claude-fable", CodingIndex: 70},
		{Name: "GPT-5.5", Slug: "gpt-5-5", CodingIndex: 48},
		{Name: "GPT-5.5 Pro", Slug: "gpt-5-5-pro", CodingIndex: 60},
		{Name: "GLM-5.3", Slug: "glm-5-3", CodingIndex: 56},
		{Name: "MiniMax M3", Slug: "minimax-m3", CodingIndex: 52},
		{Name: "DeepSeek V4 Flash", Slug: "deepseek-v4-flash", CodingIndex: 38},
	}
}

func TestMatchAA_PrefersExactWorkerVariant(t *testing.T) {
	m, ok := MatchAA(aaFixture(), LegCodex)
	require.True(t, ok)
	assert.Equal(t, "gpt-5-5", m.Slug,
		"the codex LEG runs gpt-5.5-fast, scored as gpt-5.5 - matching the Pro variant would overstate it by 12 index points")
}

func TestMatchAA_NoMatchMeansSkip(t *testing.T) {
	_, ok := MatchAA(aaFixture(), LegCursor) // Composer not in fixture
	assert.False(t, ok, "unmatched legs keep their hand-written prior")
	_, ok = MatchAA(aaFixture(), LegGrok) // grok-build not in fixture
	assert.False(t, ok)
}

func TestProposePriors_AnchoredOnClaude(t *testing.T) {
	got, err := ProposePriors(aaFixture())
	require.NoError(t, err)
	assert.InDelta(t, 9.5, got[LegClaude], 0.01, "claude anchors the scale at its hand-written 9.5")
	assert.InDelta(t, 48.0/70.0*9.5, got[LegCodex], 0.05)
	assert.InDelta(t, 56.0/70.0*9.5, got[LegGLM], 0.05)
	_, hasCursor := got[LegCursor]
	assert.False(t, hasCursor, "no benchmark match → no proposal")
}

func TestProposePriors_NoAnchorIsAnError(t *testing.T) {
	models := []AAModel{{Name: "GLM-5.3", Slug: "glm-5-3", CodingIndex: 56}}
	_, err := ProposePriors(models)
	assert.Error(t, err, "without the claude anchor the scale is meaningless")
}

func TestPriorOverridesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "priors.json")
	require.NoError(t, SavePriorOverrides(path, map[Leg]float64{LegGLM: 8.1, LegFree: 5.2}))

	orig := QualityPrior(LegGLM)
	t.Cleanup(func() { resetPriors() })
	n, err := LoadPriorOverridesFrom(path)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.InDelta(t, 8.1, QualityPrior(LegGLM), 0.01)
	assert.InDelta(t, 5.2, QualityPrior(LegFree), 0.01)
	assert.NotEqual(t, orig, QualityPrior(LegGLM))
	assert.InDelta(t, 9.5, QualityPrior(LegClaude), 0.01, "legs absent from the override file keep compiled priors")

	resetPriors()
	assert.InDelta(t, orig, QualityPrior(LegGLM), 0.01)
}

func TestLoadPriorOverridesMissingFileIsFine(t *testing.T) {
	n, err := LoadPriorOverridesFrom(filepath.Join(t.TempDir(), "nope.json"))
	assert.NoError(t, err, "no override file is the normal state, not an error")
	assert.Zero(t, n)
	_ = os.Unsetenv("unused")
}
