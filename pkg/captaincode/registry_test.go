package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MM37 phase 1: legs are data. The compiled registry must reproduce exactly
// what the hand-written tables used to say, and an overlay must be able to
// add a model without a rebuild.

func TestRegistryDefaultsReproduceTheLadder(t *testing.T) {
	t.Setenv("CAPTAIN_GLM_MODEL", "")
	t.Setenv("CAPTAIN_GLM_PROVIDER", "")
	_, err := LoadRegistry(filepath.Join(t.TempDir(), "none.json"))
	require.NoError(t, err)
	assert.Equal(t, []Leg{LegJev, LegFree, LegQwen, LegStep, LegGPTOSS, LegGrok, LegLuna, LegDS4Flash, LegMiniMax, LegDeepSeek, LegGemini, LegKimi, LegCursor, LegGLM, LegCodex, LegGrokMax, LegCodexCLI, LegClaude}, AllLegs)
	assert.NotContains(t, Rungs, LegJev, "the decision leg is registered but never a worker rung")
	assert.Equal(t, "openrouter", legModels[LegGLM].Provider)
	assert.Equal(t, "z-ai/glm-5.3", legModels[LegGLM].Model)
	_, claudeInModels := legModels[LegClaude]
	assert.False(t, claudeInModels, "CLI legs have no opencode pin")
	assert.Equal(t, 9.5, QualityPrior(LegClaude))
	assert.True(t, LegSupportsVision(LegGemini))
	assert.False(t, LegSupportsVision(LegGLM))
	assert.True(t, IsFrontierClass(LegCodexCLI))
	assert.Equal(t, "Claude (captain · claude -p)", LegDisplayName(LegClaude))
	assert.Contains(t, legDescription(LegCursor), "Composer")
	s, ok := Spec(LegCodexCLI)
	require.True(t, ok)
	assert.Equal(t, TransportCodexCLI, s.Transport)
	assert.Equal(t, "CAPTAIN_CODEX_CLI", s.EnvPrefix())
}

func TestRegistryOverlayAddsALegWithoutARebuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legs.json")
	ov := registryOverlay{Legs: []LegSpec{{ID: "muse", Transport: TransportOpencode, Provider: "openrouter", Model: "meta/muse-spark-1.3",
		PriceIn: 1.25, PriceOut: 4.25, Ctx: 1048576, Prior: 7.9, Display: "Muse Spark 1.3 (captain · OpenRouter)", Note: "Meta; strong value on prose",
		DomainPrior: map[Domain]float64{DomainEditorial: 8.4}}}}
	require.NoError(t, SaveRegistryOverlay(path, ov))
	n, err := LoadRegistry(path)
	t.Cleanup(func() { LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.True(t, KnownLeg("muse"))
	assert.Contains(t, LegIDs(), "muse")
	assert.Equal(t, "openrouter", legModels["muse"].Provider)
	assert.Equal(t, 7.9, QualityPrior("muse"))
	assert.Equal(t, 8.4, QualityPriorFor("muse", DomainEditorial), "registry domain priors are honored")
	assert.Equal(t, 7.9, QualityPriorFor("muse", DomainCode), "unknown domain → overall prior")
	// Ladder position follows the prior: between glm/gemini (7.8) and kimi (8.0).
	pos := map[Leg]int{}
	for i, l := range AllLegs {
		pos[l] = i
	}
	assert.Greater(t, pos["muse"], pos[LegGemini])
	assert.Less(t, pos["muse"], pos[LegKimi])
	assert.Equal(t, "Muse Spark 1.3 (captain · OpenRouter)", LegDisplayName("muse"))
	assert.Contains(t, legDescription("muse"), "Meta")
	assert.Contains(t, Rungs, Leg("muse"), "a worker leg the director can assign")
	// Env override works for overlay legs too.
	t.Setenv("CAPTAIN_MUSE_MODEL", "meta/muse-spark-1.3-contributor")
	_, err = LoadRegistry(path)
	require.NoError(t, err)
	assert.Equal(t, "meta/muse-spark-1.3-contributor", legModels["muse"].Model)
}

func TestRegistryOverlayCanOverrideAndDisableCompiledLegs(t *testing.T) {
	t.Setenv("CAPTAIN_GLM_MODEL", "")
	t.Setenv("CAPTAIN_GLM_PROVIDER", "")
	path := filepath.Join(t.TempDir(), "legs.json")
	require.NoError(t, SaveRegistryOverlay(path, registryOverlay{
		Legs:    []LegSpec{{ID: LegGLM, Model: "z-ai/glm-5.3-flash", PriceIn: 0.15, PriceOut: 0.5}},
		Disable: []Leg{LegQwen},
	}))
	_, err := LoadRegistry(path)
	t.Cleanup(func() { LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	require.NoError(t, err)
	assert.Equal(t, "openrouter", legModels[LegGLM].Provider, "untouched fields keep their compiled value")
	assert.Equal(t, "z-ai/glm-5.3-flash", legModels[LegGLM].Model)
	assert.False(t, KnownLeg(LegQwen), "disabled legs leave the ladder")
	assert.NotContains(t, AllLegs, LegQwen)
	assert.NotContains(t, Rungs, LegQwen)
}

func TestRegistryRejectsBrokenEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legs.json")
	require.NoError(t, SaveRegistryOverlay(path, registryOverlay{Legs: []LegSpec{
		{ID: "team", Provider: "x", Model: "y"},          // reserved
		{ID: "Bad Name", Provider: "x", Model: "y"},      // not a word
		{ID: "nomodel", Transport: TransportOpencode},    // opencode needs provider/model
		{ID: "ok", Provider: "openrouter", Model: "a/b"}, // fine
	}}))
	_, err := LoadRegistry(path)
	t.Cleanup(func() { LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	require.Error(t, err, "broken entries are reported")
	assert.True(t, KnownLeg("ok"), "and the good one still loads")
	assert.False(t, KnownLeg("team"))
}

func TestAddAndRemoveLeg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legs.json")
	t.Cleanup(func() { LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	require.NoError(t, AddLeg(path, LegSpec{ID: "muse", Provider: "openrouter", Model: "meta/muse-spark-1.3", Prior: 7.9}))
	assert.True(t, KnownLeg("muse"))
	require.NoError(t, AddLeg(path, LegSpec{ID: "muse", Prior: 8.1}), "update merges")
	assert.Equal(t, 8.1, QualityPrior("muse"))
	assert.Equal(t, "meta/muse-spark-1.3", legModels["muse"].Model)
	require.NoError(t, RemoveLeg(path, "muse"))
	assert.False(t, KnownLeg("muse"))
	require.NoError(t, RemoveLeg(path, LegQwen), "compiled legs are disabled, not deleted")
	assert.False(t, KnownLeg(LegQwen))
	raw, _ := os.ReadFile(path)
	var ov registryOverlay
	require.NoError(t, json.Unmarshal(raw, &ov))
	assert.Contains(t, ov.Disable, LegQwen)
	require.NoError(t, AddLeg(path, LegSpec{ID: LegQwen, Prior: 6.9}), "re-adding clears the disable")
	assert.True(t, KnownLeg(LegQwen))
	assert.Error(t, RemoveLeg(path, "nope"))
}

// ----------------------------------------------------------- phase 2: priors

func TestDomainPriorOverridesLoadBothFormats(t *testing.T) {
	t.Cleanup(func() { LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	path := filepath.Join(t.TempDir(), "priors.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"grok": 7.3, "gemini": {"all": 8.0, "code": 7.6, "editorial": 8.5}}`), 0o644))
	n, err := LoadPriorOverridesFrom(path)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, 7.3, QualityPrior(LegGrok), "v1 number")
	assert.Equal(t, 8.0, QualityPrior(LegGemini), "v2 'all'")
	assert.Equal(t, 7.6, QualityPriorFor(LegGemini, DomainCode))
	assert.Equal(t, 8.5, QualityPriorFor(LegGemini, DomainEditorial))
	assert.Equal(t, 8.0, QualityPriorFor(LegGemini, DomainResearch), "no research entry → overall")
	// The ladder re-sorts on new priors: gemini 8.0 now sits at/after kimi 8.0, never before glm.
	for i := 1; i < len(AllLegs); i++ {
		assert.GreaterOrEqual(t, QualityPrior(AllLegs[i]), QualityPrior(AllLegs[i-1]), "ladder inversion after override at %s", AllLegs[i])
	}
}

func TestProposeDomainPriorsMapsIndices(t *testing.T) {
	models := []AAModel{
		{Slug: "claude-opus-5-5", Name: "Claude Opus 5.5", CodingIndex: 60, IntelligenceIndex: 80},
		{Slug: "gemini-3-7-flash", Name: "Gemini 3.7 Flash", CodingIndex: 45, IntelligenceIndex: 72},
		{Slug: "grok-build-0-1", Name: "Grok Build", CodingIndex: 42, IntelligenceIndex: 0},
	}
	got, err := ProposeDomainPriors(models)
	require.NoError(t, err)
	g := got[LegGemini]
	assert.InDelta(t, 45.0/60*9.5, g["code"], 0.06)
	assert.InDelta(t, 72.0/80*9.5, g["editorial"], 0.06)
	assert.Equal(t, g["editorial"], g["research"])
	assert.InDelta(t, (g["code"]+g["editorial"])/2, g["all"], 0.06)
	k := got[LegGrok]
	assert.Contains(t, k, "code")
	assert.NotContains(t, k, "editorial", "no intelligence index → no prose prior")
	assert.Equal(t, k["code"], k["all"])
	_, hasKimi := got[LegKimi]
	assert.False(t, hasKimi, "no row → untouched")
}

func TestBlendedQualityForUsesDomainEvidence(t *testing.T) {
	s := LegStats{Scored: 10, AvgQuality: 5.0, ByDomain: map[Domain]ClassStat{DomainEditorial: {Scored: 10, AvgQuality: 9.0}}}
	assert.Greater(t, BlendedQualityFor(LegGrok, s, DomainEditorial), BlendedQualityFor(LegGrok, s, DomainCode),
		"a leg graded well on prose and badly on code ranks differently per domain")
}

func TestLedgerStatsByDomain(t *testing.T) {
	l := &Ledger{}
	l.Events = append(l.Events,
		Event{Leg: LegGrok, Outcome: "ok", Quality: 9, Domain: "editorial", Duration: 10, Task: "x"},
		Event{Leg: LegGrok, Outcome: "ok", Quality: 4, Domain: "code", Duration: 10, Task: "y"},
		Event{Leg: LegGrok, Outcome: "ok", Quality: 8, Domain: "editorial", Duration: 10, Task: "z"},
	)
	st := l.Stats()[LegGrok]
	require.NotNil(t, st.ByDomain)
	assert.Equal(t, 2, st.ByDomain[DomainEditorial].Scored)
	assert.InDelta(t, 8.5, st.ByDomain[DomainEditorial].AvgQuality, 0.01)
	assert.InDelta(t, 4.0, st.ByDomain[DomainCode].AvgQuality, 0.01)
}

// ----------------------------------------------------------- phase 3: value

func TestEstimateCost(t *testing.T) {
	assert.Equal(t, 0.0, EstimateCost(LegClaude, 100_000), "subscription legs have no per-token $")
	assert.Equal(t, 0.0, EstimateCost(LegFree, 100_000))
	glm := EstimateCost(LegGLM, 1_000_000) // 0.75M in × 1.09 + 0.25M out × 3.43
	assert.InDelta(t, 0.75*1.09+0.25*3.43, glm, 0.001)
	assert.Equal(t, 0.0, EstimateCost("nope", 1000))
}

func TestValueRankPrefersCheapWhenQualityTies(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_WEIGHTS", "")
	t.Setenv("CAPTAIN_VALUE_TAU", "")
	// gemini 7.8 @ $0.38/2.0 vs glm 7.8 @ $0.6/2.2 → same quality, gemini cheaper.
	rows := ValueRank(ClassMedium, DomainGeneral, []Leg{LegGLM, LegGemini}, nil, 100_000, nil)
	require.Len(t, rows, 2)
	assert.Equal(t, LegGemini, rows[0].Leg)
	assert.Greater(t, rows[0].Value, rows[1].Value)
}

func TestValueRankGoodEnoughThreshold(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_TAU", "")
	rows := ValueRank(ClassMedium, DomainGeneral, []Leg{LegFree, LegQwen, LegGrok, LegGLM}, nil, 50_000, nil)
	legs := Legs(rows)
	assert.NotContains(t, legs, LegFree, "6.0 < τ(medium)=7.0")
	assert.NotContains(t, legs, LegQwen, "6.8 < 7.0")
	assert.Contains(t, legs, LegGrok, "7.0 clears τ(medium)=7.0")
	assert.Contains(t, legs, LegGLM)
	trivial := Legs(ValueRank(ClassTrivial, DomainGeneral, []Leg{LegFree, LegGLM}, nil, 50_000, nil))
	assert.Equal(t, LegFree, trivial[0], "trivial: cost weighs more than quality - free (6.0 ≥ τ=5.5) beats a $0.05 glm")
	medium := Legs(ValueRank(ClassMedium, DomainGeneral, []Leg{LegFree, LegGLM}, nil, 50_000, func(Leg) float64 { return 0 }))
	assert.Equal(t, LegGLM, medium[0], "medium: quality weighs more (and free is under τ anyway)")
	t.Setenv("CAPTAIN_VALUE_TAU", "5,6.5")
	assert.Contains(t, Legs(ValueRank(ClassMedium, DomainGeneral, []Leg{LegQwen}, nil, 50_000, nil)), LegQwen, "env lowers the bar")
}

func TestValueRankSubscriptionPressureCountsAsCost(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_TAU", "5,5")
	// grok (sub, 7.0) vs qwen (paid, 6.8): grok wins at zero pressure, loses when its window is closing.
	free := Legs(ValueRank(ClassMedium, DomainCode, []Leg{LegGrok, LegQwen}, nil, 50_000, func(Leg) float64 { return 0 }))
	assert.Equal(t, LegGrok, free[0])
	pressured := Legs(ValueRank(ClassMedium, DomainCode, []Leg{LegGrok, LegQwen}, nil, 50_000, func(l Leg) float64 {
		if l == LegGrok {
			return 1
		}
		return 0
	}))
	assert.Equal(t, LegQwen, pressured[0], "a closing window is a cost")
}

func TestValueRankDomainPriorMovesTheOrder(t *testing.T) {
	t.Cleanup(func() { LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	t.Setenv("CAPTAIN_VALUE_TAU", "5,5")
	path := filepath.Join(t.TempDir(), "legs.json")
	require.NoError(t, SaveRegistryOverlay(path, registryOverlay{Legs: []LegSpec{{ID: "muse", Provider: "openrouter", Model: "meta/muse-spark-1.3-contributor",
		PriceIn: 0.10, PriceOut: 0.20, Prior: 7.0, DomainPrior: map[Domain]float64{DomainEditorial: 8.6}}}}))
	_, err := LoadRegistry(path)
	require.NoError(t, err)
	code := Legs(ValueRank(ClassMedium, DomainCode, []Leg{LegGLM, "muse"}, nil, 50_000, nil))
	prose := Legs(ValueRank(ClassMedium, DomainEditorial, []Leg{LegGLM, "muse"}, nil, 50_000, nil))
	assert.Equal(t, LegGLM, code[0], "on code, glm's 7.8 beats muse's 7.0 even at 6× the price")
	assert.Equal(t, Leg("muse"), prose[0], "on prose, muse's 8.6 domain prior + low price wins")
}

func TestValueRankLatencyBreaksTies(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_TAU", "5,5")
	stats := map[Leg]LegStats{LegGLM: {N: 5, AvgDurationMs: 300_000}, LegGemini: {N: 5, AvgDurationMs: 20_000}}
	rows := ValueRank(ClassMedium, DomainGeneral, []Leg{LegGLM, LegGemini}, stats, 50_000, nil)
	assert.Equal(t, LegGemini, rows[0].Leg)
}

func TestValueRankSortsUnreliableLegsLast(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_TAU", "5,5")
	// kimi: $0 (NIM) and prior 8.0 wins on value - but 0 successes, 2 fails.
	// (minimax played this part until NIM retired it and it became a paid
	// OpenRouter leg, 2026-09-15.)
	stats := map[Leg]LegStats{LegKimi: {N: 0, Fails: 2}, LegGemini: {N: 3, Scored: 0}}
	rows := ValueRank(ClassMedium, DomainGeneral, []Leg{LegKimi, LegGemini}, stats, 50_000, nil)
	require.Len(t, rows, 2)
	assert.Equal(t, LegGemini, rows[0].Leg, "a leg failing a third of its runs never leads the cheap path")
	assert.True(t, rows[1].Unreliable)
	// One fail in six is not unreliable.
	stats[LegKimi] = LegStats{N: 5, Fails: 1}
	rows = ValueRank(ClassMedium, DomainGeneral, []Leg{LegKimi, LegGemini}, stats, 50_000, nil)
	assert.Equal(t, LegKimi, rows[0].Leg)
}

func TestMatchAAPrefersExactSlugThenBaseVariant(t *testing.T) {
	models := []AAModel{
		{Slug: "gpt-6-astra-non-reasoning", Name: "GPT-6 Astra (Non-reasoning)", CodingIndex: 76.2, IntelligenceIndex: 45.2},
		{Slug: "gpt-6-astra-xhigh", Name: "GPT-6 Astra (xhigh)", CodingIndex: 75.9, IntelligenceIndex: 52.5},
		{Slug: "gpt-6-astra", Name: "GPT-6 Astra (max)", CodingIndex: 76.9, IntelligenceIndex: 52.8},
		{Slug: "claude-opus-5", Name: "Claude Opus 5 (Adaptive Reasoning, Max Effort)", CodingIndex: 78, IntelligenceIndex: 50.7},
		{Slug: "claude-opus-5-5", Name: "Claude Opus 5.5 (Adaptive Reasoning, Max Effort)", CodingIndex: 90, IntelligenceIndex: 60},
		{Slug: "claude-opus-5-5-xhigh", Name: "Claude Opus 5.5 (Adaptive Reasoning, Xhigh Effort)", CodingIndex: 89, IntelligenceIndex: 59},
	}
	m, ok := MatchAA(models, LegCodexCLI)
	require.True(t, ok)
	assert.Equal(t, "gpt-6-astra-xhigh", m.Slug, "the registry's exact slug - the variant the leg actually runs")
	m, ok = MatchAA(models, LegClaude)
	require.True(t, ok)
	assert.Equal(t, "claude-opus-5-5", m.Slug)
	// When the feed lacks Opus 5.5, claude reads the Opus 5 row (as grok-max
	// reads grok-4.6 when 4.7 is missing) instead of dropping off the ranking.
	m, ok = MatchAA(models[:4], LegClaude)
	require.True(t, ok)
	assert.Equal(t, "claude-opus-5", m.Slug, "the newest scored member of the family, base variant")
	// Substring fallback: shortest slug is the base variant.
	loose := []AAModel{
		{Slug: "muse-spark-1-3-xhigh", Name: "Muse Spark 1.3 (xhigh)", CodingIndex: 76.5, IntelligenceIndex: 45.2},
		{Slug: "muse-spark-1-3", Name: "Muse Spark 1.3 (max)", CodingIndex: 75.8, IntelligenceIndex: 48.2},
	}
	path := filepath.Join(t.TempDir(), "legs.json")
	require.NoError(t, AddLeg(path, LegSpec{ID: "muse", Provider: "openrouter", Model: "meta/muse-spark-1.3", AA: "muse-spark-1-3", Prior: 7.5}))
	t.Cleanup(func() { LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	m, ok = MatchAA(loose, "muse")
	require.True(t, ok)
	assert.Equal(t, "muse-spark-1-3", m.Slug)
}
