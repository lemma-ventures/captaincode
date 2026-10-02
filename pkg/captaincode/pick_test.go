package captaincode

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func est(leg Leg, a, b, minutes float64) ModelEstimate {
	return ModelEstimate{Key: ModelKey{Leg: leg}, A: a, B: b, LogMu: math.Log(minutes * 60000), LogSigma: 0.5}
}

// /quality gave claude and grok-max equal shares on a near-equal grade while
// claude took 17.5 minutes a run against 3.2 (2026-10-02). The time rule
// prefers the leg that gets an accepted answer sooner.
func TestPickByTimePrefersTheSoonerAcceptedAnswer(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	tp, ok := PickByTime([]PickCandidate{
		{Leg: LegClaude, Est: est(LegClaude, 80, 20, 17.5)},
		{Leg: LegGrokMax, Est: est(LegGrokMax, 78, 22, 3.2)},
	}, false, rng)
	require.True(t, ok)
	assert.Equal(t, LegGrokMax, tp.Leg)
	assert.False(t, tp.Explored)
	assert.Less(t, tp.Minutes[LegGrokMax], tp.Minutes[LegClaude])
}

// A waiting user is not offered a fast leg that is clearly worse.
func TestPickByTimeKeepsWeakLegsOffLiveTurns(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	tp, _ := PickByTime([]PickCandidate{
		{Leg: LegClaude, Est: est(LegClaude, 90, 10, 17.5)},
		{Leg: LegFree, Est: est(LegFree, 40, 60, 0.5)},
	}, true, rng)
	assert.Equal(t, LegClaude, tp.Leg)
	assert.NotContains(t, tp.Propensities, LegFree, "below the live margin: never drawn")
}

// Uncertain legs are drawn in proportion to their chance of being best.
func TestPickByTimeExploresUncertainLegs(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	tp, _ := PickByTime([]PickCandidate{
		{Leg: LegGLM, Est: est(LegGLM, 2, 1, 5)},
		{Leg: LegKimi, Est: est(LegKimi, 2, 1, 5)},
	}, true, rng)
	var sum float64
	for _, p := range tp.Propensities {
		sum += p
	}
	assert.InDelta(t, 1, sum, 1e-9)
	assert.Greater(t, tp.Propensities[LegGLM], 0.2)
	assert.Greater(t, tp.Propensities[LegKimi], 0.2)
}

// A filling quota window costs time; the routing mix pulls.
func TestPickByTimeWeighsQuotaAndMix(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	a := PickCandidate{Leg: LegClaude, Est: est(LegClaude, 80, 20, 5)}
	b := PickCandidate{Leg: LegGLM, Est: est(LegGLM, 80, 20, 5)}
	a.Pressure = 0.9
	tp, _ := PickByTime([]PickCandidate{a, b}, false, rng)
	assert.Equal(t, LegGLM, tp.Leg, "claude's window is nearly full")

	a.Pressure, b.MixDeficit = 0, 0
	a.MixDeficit = 0.3
	tp, _ = PickByTime([]PickCandidate{a, b}, false, rng)
	assert.Equal(t, LegClaude, tp.Leg, "the mix wants more of what claude serves")
}

func TestMixDeficitFollowsTheTarget(t *testing.T) {
	mix := SteerMix{OSS: 50, Set: true}
	recent := []Leg{LegClaude, LegClaude, LegClaude, LegClaude}
	assert.Greater(t, MixDeficit(mix, recent, LegGLM), 0.4, "no open weights recently, target 50%")
	t.Setenv("CAPTAIN_STEER", "0")
	assert.Zero(t, MixDeficit(mix, recent, LegGLM))
}
