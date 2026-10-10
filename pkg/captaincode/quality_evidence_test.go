package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Claude director grading Claude's work is not evidence; a judge from
// another vendor is; a grade nobody attributed counts half.
func TestQualityCountsByWhoGradedIt(t *testing.T) {
	assert.Equal(t, 1.0, qualityWeight(Event{Leg: LegClaude, Quality: 8, Judge: LegGLM}))
	assert.Equal(t, 0.0, qualityWeight(Event{Leg: LegClaude, Quality: 9, Judge: LegClaude}))
	assert.Equal(t, 0.5, qualityWeight(Event{Leg: LegClaude, Quality: 9}))
	assert.Equal(t, 0.0, qualityWeight(Event{Leg: LegClaude}))

	st := statsOf([]Event{
		{Leg: LegClaude, Outcome: "ok", Quality: 10, Judge: LegClaude}, // self-graded: out
		{Leg: LegClaude, Outcome: "ok", Quality: 6, Judge: LegGLM},
		{Leg: LegClaude, Outcome: "ok", Quality: 9},
	})[LegClaude]
	assert.InDelta(t, (6+0.5*9)/1.5, st.AvgQuality, 1e-9)
	assert.Equal(t, 2, st.Scored, "1.5 weighted scores")
}

func TestPickJudgesComeFromTwoOtherVendors(t *testing.T) {
	js := PickJudges(LegClaude, nil, 2)
	require.NotEmpty(t, js)
	seen := map[string]bool{VendorOf(LegClaude): true}
	for _, j := range js {
		assert.False(t, seen[VendorOf(j)], "%s repeats a vendor", j)
		seen[VendorOf(j)] = true
	}
}

// Acceptance sat at 96-99% for every busy leg. The rejected share of the
// informative outcomes - not silence - lowers a leg's quality.
func TestRejectedOutcomesLowerQualityAndSilenceDoesNot(t *testing.T) {
	var outs []OutcomeEvidence
	for i := 0; i < 7; i++ {
		outs = append(outs, OutcomeEvidence{TaskID: "a" + string(rune('0'+i)), Leg: LegDS4Flash, Status: AcceptanceAccepted, DecidedBy: DecidedByChecks})
	}
	for i := 0; i < 3; i++ {
		outs = append(outs, OutcomeEvidence{TaskID: "r" + string(rune('0'+i)), Leg: LegDS4Flash, Status: AcceptanceRejected, DecidedBy: DecidedByReprompt})
	}
	for i := 0; i < 20; i++ {
		outs = append(outs, OutcomeEvidence{TaskID: "s" + string(rune('a'+i)), Leg: LegDS4Flash, Status: AcceptanceAccepted, DecidedBy: DecidedBySilence})
	}
	st := map[Leg]LegStats{}
	foldOutcomes(st, outs)
	assert.Equal(t, 10, st[LegDS4Flash].Decided, "silence is not evidence")
	assert.Equal(t, 3, st[LegDS4Flash].Rejected)
	assert.InDelta(t, 1.2, OutcomePenalty(st[LegDS4Flash]), 1e-9)
	assert.InDelta(t, QualityPrior(LegDS4Flash)-1.2, BlendedQuality(LegDS4Flash, st[LegDS4Flash]), 1e-9)
	assert.Zero(t, OutcomePenalty(LegStats{Decided: 4, Rejected: 4}), "too few outcomes to judge")

	assert.False(t, RoutingSample{Outcome: &OutcomeEvidence{Status: AcceptanceAccepted, DecidedBy: DecidedBySilence}}.Labeled())
	assert.True(t, RoutingSample{Outcome: &OutcomeEvidence{Status: AcceptanceAccepted, DecidedBy: DecidedByCommit}}.Labeled())
}

// codex-cli carried a 9.3 prior and averaged 7.2 over the judges' scores.
func TestRefitMovesAPriorPartWayTowardTheJudges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "priors.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"codex-cli": {"all": 9.3, "code": 9.2}}`), 0o644))
	_, err := LoadPriorOverridesFrom(path)
	require.NoError(t, err)
	t.Cleanup(func() { applyRegistry() })

	changes := ProposeRefit(map[Leg]LegStats{
		LegCodexCLI: {Scored: 34, AvgQuality: 7.2},
		LegGLM:      {Scored: 5, AvgQuality: 9.9}, // too little evidence
	})
	require.Len(t, changes, 1)
	assert.Equal(t, PriorChange{Leg: LegCodexCLI, From: 9.3, To: 8.8, Measured: 7.2, Scored: 34}, changes[0], "a step of at most 0.5")

	now := time.Now()
	assert.True(t, RefitDue(path, now))
	require.NoError(t, ApplyRefit(path, changes, now))
	assert.Equal(t, 8.8, QualityPrior(LegCodexCLI))
	var raw map[string]map[string]float64
	data, _ := os.ReadFile(path)
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, map[string]float64{"all": 8.8, "code": 8.7}, raw["codex-cli"], "every domain moves by the same step")
	assert.False(t, RefitDue(path, now.Add(time.Hour)), "weekly")
	assert.True(t, RefitDue(path, now.Add(8*24*time.Hour)))
}
