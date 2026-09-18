package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The brain narrates a run on the reasoning channel ("captain · grok": tool
// lines, outcomes, the worker's own words). In the TUI's "hide" thinking mode
// that block folds to one clickable line and opens on click - the collapsed
// view a busy TUI should show, details one click away (2026-09-13). init
// seeds that mode explicitly so a fresh store has a deliberate value; the
// user's own choice, either way, is left alone.

func TestEnsureThinkingShownSeedsAFreshStore(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	changed, err := EnsureThinkingShown()
	require.NoError(t, err)
	assert.True(t, changed)

	body, err := os.ReadFile(TuiKVPath())
	require.NoError(t, err)
	var kv map[string]any
	require.NoError(t, json.Unmarshal(body, &kv))
	assert.Equal(t, "hide", kv["thinking_mode"], "collapsed by default; `/thinking` opens it")
}

func TestEnsureThinkingShownRespectsAnExplicitChoice(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Dir(TuiKVPath()), 0o755))
	// The user toggled it off on purpose; init must not fight them, and must
	// not drop the other keys in the store.
	require.NoError(t, os.WriteFile(TuiKVPath(), []byte(`{"thinking_mode":"show","sidebar":"open"}`), 0o644))

	changed, err := EnsureThinkingShown()
	require.NoError(t, err)
	assert.False(t, changed)

	body, _ := os.ReadFile(TuiKVPath())
	var kv map[string]any
	require.NoError(t, json.Unmarshal(body, &kv))
	assert.Equal(t, "show", kv["thinking_mode"], "the user's explicit choice stands")
	assert.Equal(t, "open", kv["sidebar"])
}

// permissions.blockReadsOutsideWorkingDirectories in Claude Code's user
// settings makes `claude -p` refuse every shell command with a $expansion,
// redirect or computed path ("Contains expansion; under the read block …
// refused" on an arc worker, 2026-09-13). init removes it; a settings file
// without it, or without a permissions block, is left exactly as it is.
func TestEnsureClaudeWorkerPermissionsRemovesTheReadBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	p := filepath.Join(home, ".claude", "settings.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"model":"opus","permissions":{"blockReadsOutsideWorkingDirectories":true,"allow":["Bash(git *)"]}}`), 0o644))
	changed, note, err := EnsureClaudeWorkerPermissions()
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Contains(t, note, "blockReadsOutsideWorkingDirectories")
	var cfg map[string]any
	body, _ := os.ReadFile(p)
	require.NoError(t, json.Unmarshal(body, &cfg))
	assert.Equal(t, "opus", cfg["model"], "other keys stay")
	perms := cfg["permissions"].(map[string]any)
	_, has := perms["blockReadsOutsideWorkingDirectories"]
	assert.False(t, has)
	assert.Equal(t, []any{"Bash(git *)"}, perms["allow"], "the user's allow rules stay")

	changed, _, err = EnsureClaudeWorkerPermissions()
	require.NoError(t, err)
	assert.False(t, changed, "idempotent")
	require.NoError(t, os.Remove(p))
	changed, _, err = EnsureClaudeWorkerPermissions()
	require.NoError(t, err)
	assert.False(t, changed, "no settings file: nothing to do")
}

func TestClaudeDirArgsWidenTheWorkersReach(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_WORKSPACE_ROOT", "")
	repo := filepath.Join(home, "Gits", "arc")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	args := ClaudeDirArgs(repo)
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "--add-dir "+filepath.Join(home, "Gits"), "sibling repos are reachable")
	assert.Contains(t, joined, "--add-dir "+os.TempDir(), "scratch dirs are reachable")
	assert.Nil(t, ClaudeDirArgs(""), "the director runs without a workspace")
}
