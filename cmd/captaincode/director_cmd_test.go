package main

import (
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Naming an agent leg is refused - but the answer must say the helm did not
// move and what the nearest judge is ("I saw that line but it doesn't say
// codex-cli is now used as director", 2026-09-22).
func TestRefusingAnAgentDirectorNamesTheHelmAndTheNearestJudge(t *testing.T) {
	b := teamBrain()
	_, _, err := b.switchDirector("codex-cli")
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "cannot direct")
	assert.Contains(t, msg, "The helm is unchanged")
	assert.Contains(t, msg, "/captain codex`")
	assert.Contains(t, msg, "directs as gpt-6-sol.", "the twin's director model is named - the standard twin, not the fast worker")

	twin, ok := captaincode.JudgeTwin(captaincode.LegCodexCLI)
	require.True(t, ok)
	assert.Equal(t, captaincode.LegCodex, twin)
	_, ok = captaincode.JudgeTwin(captaincode.LegCursor)
	assert.False(t, ok, "Composer is served nowhere else")

	// Cursor has no twin: the refusal still says the helm is unchanged.
	_, _, err = b.switchDirector("cursor")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "The helm is unchanged")
	assert.NotContains(t, err.Error(), "Nearest judge")
}
