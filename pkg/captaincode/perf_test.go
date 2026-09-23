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
	// The snapshot of 2026-09-22 lists Opus 5.5 (57.6) above astra-xhigh
	// (52.4) and Grok 4.7 (46.4); this order is the roster's frontier section
	// and the failover.
	require.Equal(t, []Leg{LegClaude, LegCodexCLI, LegGrokMax}, fl, "opus 5.5, astra, grok-4.7 - the index order")
	chain := FrontierChain(LegFrontier)
	assert.Equal(t, LegClaude, chain[0], "the tier closed, not the CLI: claude at standard settings leads whatever the index says")
	assert.ElementsMatch(t, fl, chain[:3], "the chain opens with the frontier legs")
	assert.NotContains(t, chain, LegFrontier)
	assert.Equal(t, LegCodexCLI, FrontierChain(LegClaude)[0], "claude itself failed: the next frontier leg by index")
	assert.Equal(t, LegCodex, chain[3], "then the next most capable model by index - GPT-6 Sol, on the 5.6 Sol row (47) until AA scores it")
	assert.Equal(t, LegGLM, chain[4], "then the best open-weights one")
	assert.Equal(t, LegGPTOSS, chain[len(chain)-1], "gpt-oss-120b ranks under the free leg's nemotron on the compiled index")
	assert.NotContains(t, FrontierChain(LegClaude), LegClaude, "the failed leg is left out")
}

func TestUpgradeFlagsANewerFamilyMember(t *testing.T) {
	u, ok := UpgradeFor(LegGemini) // registry pins gemini-3-7-flash; the snapshot lists 3-8-flash above it
	require.True(t, ok)
	assert.Equal(t, "gemini-3-8-flash", u.Slug)
	_, ok = UpgradeFor(LegClaude)
	assert.False(t, ok, "claude reads the Opus 5.5 row, the newest of its family")
	_, ok = UpgradeFor(LegGLM)
	assert.False(t, ok, "glm-5.3 is current; 5.3-flash is a different family and scores lower")
}

// ── what a refresh changed ──────────────────────────────────────────────────
//
// The feed listed Grok 4.7 on 2026-09-22, the day after its release, with the
// coding index still pending. The refresh must say so: the arrival, the
// grok-max row moving from 4.6 to 4.7, and nothing about legs that did not move.

func perfFeedBefore() []AAModel {
	return []AAModel{
		{Slug: "claude-opus-5", Name: "Claude Opus 5", Creator: "Anthropic", Released: "2026-07-24", IntelligenceIndex: 50.8, CodingIndex: 78},
		{Slug: "gpt-6-astra-xhigh", Name: "GPT-6 Astra (xhigh)", Creator: "OpenAI", Released: "2026-09-03", IntelligenceIndex: 52.4, CodingIndex: 75.9},
		{Slug: "grok-4-6", Name: "Grok 4.6", Creator: "SpaceXAI", Released: "2026-08-12", IntelligenceIndex: 44.3, CodingIndex: 76.8},
		{Slug: "grok-4-6-xhigh", Name: "Grok 4.6 (xhigh)", Creator: "SpaceXAI", Released: "2026-08-12", IntelligenceIndex: 44.2, CodingIndex: 75.9},
		{Slug: "gemini-3-7-flash", Name: "Gemini 3.7 Flash", Creator: "Google", Released: "2026-08-13", IntelligenceIndex: 39.1, CodingIndex: 76.1},
		{Slug: "glm-5-3", Name: "GLM-5.3", Creator: "Z AI", Released: "2026-08-18", IntelligenceIndex: 44.8, CodingIndex: 74.8},
	}
}

func TestPerfDiffNamesArrivalsMovesAndNewFlags(t *testing.T) {
	before := perfFeedBefore()
	after := append(perfFeedBefore(),
		AAModel{Slug: "grok-4-7", Name: "Grok 4.7", Creator: "SpaceXAI", Released: "2026-09-21", IntelligenceIndex: 46.4},
		AAModel{Slug: "grok-4-7-high", Name: "Grok 4.7 (high)", Creator: "SpaceXAI", Released: "2026-09-21", IntelligenceIndex: 46.3},
		AAModel{Slug: "claude-opus-5-5", Name: "Claude Opus 5.5", Creator: "Anthropic", Released: "2026-09-22", IntelligenceIndex: 57.6},
		AAModel{Slug: "gemini-3-8-flash", Name: "Gemini 3.8 Flash", Creator: "Google", Released: "2026-09-02", IntelligenceIndex: 40.9, CodingIndex: 76.3},
		AAModel{Slug: "step-5-preview", Name: "Step 5 (preview)", Creator: "StepFun", Released: "2026-09-20"}, // unscored: not an arrival
	)
	news := PerfDiff(before, after)
	require.False(t, news.Empty())

	arrived := []string{}
	for _, a := range news.Arrivals {
		arrived = append(arrived, a.Slug)
	}
	assert.Equal(t, []string{"claude-opus-5-5", "grok-4-7", "gemini-3-8-flash"}, arrived, "base variants only, best first; the effort variant and the unscored row are not news")

	moved := map[Leg]PerfMove{}
	for _, m := range news.Moved {
		moved[m.Leg] = m
	}
	require.Contains(t, moved, LegGrokMax, "grok-max's pin names grok-4-7: it reads that row the moment the feed lists it")
	assert.Equal(t, "grok-4-6", moved[LegGrokMax].From.Slug)
	assert.Equal(t, "grok-4-7", moved[LegGrokMax].To.Slug)
	require.Contains(t, moved, LegClaude, "claude's pin names Opus 5.5")
	assert.Equal(t, "claude-opus-5", moved[LegClaude].From.Slug)
	assert.Equal(t, "claude-opus-5-5", moved[LegClaude].To.Slug)
	assert.NotContains(t, moved, LegGemini, "gemini still reads 3-7-flash: a ⇡, not a move")
	assert.NotContains(t, moved, LegGLM)

	require.Len(t, news.Flagged, 1, "one leg newly flagged")
	assert.Equal(t, LegGemini, news.Flagged[0].Leg)
	assert.Equal(t, "gemini-3-8-flash", news.Flagged[0].Upgrade.Slug)
	assert.Equal(t, "gemini-3-7-flash", news.Flagged[0].Current.Slug)

	assert.True(t, PerfDiff(after, after).Empty(), "the same feed twice is no news")
	again := PerfDiff(after, append(after, AAModel{Slug: "grok-4-7-xhigh", Creator: "SpaceXAI", IntelligenceIndex: 46}))
	assert.True(t, again.Empty(), "an effort variant of a listed model is not an arrival, and gemini's ⇡ is not repeated")
}

func TestPerfFlagsListsEveryFlaggedLeg(t *testing.T) {
	feed := append(perfFeedBefore(), AAModel{Slug: "gemini-3-8-flash", Creator: "Google", Released: "2026-09-02", IntelligenceIndex: 40.9, CodingIndex: 76.3})
	flags := PerfFlags(feed)
	require.Len(t, flags, 1)
	assert.Equal(t, LegGemini, flags[0].Leg)
	assert.Equal(t, 40.9, flags[0].Upgrade.Perf)
	assert.Empty(t, PerfFlags(perfFeedBefore()), "nothing newer listed: no flags")
}

func TestSnapshotRanksTheNewestRowsTheDayTheyLand(t *testing.T) {
	// The compiled snapshot of 2026-09-22 lists Opus 5.5 and Grok 4.7 with
	// the coding index pending: they rank on intelligence, and the legs
	// pinned to them read those rows rather than the previous generation's.
	r, ok := PerfFor(LegClaude)
	require.True(t, ok)
	assert.Equal(t, "claude-opus-5-5", r.Slug)
	r, ok = PerfFor(LegGrokMax)
	require.True(t, ok)
	assert.Equal(t, "grok-4-7", r.Slug)
	assert.Zero(t, r.Coding, "listed before its coding evals landed")
}
