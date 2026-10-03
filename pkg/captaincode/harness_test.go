package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The user reads harness and provenance, not routing legs: two legs on one
// model family behind one provider are one name (2026-10-03).
func TestHarnessNamesTheHarnessAndTheProvenance(t *testing.T) {
	for leg, want := range map[Leg]string{
		LegClaude: "claude-cli", LegCodexCLI: "codex-cli", LegCursor: "cursor-cli",
		"grok": "grok-xai", "grok-max": "grok-xai",
		"codex": "gpt-openai", "luna": "gpt-openai",
		"kimi": "kimi-nim", "glm": "glm-nim", "ds-flash": "deepseek-nim",
		"ds4-flash": "deepseek-hf", "step": "step-hf",
		"gemini": "gemini-openrouter", "gpt-oss": "gpt-oss-openrouter", "qwen": "qwen-openrouter",
	} {
		assert.Equal(t, want, Harness(leg), "leg %s", leg)
	}
}

func TestModelShortReadsLikeAVersion(t *testing.T) {
	assert.Equal(t, "opus-5.5", ModelShort("claude-opus-5-5"))
	assert.Equal(t, "opus-5.5", ModelShort("claude-opus-5-5-frontier"))
	assert.Equal(t, "kimi-k3", ModelShort("moonshotai/kimi-k3"))
	assert.Equal(t, "step-3.5-flash", ModelShort("stepfun-ai/Step-3.5-Flash"))
	assert.Equal(t, "qwen3.5-397b-a17b", ModelShort("qwen/qwen3.5-397b-a17b"), "only a trailing dashed version is dotted")
}

func TestRunLabelSaysWhatRanAndHowHard(t *testing.T) {
	assert.Equal(t, "kimi-nim:kimi-k3@effort:high", RunLabel("kimi", EffortHigh))
	assert.Equal(t, "codex-cli:"+ModelAt(LegCodexCLI, EffortMax)+"@effort:max", RunLabel(LegCodexCLI, EffortMax))
	assert.Contains(t, RunLabel("kimi", ""), "@effort:default")
}

func TestEffortModelsGroupTheEffortsThatPickAModel(t *testing.T) {
	ms := EffortModels(LegCodexCLI)
	var all []Effort
	for _, m := range ms {
		assert.NotEmpty(t, m.Model)
		all = append(all, m.Efforts...)
	}
	assert.Equal(t, displayEfforts, all, "every effort appears once, in order")
}
