package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func adiTestFeed() *ADIFeed {
	return &ADIFeed{RunStamp: "2026-09-17T051707Z", Tuples: []ADITuple{
		{Provider: "openrouter", Model: "openai/gpt-oss-120b", Label: "Cerebras via OpenRouter", Rank: 1, Streak: 10, MeanModeShare: 1.0, Green: true,
			AsOf: "2026-09-16T231701Z", ProviderPrefs: map[string]any{"order": []any{"cerebras"}, "allow_fallbacks": false}},
		{Provider: "openrouter", Model: "openai/gpt-oss-120b", Label: "Groq via OpenRouter", Rank: 5, Streak: 0, MeanModeShare: 0.6, Green: false,
			ProviderPrefs: map[string]any{"order": []any{"groq"}, "allow_fallbacks": false}},
		{Provider: "openrouter", Model: "z-ai/glm-5.3", Label: "", Rank: 9, Streak: 0, MeanModeShare: 0.4, Green: false},
		{Provider: "nvidia_nim", Model: "moonshotai/kimi-k3", Label: "NIM hosted", Rank: 3, Streak: 2, MeanModeShare: 0.9, Green: true},
	}}
}

func TestPoolWordsAreReadAnywhereAndPathsAreNot(t *testing.T) {
	assert.Equal(t, Pool{OSS: true}, MidPromptPool("/oss fix the parser"))
	assert.Equal(t, Pool{Deterministic: true}, MidPromptPool("fix the parser /deterministic"))
	assert.Equal(t, Pool{OSS: true, Deterministic: true}, MidPromptPool("/repeat 5 /oss /det fix it"))
	assert.Equal(t, Pool{}, MidPromptPool("read /open/api.md and /oss/README"), "path segments are paths")
	assert.Equal(t, Pool{}, MidPromptPool("the oss community"), "no slash, no directive")
	assert.Equal(t, "oss+deterministic", Pool{OSS: true, Deterministic: true}.String())
}

func TestOpenWeightsByFamilyAndRegistryFlag(t *testing.T) {
	assert.True(t, OpenWeights(LegGLM))
	assert.True(t, OpenWeights(LegDeepSeek))
	assert.True(t, OpenWeights(LegKimi))
	assert.True(t, OpenWeights(LegGPTOSS))
	assert.True(t, OpenWeights(LegStep))
	assert.True(t, OpenWeights(LegDS4Flash))
	assert.False(t, OpenWeights(LegClaude))
	assert.False(t, OpenWeights(LegGrok))
	assert.False(t, OpenWeights(LegCodex))
	assert.False(t, OpenWeights(LegGemini), "gemini flash is closed; gemma would be open")
	for _, l := range FilterPool(Pool{OSS: true}, AllLegs) {
		assert.True(t, OpenWeights(l))
	}
	assert.NotContains(t, FilterPool(Pool{OSS: true}, AllLegs), LegClaude)
}

func TestDeterministicPoolFollowsTheADIFeed(t *testing.T) {
	old := ADISnapshot()
	defer SetADIFeed(old)
	SetADIFeed(nil)
	assert.Empty(t, FilterPool(Pool{Deterministic: true}, AllLegs), "no feed: nothing is known green")

	SetADIFeed(adiTestFeed())
	tup, ok := ADIFor(LegGPTOSS)
	require.True(t, ok)
	assert.True(t, tup.Green)
	assert.Equal(t, "Cerebras via OpenRouter", tup.Label, "green first, then by rank: the Cerebras pin, not the Groq one")
	assert.Equal(t, []any{"cerebras"}, ADIPinFor(LegGPTOSS)["order"])
	assert.Nil(t, ADIPinFor(LegGLM), "measured red: no pin")
	assert.Nil(t, ADIPinFor(LegClaude), "not measured")
	assert.Equal(t, []Leg{LegGPTOSS, LegKimi}, ADIGreenLegs(), "best ADI rank first")
	assert.Equal(t, []Leg{LegGPTOSS, LegKimi}, FilterPool(Pool{Deterministic: true}, AllLegs))
	assert.Equal(t, []Leg{LegGPTOSS, LegKimi}, FilterPool(Pool{OSS: true, Deterministic: true}, AllLegs), "both: open AND green")
	assert.Contains(t, ADIOneLine(LegGPTOSS), "green (ADI #1, streak 10")
	assert.Equal(t, "not measured by ADI", ADIOneLine(LegCursor))
	assert.Empty(t, ADIGreenUnregistered(), "every green tuple is served by a leg here")
}

func TestModifiersHoistPastControlWords(t *testing.T) {
	cases := map[string]string{
		"/oss /repeat 5 fix the parser":         "/repeat 5 /oss fix the parser",
		"/deterministic /team review the plan":  "/team /deterministic review the plan",
		"/quality /oss /parallel run the bench": "/parallel /quality /oss run the bench",
		"/oss /grok fix it":                     "/grok /oss fix it",
		"/oss /frontier prove it":               "/frontier /oss prove it",
		"/repeat 5 /oss fix the parser":         "/repeat 5 /oss fix the parser",
		"/oss fix the parser":                   "/oss fix the parser",
		"fix the parser /oss":                   "fix the parser /oss",
		"/quality address this /oss later":      "/quality address this /oss later",
		"/oss /repeat fix until green":          "/repeat /oss fix until green",
		"/det /codex-cli port the module":       "/codex-cli /det port the module",
	}
	for in, want := range cases {
		assert.Equal(t, want, HoistLeading(in), in)
	}
	assert.Equal(t, "team", LeadingForced("/team /oss x"))
	assert.Equal(t, "frontier", LeadingForced("/frontier x"))
	assert.Equal(t, "codex-cli", LeadingForced("/codex-cli /det x"), "the longest leg name wins")
	assert.Equal(t, "", LeadingForced("/oss x"))
	assert.Equal(t, "", LeadingForced("/repeat 5 /oss x"))
}
