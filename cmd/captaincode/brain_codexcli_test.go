package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every leg the brain serves must be strippable as a forced prefix - the
// regex used to be a hand-written list and silently lagged the leg roster
// (deepseek/gemini/kimi were missing: "/gemini do X" reached the worker with
// its prefix, 2026-09-09).
func TestCaptainDirectiveCoversEveryLeg(t *testing.T) {
	ids := append([]string{}, legIDs()...)
	ids = append(ids, "team", "frontier", "quality", "speed", "save")
	for _, id := range ids {
		assert.Equal(t, "do the thing", stripCaptainDirectives("/"+id+" do the thing"), "prefix /%s", id)
	}
	assert.Equal(t, "codex-cli", modelForTask("/codex-cli think hard", "free"), "detached rounds resolve the codex-cli model")
}

func TestModelsIncludeCodexCLI(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.models(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assert.Contains(t, rec.Body.String(), `"codex-cli"`)
}

func TestCaptainHelpMentionsCodexCLI(t *testing.T) {
	assert.Contains(t, captainHelp, "`/codex-cli <task>`")
	assert.Contains(t, captainHelp, "`/codex-cli`")
}

func TestPromptBudgetCodexCLIIsFrontierClass(t *testing.T) {
	t.Setenv("CAPTAIN_WRAPPER_MAX_PROMPT", "")
	assert.Equal(t, promptBudget(captaincode.LegClaude), promptBudget(captaincode.LegCodexCLI))
}

// A reroute repairs a cheaper run; it does not escalate to a frontier-class
// leg while an ordinary leg is open. With nothing else open, codex-cli beats no
// answer at all.
func TestRerouteTarget_FrontierClassOnlyWhenNothingElseIsOpen(t *testing.T) {
	b := &brain{
		ledger:  &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		allowed: map[captaincode.Leg]bool{captaincode.LegCursor: true, captaincode.LegCodexCLI: true, captaincode.LegGLM: true},
	}
	b.ledger.Cooldown(captaincode.LegCursor, 10*time.Minute)
	fb, ok := b.rerouteTarget(captaincode.LegCursor, "refactor the webhook retry loop")
	require.True(t, ok)
	assert.Equal(t, captaincode.LegGLM, fb, "an ordinary open leg wins over codex-cli")

	b.ledger.Cooldown(captaincode.LegGLM, 10*time.Minute)
	fb, ok = b.rerouteTarget(captaincode.LegCursor, "refactor the webhook retry loop")
	require.True(t, ok)
	assert.Equal(t, captaincode.LegCodexCLI, fb, "last resort: codex-cli rather than no answer")
}

func TestToolVersionPicksTheNumber(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\necho 'codex-cli 0.153.4'\n"), 0o755))
	t.Setenv("PATH", dir)
	assert.Equal(t, "0.153.4", toolVersion("codex"), "`codex --version` prints `codex-cli 0.153.4` - the first field is the name")
}

func TestUpgradeIncludesCodex(t *testing.T) {
	names := []string{}
	for _, tl := range upgradeTools {
		names = append(names, tl.name)
	}
	assert.Contains(t, strings.Join(names, ","), "codex")
}
