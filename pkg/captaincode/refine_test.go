package captaincode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R4 (PRIME_AGENT_NOTES): prime's /refine discipline applied to captain -
// self-improvement touches OVERLAYS only (leg notes, prior nudges), in small
// evidence-backed steps, with snapshots and rollback. Scorecards steer routing
// with numbers; refine improves the WORDS the director reasons over.

func TestRefineProposalValidation(t *testing.T) {
	good := RefineProposal{
		Notes:  map[Leg]string{LegGrok: "strong on editorial rewrites; wedges on long xAI streams - reroute-tolerant"},
		Priors: map[Leg]float64{LegCodex: 6.0},
	}
	require.NoError(t, ValidateRefineProposal(good, map[Leg]float64{LegCodex: 6.4}))

	cases := []struct {
		name string
		p    RefineProposal
		want string
	}{
		{"unknown leg", RefineProposal{Notes: map[Leg]string{"gpt5": "x"}}, "unknown leg"},
		{"note too long", RefineProposal{Notes: map[Leg]string{LegGrok: strings.Repeat("x", 300)}}, "note"},
		{"prior jump too large", RefineProposal{Priors: map[Leg]float64{LegCodex: 8.5}}, "nudge"},
		{"prior out of range", RefineProposal{Priors: map[Leg]float64{LegFree: 11}}, "range"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateRefineProposal(c.p, map[Leg]float64{LegCodex: 6.4, LegFree: 10.5})
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestApplyAndRollbackRefine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Seed an existing note so apply must snapshot it.
	require.NoError(t, ApplyRefine(RefineProposal{Notes: map[Leg]string{LegGrok: "v1 note"}}))
	require.NoError(t, ApplyRefine(RefineProposal{
		Notes:  map[Leg]string{LegGrok: "v2 note", LegCodex: "weak on prose"},
		Priors: map[Leg]float64{LegCodex: 6.0},
	}))

	notes, err := LoadLegNotes()
	require.NoError(t, err)
	assert.Equal(t, "v2 note", notes[LegGrok])
	assert.Equal(t, "weak on prose", notes[LegCodex])

	// Rollback restores the pre-apply state.
	require.NoError(t, RollbackRefine())
	notes, err = LoadLegNotes()
	require.NoError(t, err)
	assert.Equal(t, "v1 note", notes[LegGrok])
	assert.NotContains(t, notes, LegCodex)
}

// The director's menu carries the refined note next to the leg's description.
func TestPlanPromptIncludesLegNotes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, ApplyRefine(RefineProposal{Notes: map[Leg]string{LegGrok: "wedges on long xAI streams"}}))
	n, err := ReloadLegNotes()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	prompt := buildPlanPrompt("do the thing", "", "", []Leg{LegGrok, LegFree}, map[Leg]LegStats{}, nil, false)
	assert.Contains(t, prompt, "wedges on long xAI streams")
}
