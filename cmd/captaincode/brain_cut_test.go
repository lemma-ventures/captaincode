package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cutConvo(framing, turns int) string {
	var sb strings.Builder
	sb.WriteString("[system]\n" + strings.Repeat("framing ", framing/8) + "\n\n[user]\nOPENING ask\n\n")
	for i := 0; i < turns; i++ {
		fmt.Fprintf(&sb, "[user]\nask %d %s\n\n[assistant]\nanswer %d %s\n\n", i, strings.Repeat("q ", 400), i, strings.Repeat("a ", 2000))
	}
	sb.WriteString("[user]\nLIVE ask\n\n")
	return sb.String()
}

// The cut works in whole turns and drops the oldest middle first.
func TestCutDropsTheOldestWholeTurns(t *testing.T) {
	in := cutConvo(2_000, 40)
	out := cutToFit(in, len(in)/2, "", 0)
	require.LessOrEqual(t, len(out), len(in)/2)
	assert.True(t, strings.HasPrefix(out, "[system]\nframing"), "framing first")
	assert.Contains(t, out, "[user]\nOPENING ask")
	assert.True(t, strings.HasSuffix(out, "[user]\nLIVE ask\n\n"), "the live turn is whole and last")
	assert.Contains(t, out, "[user]\nask 39 ")
	assert.NotContains(t, out, "[user]\nask 0 ")
	// Every kept message starts at a marker: nothing was cut mid-turn.
	_, conv := splitLead(out)
	for _, tr := range splitTurns(conv) {
		body := conv[tr.start:tr.end]
		if strings.HasPrefix(body, "[user]\nask ") || strings.HasPrefix(body, "[assistant]\nanswer ") {
			assert.True(t, strings.HasSuffix(body, "\n\n"), "a whole message")
		}
	}
	// The kept history is contiguous up to the live turn.
	first := strings.Index(out, "[user]\nask ")
	var n int
	fmt.Sscanf(out[first:], "[user]\nask %d", &n)
	for i := n; i < 40; i++ {
		assert.Contains(t, out, fmt.Sprintf("[user]\nask %d ", i))
	}
	assert.Contains(t, out, fmt.Sprintf("- ask %d q q", n-1), "the newest left-out request is indexed")
}

// A summary that covers the left-out turns stands in for them.
func TestCutUsesTheSummaryForWhatItCovers(t *testing.T) {
	in := cutConvo(500, 40)
	_, conv := splitLead(in)
	covered := strings.Index(conv, "[user]\nask 30 ")
	out := cutToFit(in, len(in)/2, "SUMMARY OF ASKS 0-29", covered)
	assert.Contains(t, out, "SUMMARY OF ASKS 0-29")
	assert.NotContains(t, out, "- ask 5 q", "covered requests are not indexed again")
}

// arc, 2026-10-08: a large framing block and a replay 0.7% over a 400k
// budget. The summary step failed ("no turn boundary to cut at") and the old
// window cut 3k chars out of the middle of a message.
func TestArcCaseIsCutInWholeTurnsWithoutASummary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := teamBrain()
	b.summarizeFn = func(span, prev string) (string, error) { t.Error("no summary for a 1% overflow"); return "", nil }
	var sb strings.Builder
	sb.WriteString("[system]\nYou are an agent.")
	for i := 0; i < 12; i++ { // a real framing: distinct paragraphs pruning keeps
		fmt.Fprintf(&sb, "\n\nRule %d: %s", i, strings.Repeat(fmt.Sprintf("guideline %d ", i), 150))
	}
	sb.WriteString("\n\n[user]\nOPENING\n\n")
	for i := 0; sb.Len() < 400_000; i++ {
		fmt.Fprintf(&sb, "[user]\nstep %d %s\n\n[assistant]\ndone %d %s\n\n", i, strings.Repeat("x", 3_000), i, strings.Repeat("y", 5_000))
	}
	sb.WriteString("[user]\nLIVE\n\n")
	in := sb.String()
	budget := len(in) - 3_000
	out := b.fitPrompt(defaultWorkspace(), captaincode.LegCodex, in, budget)
	require.LessOrEqual(t, len(out), budget)
	assert.Contains(t, out, "You are an agent.", "the framing stays")
	assert.Contains(t, out, "OPENING")
	assert.True(t, strings.HasSuffix(out, "[user]\nLIVE\n\n"))
	assert.NotContains(t, out, "[user]\nstep 0 ", "the oldest turn is what goes")
	assert.Contains(t, out, "- step 0 ", "and it is indexed")
}

// A live turn too big for the budget keeps its start and end.
func TestCutTrimsAGiantLiveTurn(t *testing.T) {
	in := "[user]\nOPENING\n\n[assistant]\nok\n\n[user]\nSTART " + strings.Repeat("z", 50_000) + " END\n\n"
	out := cutToFit(in, 20_000, "", 0)
	require.LessOrEqual(t, len(out), 20_000)
	assert.Contains(t, out, "START")
	assert.Contains(t, out, "END")
	assert.Contains(t, out, "the middle of this turn was cut")
}
