package captaincode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCalibrate_NoEvents(t *testing.T) {
	now := time.Now()
	c := Calibrate(LegClaude, DomainCode, nil, now)
	assert.Equal(t, QualityPriorFor(LegClaude, DomainCode), c.Quality, "no events → pure prior")
	assert.Equal(t, c.Quality, c.CILower, "no evidence → CI collapses to point")
	assert.Equal(t, c.Quality, c.CIUpper)
	assert.True(t, c.Stale, "no evidence is stale")
	assert.Equal(t, 0, c.SampleSize)
}

func TestCalibrate_SingleRun(t *testing.T) {
	now := time.Now()
	events := []Event{
		{Leg: LegGrok, At: now.Add(-1 * time.Hour), Outcome: "ok", Quality: 8.0, Task: "fix typo", Domain: "code"},
	}
	c := Calibrate(LegGrok, DomainCode, events, now)
	assert.Equal(t, 1, c.SampleSize)
	assert.True(t, c.CILower < c.Quality, "CI has width with one observation")
	assert.True(t, c.CIUpper > c.Quality)
	assert.True(t, c.EffectiveN > 0.9, "fresh observation has near-full weight")
	assert.True(t, c.Stale, "one observation is still prior-dominated (eff < stale threshold)")
}

func TestCalibrate_TimeDecay(t *testing.T) {
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour) // 30 days ago
	fresh := now.Add(-1 * time.Hour)
	events := []Event{
		{Leg: LegGrok, At: old, Outcome: "ok", Quality: 10.0, Task: "old", Domain: "code"},
		{Leg: LegGrok, At: fresh, Outcome: "ok", Quality: 5.0, Task: "new", Domain: "code"},
	}
	c := Calibrate(LegGrok, DomainCode, events, now)
	assert.Equal(t, 2, c.SampleSize)
	assert.True(t, c.Quality < 7.5, "fresh q=5 should pull below the average of 5 and 10 (decay)")
	assert.True(t, c.EffectiveN < 2, "old observation decays")
}

func TestCalibrate_Reliability(t *testing.T) {
	now := time.Now()
	events := []Event{
		{Leg: LegGrok, At: now.Add(-3 * time.Hour), Outcome: "ok", Quality: 8.0, Task: "t1"},
		{Leg: LegGrok, At: now.Add(-2 * time.Hour), Outcome: "fail", Error: "provider down", Task: "t2"},
		{Leg: LegGrok, At: now.Add(-1 * time.Hour), Outcome: "ok", Quality: 7.0, Task: "t3"},
	}
	c := Calibrate(LegGrok, DomainCode, events, now)
	assert.Equal(t, 3, c.ReliabilityN, "all attempts counted")
	assert.True(t, c.Reliability > 0.5, "2/3 success → reliability > 0.5")
	assert.True(t, c.Reliability < 0.9, "but not perfect")
	assert.NotEqual(t, "?", c.ReliabilityCI, "Wilson interval computed")
}

func TestCalibrate_ReliabilityZeroFailures(t *testing.T) {
	now := time.Now()
	events := []Event{
		{Leg: LegClaude, At: now.Add(-1 * time.Hour), Outcome: "ok", Quality: 9.0, Task: "t1"},
		{Leg: LegClaude, At: now.Add(-30 * time.Minute), Outcome: "ok", Quality: 8.5, Task: "t2"},
	}
	c := Calibrate(LegClaude, DomainCode, events, now)
	assert.Equal(t, 2, c.ReliabilityN)
	assert.InDelta(t, 1.0, c.Reliability, 0.01, "no failures → reliability ≈ 1")
}

func TestCalibrate_StaleDetection(t *testing.T) {
	now := time.Now()
	old := now.Add(-60 * 24 * time.Hour) // 60 days ago
	events := []Event{
		{Leg: LegGrok, At: old, Outcome: "ok", Quality: 8.0, Task: "ancient"},
	}
	c := Calibrate(LegGrok, DomainCode, events, now)
	assert.True(t, c.Stale, "very old single observation is stale")
	assert.True(t, c.EffectiveN < 1, "heavily decayed")
}

func TestCalibrate_DomainSpecific(t *testing.T) {
	now := time.Now()
	events := []Event{
		{Leg: LegGrok, At: now.Add(-1 * time.Hour), Outcome: "ok", Quality: 9.0, Task: "write article", Domain: "editorial"},
		{Leg: LegGrok, At: now.Add(-30 * time.Minute), Outcome: "ok", Quality: 5.0, Task: "fix bug", Domain: "code"},
	}
	codeCal := Calibrate(LegGrok, DomainCode, events, now)
	editorialCal := Calibrate(LegGrok, DomainEditorial, events, now)
	assert.True(t, editorialCal.Quality > codeCal.Quality, "editorial quality > code quality for this leg")
}

func TestCalibrate_AgeField(t *testing.T) {
	now := time.Now()
	events := []Event{
		{Leg: LegGrok, At: now.Add(-2 * time.Hour), Outcome: "ok", Quality: 7.0, Task: "t1"},
	}
	c := Calibrate(LegGrok, DomainCode, events, now)
	assert.True(t, c.Age > 1*time.Hour, "age reports time since last run")
	assert.True(t, c.Age < 3*time.Hour)
}

func TestCalibrate_FormatHasCI(t *testing.T) {
	now := time.Now()
	events := []Event{
		{Leg: LegGrok, At: now.Add(-1 * time.Hour), Outcome: "ok", Quality: 8.0, Task: "t1"},
		{Leg: LegGrok, At: now.Add(-30 * time.Minute), Outcome: "ok", Quality: 7.5, Task: "t2"},
	}
	c := Calibrate(LegGrok, DomainCode, events, now)
	s := c.Format()
	assert.Contains(t, s, "[", "format includes CI bounds")
	assert.Contains(t, s, "n=2", "format includes sample size")
	assert.Contains(t, s, "reliability", "format includes reliability")
}

func TestCalibrate_FormatNoEvidence(t *testing.T) {
	now := time.Now()
	c := Calibrate(LegGrok, DomainCode, nil, now)
	s := c.Format()
	assert.Contains(t, s, "prior")
	assert.Contains(t, s, "no local evidence")
}

func TestDecayWeight(t *testing.T) {
	now := time.Now()
	assert.InDelta(t, 1.0, decayWeight(now, now, 14), 0.001, "fresh → full weight")
	assert.InDelta(t, 0.5, decayWeight(now.Add(-14*24*time.Hour), now, 14), 0.01, "one half-life → half weight")
	assert.InDelta(t, 0.25, decayWeight(now.Add(-28*24*time.Hour), now, 14), 0.01, "two half-lives → quarter weight")
	assert.InDelta(t, 0.0, decayWeight(time.Time{}, now, 14), 0.001, "zero time → zero weight")
}

func TestWilsonInterval(t *testing.T) {
	assert.Equal(t, "?", wilsonInterval(0.5, 0), "n=0 → unknown")
	s := wilsonInterval(1.0, 10)
	assert.Contains(t, s, "–", "interval has a range")
	s2 := wilsonInterval(0.0, 5)
	assert.Contains(t, s2, "–", "all-failures interval has a range")
}
