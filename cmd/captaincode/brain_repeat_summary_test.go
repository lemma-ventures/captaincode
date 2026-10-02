package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live 2026-10-02: the digest model returned nothing for two rounds, and the
// fallback showed each round's first line - its opening narration. A
// 55-minute round that committed three fixes read "Orienting: three roadmap
// rounds ran this morning…". Both legs are tried, then the final answer's
// first line stands in.
func TestRoundSummaryFallsBackToTheFinalAnswer(t *testing.T) {
	t.Setenv("CAPTAIN_LEGS", "")
	b := teamBrain()
	calls := 0
	b.roundSummaryFn = func(string) string { calls++; return "" }
	streamed := "Orienting: three roadmap rounds ran this morning.\n\nI've committed three fixes on own-skills-shelf."
	final := "I've committed three fixes on own-skills-shelf.\n\n| Commit | What |"
	assert.Equal(t, "I've committed three fixes on own-skills-shelf.", b.roundSummary("/frontier continue", streamed, final))
	assert.Equal(t, 2, calls, "the compaction leg, then the free leg")

	assert.Equal(t, "Orienting: three roadmap rounds ran this morning.", b.roundSummary("/frontier continue", streamed, ""),
		"without a final answer the streamed text is all there is")
}

// The digest read the first 1,200 characters of a round: its narration.
func TestRoundSummaryReadsTheEndOfALongRound(t *testing.T) {
	b := teamBrain()
	var got string
	b.roundSummaryFn = func(in string) string { got = in; return "did it" }
	text := "Orienting: reading the roadmap.\n\n" + strings.Repeat("progress line\n", 2_000) + "Implemented attempt accounting; tests pass."
	assert.Equal(t, "did it", b.roundSummary("continue", text, ""))
	assert.Contains(t, got, "Implemented attempt accounting", "the conclusion reaches the digest")
	assert.Less(t, len(got), 14_000, "bounded")
}

// The round's final answer is the newest run of its task since the round
// began; the run record holds the task without its /frontier directive.
func TestRoundFinalIsThisRoundsRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	recordRunHistory(runRecord{Kind: "frontier", Task: "continue implementing roadmap", Output: "an earlier round"})
	start := time.Now()
	time.Sleep(10 * time.Millisecond)
	recordRunHistory(runRecord{Kind: "solo", Task: "something else", Output: "not this one"})
	recordRunHistory(runRecord{Kind: "frontier", Task: "continue implementing roadmap\n\n[captain] round contract", Output: "this round's answer"})
	require.Equal(t, "this round's answer", roundFinal("/frontier continue implementing roadmap", start))
	assert.Empty(t, roundFinal("/frontier continue implementing roadmap", time.Now().Add(time.Minute)), "nothing recorded since")
}
