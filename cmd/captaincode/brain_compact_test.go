package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R1 (PRIME_AGENT_NOTES): overflow is SUMMARIZED, not truncated. The old
// windowPrompt cut the middle out with an "[elided]" marker - losing decisions,
// style notes and file lists that "in my writing style" turns depend on.

// longConvo builds a conversation whose replay exceeds the trivial budget.
func longConvo(turns int) []string {
	out := make([]string, 0, turns*2)
	for i := 0; i < turns; i++ {
		out = append(out,
			fmt.Sprintf("user turn %d: we decided the paper is called OPSIS and uses mobility classes. %s", i, strings.Repeat("filler prose ", 400)),
			fmt.Sprintf("assistant turn %d: noted.", i))
	}
	return out
}

func compactBrain(t *testing.T) (*brain, *[]string, *int) {
	t.Helper()
	b := teamBrain()
	var prompts []string
	calls := 0
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		prompts = append(prompts, prompt)
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	b.summarizeFn = func(span, prev string) (string, error) {
		calls++
		tag := fmt.Sprintf("SUMMARY#%d(prev=%v,span=%dch)", calls, prev != "", len(span))
		if prev != "" {
			return prev + "+" + tag, nil
		}
		return tag, nil
	}
	return b, &prompts, &calls
}

func TestOverflowIsSummarizedNotTruncated(t *testing.T) {
	b, prompts, calls := compactBrain(t)
	turns := append(longConvo(30), "fix typo in README") // trivial → 48k budget
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, turns...))
	require.Equal(t, 200, rec.Code)

	require.Len(t, *prompts, 1)
	p := (*prompts)[0]
	// The span is folded in bounded slices (compactChunk), so the call count
	// tracks span size - what matters is that it was SUMMARIZED, not truncated.
	assert.GreaterOrEqual(t, *calls, 1, "the overflow was summarized")
	assert.Contains(t, p, "SUMMARY#1", "the summary replaces the elided span")
	assert.Contains(t, p, "compacted summary", "labeled for the worker")
	assert.NotContains(t, p, "conversation truncated", "no lossy elision marker")
	assert.Contains(t, p, "fix typo in README", "the live turn survives")
	assert.Less(t, len(p), 70_000, "the budget still holds (got %d)", len(p))
	// The kept tail must start at a turn boundary, never mid-turn: the summary
	// block is followed immediately by a whole [user]/[assistant] turn.
	assert.Regexp(t, `SUMMARY#\d+\([^)]*\)\n\n\[(user|assistant)\]`, p)
}

func TestCompactionCacheAndIterativeGrowth(t *testing.T) {
	b, _, calls := compactBrain(t)
	base := longConvo(30)

	// Two turns in the same session (distinct tasks so solo-dedupe stays out
	// of the way) → one summarize: the second turn hits the compaction cache.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, wfReq(false, append(base, fmt.Sprintf("fix typo in README v%d", i))...))
		require.Equal(t, 200, rec.Code)
	}
	firstTurnCalls := *calls
	require.GreaterOrEqual(t, firstTurnCalls, 1)
	// Turn two adds NOTHING: the cached summary is reused wholesale.
	assert.Equal(t, firstTurnCalls, *calls, "identical replay reuses the cached summary")

	// The conversation grows with GENUINELY new turns - repeating the earlier
	// ones would (correctly) be collapsed by the dedupe pass, which is not what
	// this test is about.
	grown := append([]string{}, base...)
	for i := 0; i < 12; i++ {
		grown = append(grown,
			fmt.Sprintf("later turn %d: we changed the pricing model. %s", i, strings.Repeat("fresh prose ", 400)),
			fmt.Sprintf("later reply %d: understood. %s", i, strings.Repeat("distinct reply ", 200)))
	}
	b2prompts := []string{}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		b2prompts = append(b2prompts, prompt)
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, append(grown, "fix typo in README v3")...))
	require.Equal(t, 200, rec.Code)
	assert.Greater(t, *calls, firstTurnCalls, "growth summarizes the NEW span")
	require.Len(t, b2prompts, 1)
	assert.Contains(t, b2prompts[0], "SUMMARY#1(prev=false", "the old summary survives inside the new one")
	assert.Contains(t, b2prompts[0], fmt.Sprintf("SUMMARY#%d(prev=true", firstTurnCalls+1), "iterated, not recomputed")
}

func TestCompactionFailsOpenToWindowing(t *testing.T) {
	b, prompts, _ := compactBrain(t)
	b.summarizeFn = func(span, prev string) (string, error) { return "", fmt.Errorf("free leg down") }
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, append(longConvo(30), "fix typo in README")...))
	require.Equal(t, 200, rec.Code)
	require.Len(t, *prompts, 1)
	assert.Contains(t, (*prompts)[0], "conversation truncated", "summarizer failure → the old lossy window, never a dead turn")
}

func TestCompactionKillSwitch(t *testing.T) {
	t.Setenv("CAPTAIN_COMPACT", "0")
	b, prompts, calls := compactBrain(t)
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, append(longConvo(30), "fix typo in README")...))
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, 0, *calls)
	assert.Contains(t, (*prompts)[0], "conversation truncated")
}

// A giant span must be folded in BOUNDED slices: one 700k-char call blew the
// free model's context and produced nothing in 5 minutes (live 2026-08-28).
func TestFoldSpanChunksAndFolds(t *testing.T) {
	b := teamBrain()
	var sizes []int
	b.summarizeFn = func(span, prev string) (string, error) {
		sizes = append(sizes, len(span))
		return prev + "|S", nil
	}
	span := strings.Repeat("[user]\nturn text here\n\n", 30_000) // ~690k chars
	sum, consumed, err := b.foldSpan(span, "", nil)
	require.NoError(t, err)
	assert.Equal(t, len(span), consumed, "whole span folded")
	require.Greater(t, len(sizes), 3, "span split into several calls")
	for _, n := range sizes {
		assert.LessOrEqual(t, n, compactChunk, "each call stays under the chunk cap")
	}
	assert.Equal(t, len(sizes), strings.Count(sum, "|S"), "each slice folds into the running summary")
}

// A mid-fold failure must persist progress: the next turn resumes instead of
// re-attempting the whole span forever.
func TestFoldSpanPartialProgressIsReported(t *testing.T) {
	b := teamBrain()
	calls := 0
	b.summarizeFn = func(span, prev string) (string, error) {
		calls++
		if calls == 3 {
			return "", fmt.Errorf("worker timed out")
		}
		return prev + "|S", nil
	}
	span := strings.Repeat("[user]\nturn text here\n\n", 30_000)
	sum, consumed, err := b.foldSpan(span, "", nil)
	require.Error(t, err)
	assert.Greater(t, consumed, 0, "partial progress is reported for persistence")
	assert.Less(t, consumed, len(span))
	assert.Equal(t, 2, strings.Count(sum, "|S"), "the two successful slices are kept")
}
