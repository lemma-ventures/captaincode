package captaincode

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// /frontier before a leg is a modifier - every named leg at its ceiling -
// not the pseudo-leg with "/claude …" as its text (live 2026-09-21:
// "/frontier /claude X > /grok > /codex-cli" ran frontier>grok>codex-cli,
// stage 1 asked to answer "/claude X").
func TestFrontierBeforeALegIsAModifier(t *testing.T) {
	in := "/frontier /claude what's your opinion about route A B and C > /grok > /codex-cli"
	hoisted := HoistLeading(in)
	assert.Equal(t, "/claude /frontier what's your opinion about route A B and C > /grok > /codex-cli", hoisted)
	assert.Equal(t, "claude", LeadingForced(hoisted))
	assert.True(t, MidPromptFrontier(hoisted))
	wf, err := ParseWorkflow(hoisted)
	require.NoError(t, err)
	require.Len(t, wf.Stages, 3)
	assert.Equal(t, LegClaude, wf.Stages[0].Legs[0].Leg)
	assert.Equal(t, "what's your opinion about route A B and C", wf.Stages[0].Legs[0].Prompt, "the modifier is the turn's, not the stage's text")
	assert.Equal(t, LegGrok, wf.Stages[1].Legs[0].Leg)
	assert.Equal(t, LegCodexCLI, wf.Stages[2].Legs[0].Leg)
	assert.Equal(t, EffortXHigh, EffortFor("frontier", ClassMedium), "the frontier model at the class's effort (TierEffort)")

	// Alone it is still the pseudo-leg, and its head is not "mid-prompt".
	assert.Equal(t, "/frontier what's your opinion", HoistLeading("/frontier what's your opinion"))
	assert.False(t, MidPromptFrontier("/frontier what's your opinion"))
	assert.False(t, MidPromptFrontier("read /frontier/notes.md"), "a path is not a wish")
	// Before a control word it hoists past it, as every modifier does.
	assert.Equal(t, "/team /frontier plan the migration", HoistLeading("/frontier /team plan the migration"))
	assert.Equal(t, "team", LeadingForced("/team /frontier plan the migration"))
}

// The plugin refuses a turn whose head is not one of these; every word the
// brain acts on must be here, or a real command would be refused.
func TestSlashCommandsHoldEveryWordTheBrainActsOn(t *testing.T) {
	got := map[string]bool{}
	for _, w := range SlashCommands() {
		got[w] = true
	}
	for _, w := range []string{"auto", "frontier", "quality", "q", "save", "speed", "fast", "oss", "deterministic", "noslop",
		"repeat", "parallel", "team", "wf", "workflow", "run", "wfrun", "openshell", "btw", "interrupt", "rename",
		"captain", "help", "init", "euclid", "context", "claude", "codex-cli", "grok-max"} {
		if !got[w] {
			t.Errorf("/%s is missing: the plugin would refuse it", w)
		}
	}
	for _, m := range []*regexp.Regexp{modifierWordRe, controlHeadRe} {
		src := m.String()
		for _, w := range strings.Split(src[strings.Index(src, "^/(")+3:strings.Index(src, ")\\b")], "|") {
			if !got[w] {
				t.Errorf("/%s is parsed by %s but missing from SlashCommands", w, m)
			}
		}
	}
}
