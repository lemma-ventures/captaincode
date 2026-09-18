package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// "Have claude, grok and codex review these examples" got planned as
// cursor+grok+glm: the director excluded itself (claude) and swapped codex on
// scorecard grounds (live 2026-07-30). When the user names the reviewers, the
// names are the requirement - the router's opinion about quality is not.
func TestNamedAssignees(t *testing.T) {
	cases := []struct {
		name string
		task string
		want []Leg
	}{
		{"three named reviewers", "Have claude, grok and codex review these examples",
			[]Leg{LegClaude, LegGrok, LegCodex}},
		{"ask form", "ask grok and claude to critique the abstract",
			[]Leg{LegGrok, LegClaude}},
		{"order is preserved", "let codex and claude and grok each red-team it",
			[]Leg{LegCodex, LegClaude, LegGrok}},
		{"free counts alongside a distinctive leg", "have free and claude review this",
			[]Leg{LegFree, LegClaude}},

		// Not assignments - models as subject matter, or a single mention.
		{"subject matter", "compare claude and grok pricing for me", nil},
		{"single leg mention", "have claude review this", nil},
		{"no cue", "claude, grok, codex", nil},
		{"free alone is an English word", "have free and open access to review it", nil},
		{"plain prose", "review the abstract and tighten the wording", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, NamedAssignees(c.task))
		})
	}
}

func TestNamedAssigneesDeduplicates(t *testing.T) {
	assert.Equal(t, []Leg{LegClaude, LegGrok},
		NamedAssignees("have claude and grok review it, then claude again"))
}

// "Have grok, codex and THEN claude review X" is a sequence, not a fan-out:
// /team has one parallel stage and cannot express it - the director claimed to
// have "sequenced cheap→deep" while running all three at once (live 2026-07-30).
func TestNamedStages(t *testing.T) {
	cases := []struct {
		name string
		task string
		want [][]Leg
	}{
		{"comma group then one",
			"Have grok, codex and then claude Review the research paper",
			[][]Leg{{LegGrok, LegCodex}, {LegClaude}}},
		{"fully sequential",
			"have grok then codex then claude review it",
			[][]Leg{{LegGrok}, {LegCodex}, {LegClaude}}},
		{"followed by",
			"ask grok and codex to review, followed by claude",
			[][]Leg{{LegGrok, LegCodex}, {LegClaude}}},
		{"after that",
			"have grok review this, after that claude checks it",
			[][]Leg{{LegGrok}, {LegClaude}}},
		{"finally",
			"let grok and codex draft it and finally claude audits it",
			[][]Leg{{LegGrok, LegCodex}, {LegClaude}}},
		{"no sequence cue is one parallel stage",
			"have claude, grok and codex review this",
			[][]Leg{{LegClaude, LegGrok, LegCodex}}},
		{"not an assignment", "compare claude and grok pricing", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, NamedStages(c.task))
		})
	}
}

// The flat list stays consistent with the staged one.
func TestNamedAssigneesFlattensStages(t *testing.T) {
	assert.Equal(t, []Leg{LegGrok, LegCodex, LegClaude},
		NamedAssignees("Have grok, codex and then claude Review the paper"))
}

func TestWorkflowFromNamedStages(t *testing.T) {
	wf, ok := WorkflowFromNamedStages(
		[][]Leg{{LegGrok, LegCodex}, {LegClaude}}, "review the paper for soundness")
	require.True(t, ok)
	require.Len(t, wf.Stages, 2)
	assert.Len(t, wf.Stages[0].Legs, 2)
	assert.Equal(t, "review the paper for soundness", wf.Stages[0].Legs[0].Prompt)
	assert.Equal(t, LegClaude, wf.Stages[1].Legs[0].Leg)
	assert.Equal(t, 3, wf.Runs())

	// Over the limits ⇒ refuse, so the caller falls back instead of truncating.
	_, ok = WorkflowFromNamedStages([][]Leg{{LegGrok}, {LegCodex}, {LegClaude}, {LegFree}, {LegGLM}}, "x")
	assert.False(t, ok)
}

// A pasted issue that MENTIONS legs is not an assignment (live 2026-09-16:
// "/quality address this issue … the director is listed as Grok … from
// what claude has just told me" ran as a team of claude and grok - a cue
// word anywhere in a long text plus two names anywhere was enough). The
// hand-off must sit next to the names.
func TestMentionsInPastedTextAreNotAssignments(t *testing.T) {
	pasted := `address this issue  When i run the doctor, the brain is starting, but the director is listed as Grok, i dont have Grok installed, so i guess this is going to have an impact You can do /captain claude to select claude as captain.
But list the issue not normal a non available leg is listed if not present on your computer
After it's interesting to see if your captain is properly rerouted if grok is not present
from what claude has just told me, becuase grok is listed and i dont have it, captaincode will do manager planning everytime, fail to reach grok, and fall back to the dumb heuristic ladder each time. , grok should not be listed if leg CLI is not present on local machine or maybe routed to openrouter where grok might be available?.`
	assert.Nil(t, NamedAssignees(pasted))
	assert.Nil(t, NamedStages(pasted))

	cases := []struct {
		name string
		task string
		want []Leg
	}{
		{"cue in the previous sentence does not carry", "Check the doctor output. claude and grok are listed as directors", nil},
		{"negated hand-off", "i dont have grok and claude installed", nil},
		{"mentioned then assigned", "the director is grok today. have claude and grok review the plan", []Leg{LegClaude, LegGrok}},
		{"task verb after the group", "claude and grok should review this design", []Leg{LegClaude, LegGrok}},
		{"use form", "use grok and codex for this one", []Leg{LegGrok, LegCodex}},
		{"far-away cue", "please use the new parser everywhere; the old one confused claude and grok", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, NamedAssignees(c.task))
		})
	}
}
