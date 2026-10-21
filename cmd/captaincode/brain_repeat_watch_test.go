package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live 2026-10-19: a watched /repeat sent 1,545 lines in one round and
// the TUI stopped answering keys. The watcher gets the narration, the worker's
// own words and the heartbeats that say something new.
func TestWatchFilterKeepsNarrationNotEveryToolCall(t *testing.T) {
	var f watchFilter
	assert.True(t, f.keep("⚙ Bash git status"), "before narration starts, tool lines are all there is")
	assert.True(t, f.keep("≡ 16:14:04  Checked repository status and history."))
	assert.False(t, f.keep("⚙ Bash cat README.md"), "the narration covers tool calls once it runs")
	assert.False(t, f.keep("↳ The Python suite passed 20 tests."))
	assert.True(t, f.keep("💬 Round result: all six packages landed"))
	assert.True(t, f.keep("✗ gate `make test` failed - one repair with the output"))
}

func TestWatchFilterDropsHeartbeatsThatSayNothingNew(t *testing.T) {
	var f watchFilter
	assert.True(t, f.keep("… claude still working (1h32m0s) · 863 tool calls · quiet 3m0s"))
	assert.False(t, f.keep("… claude still working (1h34m0s) · 863 tool calls · quiet 5m0s"))
	assert.False(t, f.keep("… claude still working (1h38m0s) · 863 tool calls · quiet 9m0s"))
	assert.True(t, f.keep("… claude still working (1h48m0s) · 870 tool calls · 1m ago: ⚙ Bash make"))

	var quiet watchFilter
	assert.True(t, quiet.keep("… codex still working (2m0s)"), "the first beat of a quiet run tells the user it lives")
	assert.False(t, quiet.keep("… codex still working (4m0s)"))
}

func TestWatchFilterCapsARoundAndPointsAtItsLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runs := filepath.Join(home, ".captaincode", "runs")
	require.NoError(t, os.MkdirAll(runs, 0o755))
	start := time.Now().Add(-time.Minute)
	writeLog := func(name string, at time.Time) {
		head := fmt.Sprintf("# captain worker log\nleg: claude\nstarted: %s\ncwd: /x\ntask: t\n---\n", at.Format(time.RFC3339))
		require.NoError(t, os.WriteFile(filepath.Join(runs, name), []byte(head), 0o600))
	}
	writeLog("r1019-1300aa-claude.log", start.Add(-time.Hour)) // the round before
	writeLog("r1019-1301bb-claude.log", start.Add(2*time.Second))
	writeLog("r1019-1302cc-cursor.log", start.Add(time.Minute)) // a later run

	var f watchFilter
	line := "≡ 16:14:04  " + strings.Repeat("x", 200)
	sent := 0
	for i := 0; i < 400; i++ {
		if f.keep(line) {
			sent += len(line)
		}
	}
	assert.LessOrEqual(t, sent, watchRoundBytes)
	assert.Greater(t, f.held, 200)
	assert.Equal(t, fmt.Sprintf("… %d more lines in `runs/r1019-1301bb-claude.log`", f.held), f.heldNote(start))

	f.reset()
	assert.True(t, f.keep(line), "the next round starts with a fresh allowance")
	assert.Empty(t, f.heldNote(start))
}
