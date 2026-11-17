package main

import (
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignedQueueStoresAndOffersTurnSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	_, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
	require.NoError(t, err)
	require.NoError(t, sqWrite("pending", sqEntry{ID: "in_one", PromptID: "prompt-one", Folder: "oak", Dir: dir, Text: "inspect", At: time.Now()}))
	sqFinish("in_one", "headless", "Inspected the repository. OPENROUTER_API_KEY="+"sk-live-fixture-secret")
	done := sqList("done")
	require.Len(t, done, 1)
	require.NotNil(t, done[0].Summary)
	assert.Equal(t, "prompt-one", done[0].Summary.PromptID)
	assert.NotContains(t, done[0].Summary.Done, "sk-live-fixture-secret")

	b := &brain{}
	summaries, err := b.linkTurnSummaries()
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, *done[0].Summary, summaries[0])
}

func TestSignedQueueTerminalRefusalsOfferFailedSummary(t *testing.T) {
	for _, test := range []struct {
		name       string
		how        string
		result     string
		wantStatus string
	}{
		{name: "expired", how: "expired", result: "failed: its approval ended before it ran", wantStatus: "failed"},
		{name: "dropped", how: "dropped", result: "failed: dropped with captain queue drop", wantStatus: "failed"},
		{name: "handed to the TUI", how: "tui", result: "handed to the TUI"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			dir := t.TempDir()
			_, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
			require.NoError(t, err)
			require.NoError(t, sqWrite("pending", sqEntry{ID: "remote", PromptID: "prompt-one", Dir: dir, Text: "inspect", At: time.Now()}))
			sqFinish("remote", test.how, test.result)

			summaries, err := (&brain{}).linkTurnSummaries()
			require.NoError(t, err)
			if test.wantStatus == "" {
				assert.Empty(t, summaries)
				return
			}
			require.Len(t, summaries, 1)
			assert.Equal(t, test.wantStatus, summaries[0].Status)
			assert.Equal(t, "The read-only turn did not complete.", summaries[0].Done)
			assert.NotEmpty(t, summaries[0].Limitations)
		})
	}
}

func TestTurnSummariesRebuildResultFromOlderQueueEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	_, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
	require.NoError(t, err)
	completed := time.Now()
	require.NoError(t, sqWrite("done", sqEntry{ID: "remote", PromptID: "prompt-one", Dir: dir, Text: "inspect", At: completed, Done: "headless", DoneAt: completed, Result: "Inspected the repository."}))
	b := &brain{}
	summaries, err := b.linkTurnSummaries()
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "Inspected the repository.", summaries[0].Done)
}

func TestTurnSummariesRebuildTerminalFailureFromOlderQueueEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	_, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
	require.NoError(t, err)
	completed := time.Now()
	require.NoError(t, sqWrite("done", sqEntry{ID: "remote", PromptID: "prompt-one", Dir: dir, Text: "inspect", At: completed, Done: "expired", DoneAt: completed, Result: "its approval ended before it ran"}))

	summaries, err := (&brain{}).linkTurnSummaries()
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "failed", summaries[0].Status)
	assert.Equal(t, "its approval ended before it ran", summaries[0].Limitations)
}

func BenchmarkLinkSummaryForQueueEntry(b *testing.B) {
	entry := sqEntry{PromptID: "prompt-one", Done: "expired", DoneAt: time.Now(), Result: "its approval ended before it ran"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := linkSummaryForQueueEntry(entry); !ok {
			b.Fatal("terminal remote prompt has no summary")
		}
	}
}

func TestTurnSummariesExcludeUnlinkedAndOrdinaryQueueEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	require.NoError(t, sqWrite("done", sqEntry{ID: "ordinary", Dir: dir, Text: "inspect", At: time.Now(), Done: "headless", DoneAt: time.Now(), Result: "done"}))
	require.NoError(t, sqWrite("done", sqEntry{ID: "remote", PromptID: "prompt-one", Dir: dir, Text: "inspect", At: time.Now(), Done: "headless", DoneAt: time.Now(), Result: "done"}))
	b := &brain{}
	summaries, err := b.linkTurnSummaries()
	require.NoError(t, err)
	assert.Empty(t, summaries)
}
