package main

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPruneSpanRemovesMechanicalBulk(t *testing.T) {
	dump := strings.Repeat("func handler() { /* long pasted file */ }\n", 500) // ~20k
	in := "[user]\nlook at this\n\n" + dump + "\n\n[assistant]\nnoted\n\n\n\n\n[user]\nagain\n\n" + dump

	out, saved := pruneSpan(in)
	assert.Greater(t, saved, 20_000, "a repeated 20k dump is mostly redundancy")
	assert.Less(t, len(out), len(in)/2)
	assert.Contains(t, out, "identical to an earlier block", "the repeat becomes a pointer, not a silent deletion")
	assert.Contains(t, out, "chars elided from this block", "the giant block is snipped with a visible marker")
	assert.Contains(t, out, "look at this", "conversation prose survives")
	assert.Contains(t, out, "noted")
	assert.NotContains(t, out, "\n\n\n", "blank-line drift collapsed")
}

func TestPruneSpanLeavesNormalProseAlone(t *testing.T) {
	in := "[user]\nfix the typo in README\n\n[assistant]\ndone, changed line 12\n\n[user]\nthanks"
	out, saved := pruneSpan(in)
	assert.Equal(t, 0, saved, "an ordinary short conversation has no bulk to remove")
	assert.Equal(t, in, out)
}

func TestFirstUserTurnPreservesOriginalIntent(t *testing.T) {
	p := "[system]\nframing\n\n[user]\nbuild the deck, never use jargon\n\n[assistant]\nok\n\n[user]\nnow the pricing page"
	assert.Contains(t, firstUserTurn(p), "never use jargon")
	assert.NotContains(t, firstUserTurn(p), "pricing page", "only the OPENING turn")
}

// Staged compaction: when the deterministic snip alone fits the budget, the
// turn must skip the LLM entirely - that is where the latency was going.
func TestFitPromptSkipsLLMWhenPruneIsEnough(t *testing.T) {
	b := teamBrain()
	called := 0
	b.summarizeFn = func(span, prev string) (string, error) { called++; return "SUM", nil }

	dump := strings.Repeat("x", 5_000)
	var sb strings.Builder
	sb.WriteString("[user]\noriginal ask\n\n")
	for i := 0; i < 40; i++ { // the same block over and over: pure redundancy
		fmt.Fprintf(&sb, "[assistant]\n%s\n\n", dump)
	}
	in := sb.String()
	require.Greater(t, len(in), 190_000)

	out := b.fitPrompt(defaultWorkspace(), captaincode.LegFree, in, 100_000)
	assert.LessOrEqual(t, len(out), 100_000, "fits the budget")
	assert.Equal(t, 0, called, "no summarization call was needed")
	assert.Contains(t, out, "original ask")
}

// When pruning is not enough, the LLM stage still runs - on the pruned text.
func TestFitPromptSummarizesWhatSurvivesPruning(t *testing.T) {
	b := teamBrain()
	var sawLen int
	b.summarizeFn = func(span, prev string) (string, error) { sawLen += len(span); return "SUM", nil }

	var sb strings.Builder
	sb.WriteString("[user]\nthe original ask\n\n")
	for i := 0; i < 60; i++ { // distinct blocks: not dedupable, must be summarized
		fmt.Fprintf(&sb, "[user]\nturn %d %s\n\n[assistant]\nreply %d\n\n", i, strings.Repeat("prose ", 900), i)
	}
	in := sb.String()

	out := b.fitPrompt(defaultWorkspace(), captaincode.LegFree, in, 60_000)
	assert.Greater(t, sawLen, 0, "the summarizer ran")
	assert.Contains(t, out, "SUM")
	assert.Contains(t, out, "the original ask", "the opening request survives compaction verbatim")
}

func TestCompactLegDefaultsToFreeAndIsOverridable(t *testing.T) {
	// Default prefers kimi (fast + NIM terms) when runnable; free is the
	// fallback when kimi is not in the allowed set. The override is cleared
	// first: a developer who has set it in their own shell must not see a
	// different default than CI does.
	t.Setenv("CAPTAIN_COMPACT_LEG", "")
	t.Setenv("CAPTAIN_LEGS", "free,grok")
	assert.Equal(t, captaincode.LegFree, compactLeg())
	t.Setenv("CAPTAIN_LEGS", "free,grok,gemini")
	assert.Equal(t, captaincode.LegGemini, compactLeg())
	t.Setenv("CAPTAIN_COMPACT_LEG", "gemini")
	assert.Equal(t, captaincode.LegGemini, compactLeg())
	t.Setenv("CAPTAIN_COMPACT_LEG", "not-a-leg")
	assert.Equal(t, captaincode.LegGemini, compactLeg(), "an unknown override falls back to the default preference, never breaks the turn")
}

// A pruned block must never end on a torn multibyte character: the pruned
// replay is codex exec's argv, and its Rust CLI refuses invalid UTF-8
// ("invalid UTF-8 was detected in one or more arguments" - every turn in a
// folder failed, 2026-09-17).
func TestPruneSpanCutsOnRuneBoundaries(t *testing.T) {
	big := strings.Repeat("→·—", pruneBlockLimit) // 3-byte runes only: any byte cut mid-rune breaks validity
	out, saved := pruneSpan("[user]\n" + big + "\n\n[assistant]\nok\n")
	assert.Greater(t, saved, 0)
	assert.True(t, utf8.ValidString(out), "the pruned prompt is valid UTF-8")
	assert.Contains(t, out, "chars elided from this block")
	assert.True(t, utf8.ValidString(firstUserTurn("[user]\n"+strings.Repeat("é", 5000)+"\n[assistant]\nx")))
}
