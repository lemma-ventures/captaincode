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
	captaincode.SetDirector(captaincode.LegGrok)
	// codex-cli lost its twin when codex moved to the Codex CLI (2026-11-10):
	// no OpenAI leg judges without tools, so it is refused like cursor.
	for _, agent := range []captaincode.Leg{captaincode.LegCodexCLI, captaincode.LegCursor, captaincode.LegCodex} {
		_, ok := captaincode.JudgeTwin(agent)
		assert.False(t, ok, "%s has no judge twin", agent)
		_, _, err := b.switchDirector(string(agent))
		require.Error(t, err, agent)
		assert.Contains(t, err.Error(), "cannot direct")
		assert.Contains(t, err.Error(), "The helm is unchanged: grok")
	}
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
