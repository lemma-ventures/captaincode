package main

import (
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Naming an agent leg with a judge on the same credential pins that judge and
// says so: refusing "/captain codex-cli" and naming /captain codex only made
// the user type it again (2026-10-13). The answer still says codex-cli is not
// the one directing ("it doesn't say codex-cli is now used as director",
// 2026-09-22).
func TestAnAgentDirectorPinsItsJudgeTwinAndSaysSo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	defer captaincode.SetDirector(captaincode.LegGrok)
	b := teamBrain()
	pick, mode, err := b.switchDirector("codex-cli")
	require.NoError(t, err)
	assert.Equal(t, "fixed", mode)
	assert.Equal(t, captaincode.LegCodex, pick.Leg)
	assert.Equal(t, captaincode.LegCodex, b.effectiveDirector())
	assert.Contains(t, pick.Reason, "codex-cli runs as an agent with tools and cannot direct")
	assert.Contains(t, pick.Reason, "directs as gpt-6.1-sol", "the twin's director model is named - the standard twin, not the fast worker")

	twin, ok := captaincode.JudgeTwin(captaincode.LegCodexCLI)
	require.True(t, ok)
	assert.Equal(t, captaincode.LegCodex, twin)
	_, ok = captaincode.JudgeTwin(captaincode.LegCursor)
	assert.False(t, ok, "Composer is served nowhere else")

	// Cursor has no twin: refused, and the refusal says the helm is unchanged.
	_, _, err = b.switchDirector("cursor")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot direct")
	assert.Contains(t, err.Error(), "The helm is unchanged: codex")
}

// The sidebar prints harness names (grok-xai, claude-cli, cursor-cli), so
// "/captain <what the sidebar says>" works: it pins the best-ranked judge
// under that name, and an agent-only name takes the agent-leg path.
func TestASidebarNameReachesItsLeg(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	defer captaincode.SetDirector(captaincode.LegGrok)
	b := teamBrain()

	pick, _, err := b.switchDirector("grok-xai")
	require.NoError(t, err)
	assert.Equal(t, captaincode.LegGrok, pick.Leg, "grok and grok-max direct as one model; the first in ladder order is the candidate")
	assert.Contains(t, pick.Reason, "grok-xai is the sidebar name of grok")

	pick, _, err = b.switchDirector("Claude-CLI")
	require.NoError(t, err)
	assert.Equal(t, captaincode.LegClaude, pick.Leg)

	_, _, err = b.switchDirector("cursor-cli")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cursor cannot direct")

	_, _, err = b.switchDirector("grok-nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is neither a leg")
}
