package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"encoding/json"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func feedFor(t *testing.T, b *brain, dir string) []activity {
	t.Helper()
	rec := httptest.NewRecorder()
	b.activityFeed(rec, httptest.NewRequest("GET", "/v1/activity?cwd="+dir, nil))
	var out struct {
		Activity []activity `json:"activity"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.Activity
}

// A busy folder no longer evicts another folder's runs from the ring.
func TestBusyFolderKeepsOtherFoldersRuns(t *testing.T) {
	b := teamBrain()
	b.pushActivity(activity{Dir: "/w/proj", Kind: "run", Leg: "claude", Text: "define the next items"})
	for i := 0; i < 200; i++ {
		b.pushActivity(activity{Dir: "/w/other", Kind: "run", Leg: "grok", Text: fmt.Sprintf("task %d", i)})
	}
	got := feedFor(t, b, "/w/proj")
	require.Len(t, got, 1)
	assert.Equal(t, "define the next items", got[0].Text)
	assert.Len(t, feedFor(t, b, "/w/other"), actsPerFolder)
}

// 34 failed decision records read as one line, and the user's prompt is
// still among the newest lines.
func TestDecisionRecordBurstFoldsIntoOneLine(t *testing.T) {
	b := teamBrain()
	dir := "/w/proj"
	b.pushActivity(activity{Dir: dir, Kind: "run", Leg: "claude", Text: "define what should be the next items"})
	b.pushActivity(activity{Dir: dir, Kind: "done", Leg: "claude", Text: "four items, two done"})
	for i := 0; i < 34; i++ {
		b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "codex", Text: "time rule: codex"})
		b.pushActivity(activity{Dir: dir, Kind: "run", Leg: "codex", Text: "Record the user's 1 decision for proj. 1. D-1"})
		b.pushActivity(activity{Dir: dir, Kind: "done", Leg: "codex", Text: "error: Token refresh failed: 401"})
	}
	got := feedFor(t, b, dir)
	require.GreaterOrEqual(t, len(got), 3)
	assert.Equal(t, "decision records", got[0].Label)
	assert.Contains(t, got[0].Text, "34 decision records, 34 failed")
	assert.Equal(t, "four items, two done", got[1].Text, "the prompt's answer is the next line, not off screen")
	assert.Equal(t, "define what should be the next items", got[2].Text)

	// One record run alone stays as it is.
	b2 := teamBrain()
	b2.pushActivity(activity{Dir: dir, Kind: "run", Leg: "codex", Text: "Record the user's 1 decision for proj."})
	b2.pushActivity(activity{Dir: dir, Kind: "done", Leg: "codex", Text: "recorded"})
	got = feedFor(t, b2, dir)
	require.Len(t, got, 2)
	assert.Equal(t, "recorded", got[0].Text)
}

func TestWorktreeRunsShowInTheirFoldersTUI(t *testing.T) {
	assert.True(t, actInFolder("/w/proj/.worktrees/topic-a", "/w/proj"))
	assert.True(t, actInFolder("", "/w/proj"), "machine-wide lines show everywhere")
	assert.False(t, actInFolder("/w/proj-ui", "/w/proj"))
	assert.False(t, actInFolder("/w/other", "/w/proj"))
}

// After a restart, Last Runs is rebuilt from the worker logs, filed under
// the TUI that started each run.
func TestRestartRestoresLastRunsFromWorkerLogs(t *testing.T) {
	logs := t.TempDir()
	write := func(name, body string, age time.Duration) {
		p := filepath.Join(logs, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		when := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(p, when, when))
	}
	start := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	write("r1110-1919f8-frontier.log", "# captain worker log\nleg: frontier\nmodel: claude-opus-frontier\nstarted: "+start+
		"\ncwd: /w/proj\ntask: define what should be the next items\n---\nthe narrative\n---\ndone in 1h44m25s (4124 chars)\n", 90*time.Minute)
	for i := 0; i < 5; i++ {
		recStart := time.Now().Add(-time.Duration(10-i) * time.Minute).Format(time.RFC3339)
		write(fmt.Sprintf("r1110-21%02d00-codex.log", i), "# captain worker log\nleg: codex\nmodel: gpt-6.1-sol\nstarted: "+recStart+
			"\ncwd: /w/proj/.worktrees/topic-a\norigin: /w/proj\ntask: Record the user's 1 decision for topic-a.\n---\n\n---\nfailed in 0s (0 chars)\nerror: opencode error: Token refresh failed: 401\n", time.Duration(10-i)*time.Minute)
	}
	write("r1110-220000-grok.log", "# captain worker log\nleg: grok\nmodel: grok-build-0.1\nstarted: "+start+"\ncwd: /w/other\ntask: elsewhere\n---\n", time.Minute)
	write("notes.txt", "not a log", time.Minute)

	b := teamBrain()
	b.restoreActivity(logs)
	got := feedFor(t, b, "/w/proj")
	require.Len(t, got, 3, "the folded records, then the prompt's done and run lines")
	// The frontier run started 2h ago and finished 16 minutes ago; the
	// records ran in the last 10 minutes.
	assert.Contains(t, got[0].Text, "5 decision records, 5 failed")
	assert.Equal(t, "done", got[1].Kind)
	assert.Equal(t, int64((time.Hour + 44*time.Minute + 25*time.Second).Milliseconds()), got[1].Ms)
	assert.Equal(t, "define what should be the next items", got[2].Text)
	assert.Equal(t, "frontier", got[2].Leg)
	assert.Equal(t, "claude-cli:opus", got[2].Label, "the frontier pseudo-leg reads as claude-cli")

	other := feedFor(t, b, "/w/other")
	require.Len(t, other, 1, "a run cut off before its footer keeps its run line")
	assert.Equal(t, "elsewhere", other[0].Text)

	t.Setenv("CAPTAIN_RESTORE_ACTIVITY", "0")
	b2 := teamBrain()
	b2.restoreActivity(logs)
	assert.Empty(t, b2.acts)
}
