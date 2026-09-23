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
		{Name: "Claude Opus 5.5", Slug: "claude-opus-5-5", CodingIndex: 70},
		{Name: "GPT-5.6 Sol", Slug: "gpt-5-6-sol", CodingIndex: 48},
		{Name: "GPT-5.6 Sol (xhigh)", Slug: "gpt-5-6-sol-xhigh", CodingIndex: 60},
		{Name: "GPT-5.6 Luna", Slug: "gpt-5-6-luna", CodingIndex: 40},
		{Name: "GLM-5.3", Slug: "glm-5-3", CodingIndex: 56},
		{Name: "MiniMax M3", Slug: "minimax-m3", CodingIndex: 52},
		{Name: "DeepSeek V4 Flash", Slug: "deepseek-v4-flash", CodingIndex: 38},
	}
}

func TestMatchAA_PrefersExactWorkerVariant(t *testing.T) {
	m, ok := MatchAA(aaFixture(), LegCodex)
	require.True(t, ok)
	assert.Equal(t, "gpt-5-6-sol", m.Slug,
		"the codex LEG runs gpt-6-sol-fast, read off 5.6 Sol's plain row until AA scores GPT-6 Sol - matching an effort variant would overstate it by 12 index points")
	m, ok = MatchAA(aaFixture(), LegLuna)
	require.True(t, ok)
	assert.Equal(t, "gpt-5-6-luna", m.Slug, "luna reads 5.6 Luna the same way")
}

// The day AA scores GPT-6 Sol, codex reads that row and stops reading 5.6.
func TestMatchAA_NewSolRowWinsOverThePredecessor(t *testing.T) {
	models := append(aaFixture(), AAModel{Name: "GPT-6 Sol", Slug: "gpt-6-sol", CodingIndex: 58})
	m, ok := MatchAA(models, LegCodex)
	require.True(t, ok)
	assert.Equal(t, "gpt-6-sol", m.Slug)
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

// The day a model ships, the feed lists it with the coding index pending
// (Opus 5.5 and Grok 4.7, 2026-09-22). The ranking reads that row - it has an
// intelligence index - but the priors, scaled from the coding index, must
// pass it over for the newest family member that has one, or a sync that day
// divides the anchor by zero.
func TestPriorsSkipARowListedWithoutACodingIndex(t *testing.T) {
	models := []AAModel{
		{Name: "Claude Opus 5.5", Slug: "claude-opus-5-5", IntelligenceIndex: 57.6},
		{Name: "Claude Opus 5", Slug: "claude-opus-5", IntelligenceIndex: 50.8, CodingIndex: 78},
		{Name: "Grok 4.7", Slug: "grok-4-7", IntelligenceIndex: 46.4},
		{Name: "Grok 4.6", Slug: "grok-4-6", IntelligenceIndex: 44.3, CodingIndex: 76.8},
		{Name: "GLM-5.3", Slug: "glm-5-3", IntelligenceIndex: 44.8, CodingIndex: 74.8},
	}
	m, ok := MatchAA(models, LegClaude)
	require.True(t, ok)
	assert.Equal(t, "claude-opus-5-5", m.Slug, "the ranking reads the newest row")
	m, ok = MatchAACoding(models, LegClaude)
	require.True(t, ok)
	assert.Equal(t, "claude-opus-5", m.Slug, "the priors read the newest scored row")
	m, ok = MatchAACoding(models, LegGrokMax)
	require.True(t, ok)
	assert.Equal(t, "grok-4-6", m.Slug)

	p, err := ProposePriors(models)
	require.NoError(t, err)
	assert.Equal(t, 9.5, p[LegClaude], "the anchor")
	assert.Equal(t, 9.4, p[LegGrokMax], "76.8/78 of the anchor, from the 4.6 row")
	dp, err := ProposeDomainPriors(models)
	require.NoError(t, err)
	assert.Equal(t, 9.5, dp[LegClaude]["code"])

	_, err = ProposePriors(models[:1])
	assert.Error(t, err, "an anchor with no coding index is an error, not a division by zero")
}
