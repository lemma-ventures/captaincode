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
