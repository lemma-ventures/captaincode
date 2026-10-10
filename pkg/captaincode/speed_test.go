package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func timedRuns(leg Leg, c Class, ms int64, n int) []Event {
	out := make([]Event, n)
	for i := range out {
		out[i] = Event{Leg: leg, Class: c, Outcome: "ok", Duration: ms}
	}
	return out
}

// Measured on this machine, trivial turns (2026-07..10): luna 2.7 s, step
// 16 s, Cursor's Composer 53 s.
func TestSpeedPickTakesTheFastestMeasuredLeg(t *testing.T) {
	var ev []Event
	ev = append(ev, timedRuns(LegLuna, ClassTrivial, 2_700, 21)...)
	ev = append(ev, timedRuns(LegStep, ClassTrivial, 16_000, 46)...)
	ev = append(ev, timedRuns(LegCursor, ClassTrivial, 1_000, 12)...) // even if it were fast
	ev = append(ev, timedRuns(LegCursor, ClassMedium, 60_000, 5)...)
	st := statsOf(ev)
	assert.Equal(t, SpeedStat{N: 21, MedianMs: 2_700}, st[LegLuna].Speed[ClassTrivial])

	pick, ok := SpeedPick([]Leg{LegCursor, LegStep, LegLuna}, st, ClassTrivial, 0)
	require.True(t, ok)
	assert.Equal(t, LegLuna, pick.Leg)
	assert.NotContains(t, pick.Band, LegCursor, "an agent CLI is never the fast leg on trivial work")
	assert.Contains(t, pick.Reason, "median 2.7 s over 21 runs")

	pick, _ = SpeedPick([]Leg{LegCursor, LegStep}, st, ClassMedium, 0)
	assert.Equal(t, LegStep, pick.Leg, "no medium runs of its own: step's runs of every class stand in, and beat cursor's 60 s")
}

func TestSpeedPickTimesALegItHasNotTimed(t *testing.T) {
	st := statsOf(timedRuns(LegStep, ClassTrivial, 16_000, 5))
	pick, _ := SpeedPick([]Leg{LegStep, LegGLM}, st, ClassTrivial, 0)
	assert.Equal(t, LegStep, pick.Leg)
	pick, _ = SpeedPick([]Leg{LegStep, LegGLM}, st, ClassTrivial, speedExploreEvery-1)
	assert.Equal(t, LegGLM, pick.Leg, "one turn in ten measures an untimed leg")
	assert.Contains(t, pick.Reason, "not timed here yet")
	pick, ok := SpeedPick([]Leg{LegGLM}, nil, ClassTrivial, 0)
	assert.True(t, ok)
	assert.Equal(t, LegGLM, pick.Leg, "nothing timed: try one")
	_, ok = SpeedPick([]Leg{LegClaude}, nil, ClassTrivial, 0)
	assert.False(t, ok, "only an agent CLI on trivial work: the lane passes")
}

func TestMedianMs(t *testing.T) {
	assert.Equal(t, int64(2), medianMs([]int64{3, 1, 2}))
	assert.Equal(t, int64(25), medianMs([]int64{40, 10, 20, 30}))
	assert.Zero(t, medianMs(nil))
}
