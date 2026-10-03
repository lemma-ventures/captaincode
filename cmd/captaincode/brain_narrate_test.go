package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNarrationMapsLinesToSteps(t *testing.T) {
	got := parseNarration("1. fetched `consensus.app`\n\n2) read the **pricing page**\n5. out of range\nnoise", 3)
	assert.Equal(t, []string{"fetched consensus.app", "read the pricing page", ""}, got)
}

// One plain line per step, with its time, in the turn's feed (2026-10-03).
func TestNarratorWritesOneLinePerStep(t *testing.T) {
	oldTick, oldWait := narrateTick, narrateWait
	narrateTick, narrateWait = 10*time.Millisecond, 0
	t.Cleanup(func() { narrateTick, narrateWait = oldTick, oldWait })

	var mu sync.Mutex
	var lines []string
	feed := newProgressFeed("step", func(s string) {
		mu.Lock()
		lines = append(lines, s)
		mu.Unlock()
	})
	defer feed.close()
	steer := captaincode.NewSteer("/repo")
	at := time.Date(2026, 10, 3, 14, 41, 16, 0, time.Local)
	steer.RecordStep(captaincode.Step{At: at, Tool: "bash", Input: "curl -sSL https://consensus.app/pricing/", Output: "Pricing Individual Team"})
	var asked string
	feed.narrate(steer, "is consensus.app an alternative to Semantic Scholar?", func(task string, steps []captaincode.Step) ([]string, error) {
		asked = narrationPrompt(task, steps)
		return []string{"read the pricing page: individual and team plans"}, nil
	})
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(lines, ""), "≡ 14:41:16  read the pricing page")
	}, 2*time.Second, 10*time.Millisecond)
	assert.Contains(t, asked, "Semantic Scholar", "the summarizer knows what was asked")
	assert.Contains(t, asked, "1. [14:41:16] bash: curl -sSL https://consensus.app/pricing/")
	assert.Contains(t, asked, "result: Pricing Individual Team")
}

func TestNarrationCanBeTurnedOff(t *testing.T) {
	t.Setenv("CAPTAIN_NARRATE", "0")
	feed := newProgressFeed("step", func(string) { t.Error("nothing is narrated") })
	defer feed.close()
	steer := captaincode.NewSteer("/repo")
	steer.RecordStep(captaincode.Step{Tool: "bash", Input: "ls"})
	called := false
	feed.narrate(steer, "x", func(string, []captaincode.Step) ([]string, error) { called = true; return nil, nil })
	time.Sleep(50 * time.Millisecond)
	assert.False(t, called)
}
