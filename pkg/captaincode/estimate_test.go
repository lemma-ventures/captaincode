package captaincode

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The cold-start prior sat near 0.5 while most labels were accepted, so the
// success floors excluded legs on no evidence (SCORING.md). With enough
// labels its level follows them; with few it is left alone.
func TestEstimatorPriorFollowsTheObservedRate(t *testing.T) {
	sample := func(i int, ok bool) RoutingSample {
		status := AcceptanceAccepted
		if !ok {
			status = AcceptanceRejected
		}
		task := fmt.Sprintf("task number %d about a parser", i)
		return RoutingSample{
			Decision: Decision{Task: task, Chosen: LegGLM, Class: ClassMedium},
			Outcome:  &OutcomeEvidence{TaskID: fmt.Sprint(i), Task: task, Status: status, DecidedBy: DecidedByCommit},
		}
	}
	var hist []RoutingSample
	for i := 0; i < 40; i++ {
		hist = append(hist, sample(i, i%10 != 0)) // 90% accepted
	}
	e := NewSuccessEstimator(hist)
	got := e.Estimate("an unrelated question about databases", ClassMedium, DomainCode, LegKimi, "", 0, time.Now())
	assert.True(t, got.PriorOnly)
	assert.Greater(t, got.P, EffortPrior(LegKimi, ""), "the prior rises toward the 90% observed")

	few := NewSuccessEstimator(hist[:10])
	got = few.Estimate("an unrelated question about databases", ClassMedium, DomainCode, LegKimi, "", 0, time.Now())
	assert.InDelta(t, EffortPrior(LegKimi, ""), got.P, 1e-9, "too few labels to calibrate")
}
