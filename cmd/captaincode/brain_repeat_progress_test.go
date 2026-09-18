package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE $50 INCIDENT (2026-08-31). An open-ended `/repeat /team …until the
// backlog is empty` ran 22 rounds over 3h50m and burned an entire OpenRouter
// balance. From round ~15 it was re-verifying an already-empty backlog - its
// own output said so. Every round SUCCEEDED, so the consecutive-failure guard
// never fired. The missing stop condition is progress, not success: two rounds
// that say the same thing mean the loop has nothing left to do.

func TestRoundsAreNearIdenticalIgnoresCosmeticDrift(t *testing.T) {
	a := "Checked the backlog. Nothing left to do: all 4 items are closed."
	cases := []struct {
		name string
		b    string
		same bool
	}{
		{"identical", a, true},
		{"whitespace and case", "checked the  BACKLOG.\nNothing left to do: all 4 items are closed.", true},
		{"reworded slightly", "Checked the backlog. Nothing left to do: all four items are closed.", true},
		{"a timestamp changed", "Checked the backlog at 14:05. Nothing left to do: all 4 items are closed.", true},
		{"real work happened", "Fixed the failing auth test and opened PR #412; two backlog items remain.", false},
		{"same topic, different finding", "Checked the backlog. Item 3 is still open and blocked on the migration.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, score := roundsAreNearIdentical(a, c.b)
			assert.Equal(t, c.same, got, "similarity %.2f", score)
		})
	}
}

func TestNearIdenticalIgnoresEmptyAndShortRounds(t *testing.T) {
	// A failed round contributes no text; two empty rounds are a failure
	// signature, which the consecutive-failure guard already owns.
	same, _ := roundsAreNearIdentical("", "")
	assert.False(t, same, "empty rounds are not evidence of no progress")
	same, _ = roundsAreNearIdentical("ok", "ok")
	assert.False(t, same, "a two-character answer is too short to judge")
}

func TestRepeatStopsWhenTwoRoundsSaySameThing(t *testing.T) {
	b := teamBrain()
	th := &repeatThread{id: "rp_test", task: "keep checking the backlog", target: 0}
	b.roundSummaryFn = func(string) string { return "checked, nothing to do" }
	rounds := 0
	b.chatFn = func(w *captureWriter) {
		rounds++
		fmt.Fprintf(w, "Checked the backlog. Nothing left to do: all 4 items are closed. (pass %d)", rounds)
	}

	b.runRepeat(context.Background(), th, oaiChatReq{Model: "free"})

	assert.Equal(t, 2, rounds, "the second round proves nothing changed; a third would be waste")
	assert.True(t, th.finished)
	assert.Contains(t, strings.ToLower(th.lastErr+th.stopReason), "no progress",
		"the thread records WHY it stopped, so the user does not think it crashed")
}

func TestRepeatKeepsGoingWhileRoundsDiffer(t *testing.T) {
	b := teamBrain()
	th := &repeatThread{id: "rp_test2", task: "work the backlog", target: 4}
	b.roundSummaryFn = func(string) string { return "did a thing" }
	// Real progress means each round REPORTS something different. A round that
	// only changes its counter is the runaway case, and the guard above owns it.
	work := []string{
		"Fixed the failing auth test: the token clock was compared before refresh, so it expired mid-request.",
		"Migrated the storage callers to the new interface and deleted the compatibility shim.",
		"Found the flaky watcher: fsevents coalesces two writes, and the test asserted on the first.",
		"Backfilled the missing migration for the priors table and re-ran the importer end to end.",
	}
	rounds := 0
	b.chatFn = func(w *captureWriter) {
		fmt.Fprint(w, work[rounds])
		rounds++
	}

	b.runRepeat(context.Background(), th, oaiChatReq{Model: "free"})

	require.Equal(t, 4, rounds, "real progress must not trip the guard")
}
