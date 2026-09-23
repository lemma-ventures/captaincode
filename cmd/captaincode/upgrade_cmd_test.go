package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `captain upgrade` - one command to bring every agent CLI current (user ask,
// 2026-08-07). Each tool's NATIVE updater does the work; captain orchestrates,
// reports versions before/after, and only restarts services when idle.

// fakeTool installs a fake agent CLI that reports a version, bumps it when its
// updater runs, and records every invocation.
func fakeTool(t *testing.T, dir, name, updateVerb string) string {
	t.Helper()
	callLog := filepath.Join(dir, name+".calls")
	verFile := filepath.Join(dir, name+".version")
	require.NoError(t, os.WriteFile(verFile, []byte("1.0.0"), 0o644))
	script := `#!/bin/sh
echo "$@" >> ` + callLog + `
case "$1" in
` + updateVerb + `) echo "2.0.0" > ` + verFile + `; echo "updated ` + name + `";;
--version) cat ` + verFile + `;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755))
	return callLog
}

func setupTools(t *testing.T) (string, map[string]string) {
	return setupToolsImpl(t)
}

func setupToolsImpl(t *testing.T) (dir string, calls map[string]string) {
	t.Helper()
	dir = t.TempDir()
	calls = map[string]string{
		"claude":       fakeTool(t, dir, "claude", "update"),
		"cursor-agent": fakeTool(t, dir, "cursor-agent", "update"),
		"opencode":     fakeTool(t, dir, "opencode", "upgrade"),
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTAIN_SRC", t.TempDir()) // no captain source → self-rebuild skipped
	return dir, calls
}

func TestUpgradeRunsEveryNativeUpdater(t *testing.T) {
	_, calls := setupTools(t)
	var sb strings.Builder
	restarted := false
	runUpgrade(&sb, upgradeOpts{restart: func() { restarted = true }, idle: func() bool { return true }})
	out := sb.String()

	for name, log := range calls {
		body, err := os.ReadFile(log)
		require.NoError(t, err, "%s must have been invoked", name)
		s := string(body)
		assert.Contains(t, s, "--version", "%s version checked", name)
		if name == "opencode" {
			assert.Contains(t, s, "upgrade")
		} else {
			assert.Contains(t, s, "update")
		}
	}
	assert.Contains(t, out, "claude")
	assert.Contains(t, out, "1.0.0 → 2.0.0", "before → after versions reported")
	assert.Contains(t, out, "source not found", "self-rebuild is skipped honestly")
	assert.True(t, restarted, "idle → services restarted so upgrades take effect")
}

func TestUpgradeCheckOnlyTouchesNothing(t *testing.T) {
	_, calls := setupTools(t)
	var sb strings.Builder
	restarted := false
	runUpgrade(&sb, upgradeOpts{checkOnly: true, restart: func() { restarted = true }, idle: func() bool { return true }})

	for name, log := range calls {
		body, _ := os.ReadFile(log)
		assert.NotContains(t, string(body), "update", "%s must not be updated in --check", name)
		assert.NotContains(t, string(body), "upgrade", "%s must not be updated in --check", name)
	}
	assert.Contains(t, sb.String(), "1.0.0")
	assert.False(t, restarted)
}

func TestUpgradeSkipsMissingToolsAndBusyRestart(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "claude", "update") // only claude exists
	t.Setenv("PATH", dir)                // cursor-agent/opencode absent
	t.Setenv("CAPTAIN_SRC", t.TempDir())
	var sb strings.Builder
	restarted := false
	runUpgrade(&sb, upgradeOpts{restart: func() { restarted = true }, idle: func() bool { return false }})
	out := sb.String()
	assert.Contains(t, out, "cursor-agent: not installed")
	assert.Contains(t, out, "updated claude")
	assert.Contains(t, out, "busy", "a running turn must never be killed by an upgrade")
	assert.False(t, restarted)
}

// grok and codex are model pins, not binaries - the inventory must say so
// instead of leaving a hole in the list (user question, 2026-08-07).
func TestUpgradeListsApiLegPins(t *testing.T) {
	setupTools(t)
	var sb strings.Builder
	runUpgrade(&sb, upgradeOpts{checkOnly: true, restart: func() {}, idle: func() bool { return true }})
	out := sb.String()
	assert.Contains(t, out, "grok:")
	assert.Contains(t, out, "xai/grok-build-0.1")
	assert.Contains(t, out, "codex:")
	assert.Contains(t, out, "openai/gpt-6-sol-fast")
	assert.Contains(t, out, "luna:")
	assert.Contains(t, out, "openai/gpt-6-luna")
	assert.Contains(t, out, "no binary to update")
}
