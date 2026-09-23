package captaincode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultSteerDoesNotBias(t *testing.T) {
	assert.InDelta(t, 100, DefaultSteer().sum(), 0.01)
	assert.Equal(t, 0.0, DefaultSteer().Deterministic)
	rows := []Scored{{Leg: LegClaude, Value: 0.5}, {Leg: LegGLM, Value: 0.4}}
	got := ApplySteer(rows, SteerMix{}, nil)
	assert.Equal(t, LegClaude, got[0].Leg)
	assert.Equal(t, 0.5, got[0].Value, "an unset mix does not move the score")
}

func TestMoreOssTwentyPercentIsRelativeAndFunded(t *testing.T) {
	next, note, err := SteerMix{}.Adjust("oss", true, true, 20)
	require.NoError(t, err)
	assert.InDelta(t, 24, next.OSS, 0.05)
	assert.InDelta(t, 19, next.Frontier, 0.05)
	assert.InDelta(t, 19, next.Quality, 0.05)
	assert.InDelta(t, 19, next.Cheap, 0.05)
	assert.InDelta(t, 19, next.Fast, 0.05)
	assert.InDelta(t, 0, next.Deterministic, 0.05)
	assert.InDelta(t, 100, next.sum(), 0.2)
	assert.True(t, next.Set)
	assert.Contains(t, note, "20%")
	assert.Contains(t, note, "24%")
}

func TestMoreFromZeroIsAbsoluteAndBareMoreStepsFive(t *testing.T) {
	next, _, err := SteerMix{}.Adjust("deterministic", true, true, 20)
	require.NoError(t, err)
	assert.InDelta(t, 20, next.Deterministic, 0.05, "20% of zero would stay zero; from zero it is 20 points")
	assert.InDelta(t, 100, next.sum(), 0.2)

	step, _, err := SteerMix{}.Adjust("oss", true, false, 0)
	require.NoError(t, err)
	assert.InDelta(t, 25, step.OSS, 0.05)
	again, _, err := step.Adjust("oss", true, false, 0)
	require.NoError(t, err)
	assert.InDelta(t, 30, again.OSS, 0.05, "repeating more compounds")
	assert.InDelta(t, 100, again.sum(), 0.2)
}

func TestLessGivesThePointsBack(t *testing.T) {
	next, _, err := SteerMix{}.Adjust("cheap", false, false, 0)
	require.NoError(t, err)
	assert.InDelta(t, 15, next.Cheap, 0.05)
	assert.InDelta(t, 100, next.sum(), 0.2)
	assert.Greater(t, next.Frontier, 20.0)
}

func TestAssignKeepsNamedNumbersAndScalesTheRest(t *testing.T) {
	named := map[string]float64{
		"oss": 20, "deterministic": 10, "frontier": 30, "quality": 20, "cheap": 10, "fast": 10,
	}
	next, _, err := SteerMix{}.Assign(named)
	require.NoError(t, err)
	assert.InDelta(t, 30, next.Frontier, 0.05)
	assert.InDelta(t, 20, next.Quality, 0.05)
	assert.InDelta(t, 10, next.Cheap, 0.05)
	assert.InDelta(t, 10, next.Fast, 0.05)
	assert.InDelta(t, 20, next.OSS, 0.05)
	assert.InDelta(t, 10, next.Deterministic, 0.05)
	assert.InDelta(t, 100, next.sum(), 0.2)

	partial, note, err := SteerMix{}.Assign(map[string]float64{"oss": 40})
	require.NoError(t, err)
	assert.InDelta(t, 40, partial.OSS, 0.05, "the number typed is the number saved")
	assert.InDelta(t, 100, partial.sum(), 0.2)
	assert.Contains(t, note, "oss")

	over, note, err := SteerMix{}.Assign(map[string]float64{"oss": 80, "frontier": 40})
	require.NoError(t, err)
	assert.InDelta(t, 100, over.sum(), 0.2)
	assert.Contains(t, note, "scaled")
	assert.InDelta(t, 0, over.Cheap, 0.05)
}

func TestParseSteerCommandLeavesTasksAlone(t *testing.T) {
	_, ok := ParseSteerCommand("do this and that")
	assert.False(t, ok)
	_, ok = ParseSteerCommand("more about the router")
	assert.False(t, ok, "more plus a non-axis is a task")
	_, ok = ParseSteerCommand("claude")
	assert.False(t, ok)

	cmd, ok := ParseSteerCommand("more oss 20%")
	require.True(t, ok)
	assert.Equal(t, "adjust", cmd.Kind)
	assert.Equal(t, "oss", cmd.Axis)
	assert.True(t, cmd.More)
	assert.True(t, cmd.HasPct)
	assert.Equal(t, 20.0, cmd.Pct)

	cmd, ok = ParseSteerCommand("less speed")
	require.True(t, ok)
	assert.Equal(t, "fast", cmd.Axis)
	assert.False(t, cmd.More)

	cmd, ok = ParseSteerCommand("oss=20% deterministic=10% frontier=30% quality=20% cheap=10% fast=10%")
	require.True(t, ok)
	assert.Equal(t, "assign", cmd.Kind)
	assert.Equal(t, 30.0, cmd.Assign["frontier"])
	assert.Equal(t, 10.0, cmd.Assign["deterministic"])

	cmd, ok = ParseSteerCommand("targets")
	require.True(t, ok)
	assert.Equal(t, "show", cmd.Kind)

	cmd, ok = ParseSteerCommand("more")
	require.True(t, ok)
	assert.NotEmpty(t, cmd.Err)

	cmd, ok = ParseSteerCommand("oss=150%")
	require.True(t, ok)
	assert.NotEmpty(t, cmd.Err)
}

func TestApplySteerLiftsAnUnderTargetAxisAndFadesWhenMet(t *testing.T) {
	rows := []Scored{{Leg: LegClaude, Value: 0.50, Quality: 9.5}, {Leg: LegGLM, Value: 0.40, Quality: 8.2}}
	mix := SteerMix{Set: true, OSS: 80, Frontier: 5, Quality: 5, Cheap: 5, Fast: 5}
	got := ApplySteer(rows, mix, nil)
	assert.Equal(t, LegGLM, got[0].Leg, "a cold window still prefers the axis the user just raised")

	var recent []Leg
	for i := 0; i < steerWarmup; i++ {
		recent = append(recent, LegGLM)
	}
	met := ApplySteer(rows, mix, recent)
	assert.Equal(t, LegClaude, met[0].Leg, "once recent routes already match, the value order returns")
}

func TestSteerAxesMatchTheRegistry(t *testing.T) {
	assert.Contains(t, steerLegAxes(LegClaude), "frontier")
	assert.NotContains(t, steerLegAxes(LegClaude), "cheap")
	assert.Contains(t, steerLegAxes(LegGLM), "oss")
	assert.Contains(t, steerLegAxes(LegGLM), "quality")
	assert.Contains(t, steerLegAxes(LegLuna), "cheap")
	assert.Contains(t, steerLegAxes(LegLuna), "fast")
	assert.Contains(t, steerLegAxes(LegCodexCLI), "frontier")

	old := ADISnapshot()
	defer SetADIFeed(old)
	SetADIFeed(adiTestFeed())
	assert.Contains(t, steerLegAxes(LegGPTOSS), "deterministic")
	assert.Contains(t, steerLegAxes(LegGPTOSS), "oss")
}
