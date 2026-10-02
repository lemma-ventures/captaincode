package main

import (
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SCORING.md Phase 3, shadow by default: every decision records what the
// time rule would have picked, and routing does not change.
func TestTimeRuleShadowsByDefault(t *testing.T) {
	t.Setenv(captaincode.PickModeEnv, "")
	b := teamBrain()
	noDirector(t, b)
	task := "refactor the lexer"
	resp := routeBody(t, b, task, map[string]any{"prefer": "quality"})
	assert.Contains(t, resp["rationale"], "quality lane:", "the lane still decides")
	d, ok := b.ledger.DecisionFor(b.openTask(task))
	require.True(t, ok)
	require.NotNil(t, d.TimePick, "the rule's pick is on the record")
	assert.Equal(t, "shadow", d.TimePick.Mode)
	assert.Equal(t, captaincode.PathLane, d.Path)
	assert.NotEmpty(t, d.TimePick.Minutes)
}

// CAPTAIN_PICK=on: the lane chooses the menu, the rule picks from it.
func TestTimeRuleDecidesWhenOn(t *testing.T) {
	t.Setenv(captaincode.PickModeEnv, "on")
	b := teamBrain()
	noDirector(t, b)
	task := "refactor the lexer"
	resp := routeBody(t, b, task, map[string]any{"prefer": "quality"})
	assert.Contains(t, resp["rationale"], "time rule:")
	d, ok := b.ledger.DecisionFor(b.openTask(task))
	require.True(t, ok)
	assert.Equal(t, captaincode.PathTime, d.Path)
	require.NotNil(t, d.TimePick)
	assert.Equal(t, "on", d.TimePick.Mode)
	assert.Equal(t, d.TimePick.Leg, d.Chosen)
	assert.Equal(t, d.TimePick.Propensities, d.Propensities)
}

func TestTimeRuleDecidesTheFrontierLaneWhenOn(t *testing.T) {
	t.Setenv(captaincode.PickModeEnv, "on")
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegClaude: true, captaincode.LegCodexCLI: true}
	pick := b.frontierLead("prove the accumulator bound")
	require.NotNil(t, pick.Time, "the rule picked among the frontier legs")
	assert.True(t, strings.HasPrefix(pick.Reason, "frontier time rule:"), pick.Reason)
	d := frontierDecision("prove the accumulator bound", pick)
	assert.Equal(t, captaincode.PathTime, d.Path)
}
