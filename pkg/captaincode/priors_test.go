package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQualityPriorsCoverEveryLeg(t *testing.T) {
	for _, leg := range AllLegs {
		if !ServesTasks(leg) {
			assert.Zero(t, QualityPrior(leg), "a decision leg (%s) is not a worker and carries no worker prior", leg)
			continue
		}
		assert.Greater(t, QualityPrior(leg), 0.0, "leg %s must have a benchmark prior", leg)
	}
	assert.Zero(t, QualityPrior(Leg("nope")), "unknown leg has no prior")
}

func TestQualityPriorOrderingMatchesLadder(t *testing.T) {
	// The escalation ladder must never escalate DOWN in benchmark quality:
	// each rung's prior is >= the rung below (ties allowed - grok vs codex
	// split on cost, not quality).
	for i := 1; i < len(AllLegs); i++ {
		lo, hi := AllLegs[i-1], AllLegs[i]
		assert.GreaterOrEqual(t, QualityPrior(hi), QualityPrior(lo),
			"ladder inversion: %s (rung %d) has a lower prior than %s (rung %d)", hi, i, lo, i-1)
	}
	assert.Equal(t, QualityPrior(LegClaude), maxPrior(), "claude is the quality apex")
}

func maxPrior() float64 {
	m := 0.0
	for _, l := range AllLegs {
		if p := QualityPrior(l); p > m {
			m = p
		}
	}
	return m
}

func TestBlendedQuality(t *testing.T) {
	leg := LegCursor // prior 8.0

	t.Run("no local scores -> pure prior", func(t *testing.T) {
		assert.Equal(t, 8.0, BlendedQuality(leg, LegStats{}))
	})

	t.Run("few local scores nudge the prior", func(t *testing.T) {
		// 2 local scores of 4.0 against prior 8.0 (weight 5):
		// (8*5 + 4*2) / 7 = 48/7 ≈ 6.86 - moved, but not captured.
		got := BlendedQuality(leg, LegStats{Scored: 2, AvgQuality: 4.0})
		assert.InDelta(t, 6.857, got, 0.01)
	})

	t.Run("many local scores dominate the prior", func(t *testing.T) {
		// 95 local scores of 4.0: (8*5 + 4*95) / 100 = 4.2 - evidence wins.
		got := BlendedQuality(leg, LegStats{Scored: 95, AvgQuality: 4.0})
		assert.InDelta(t, 4.2, got, 0.01)
	})
}
