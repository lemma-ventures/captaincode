package captaincode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectorModesAreWordsNotLegs(t *testing.T) {
	for w, want := range map[string]DirectorMode{"frontier": DirectorFrontier, "Quality": DirectorQuality, " auto ": DirectorAuto} {
		m, ok := ParseDirectorMode(w)
		assert.True(t, ok, w)
		assert.Equal(t, want, m)
	}
	_, ok := ParseDirectorMode("claude")
	assert.False(t, ok, "a leg name is not a mode")
}

func TestOnlyJudgesCanDirect(t *testing.T) {
	assert.True(t, DirectorCapable(LegClaude), "claude -p, no tools")
	assert.True(t, DirectorCapable(LegGLM), "an opencode pin runs with tools off")
	assert.False(t, DirectorCapable(LegCodexCLI), "codex exec is an agent, not a judge")
	assert.False(t, DirectorCapable(LegCursor), "cursor-agent likewise")
	for _, c := range DirectorCandidates() {
		assert.True(t, DirectorCapable(c.Leg))
	}
}

func TestGrokDirectsAsGrok46NotGrokBuild(t *testing.T) {
	p, model := directorPerf(LegGrok)
	assert.Equal(t, "grok-4.6", model, "the director override, not the worker pin")
	pw, _ := directorPerf(LegGrokMax)
	assert.Equal(t, pw, p, "scored on the same model grok-max pins")
	pb, _ := PerfFor(LegGrok)
	assert.Greater(t, p, pb.Perf, "grok-build would understate the director")
}

func TestFrontierIsTheTopRankedJudge(t *testing.T) {
	pick, ok := PickFrontierDirector()
	require.True(t, ok)
	c := DirectorCandidates()
	assert.Equal(t, c[0].Leg, pick.Leg)
	assert.Equal(t, LegClaude, pick.Leg, "on the compiled snapshot claude-fable ranks first among judges")
	assert.Contains(t, pick.Reason, "ranks first")
}

func TestQualityIsTheBestOfTierTwo(t *testing.T) {
	t.Setenv("CAPTAIN_DIRECTOR_TIER_BAND", "")
	pick, ok := PickQualityDirector()
	require.True(t, ok)
	c := DirectorCandidates()
	assert.NotEqual(t, c[0].Leg, pick.Leg, "never the flagship")
	// Everything above the pick is within the band of the top; the pick is not.
	floor := c[0].Perf * 0.9
	for _, x := range c {
		if x.Leg == pick.Leg {
			assert.Less(t, x.Perf, floor)
			break
		}
		assert.GreaterOrEqual(t, x.Perf, floor, "%s should be tier 1", x.Leg)
	}
	assert.Contains(t, pick.Reason, "tier 2")

	t.Setenv("CAPTAIN_DIRECTOR_TIER_BAND", "0.99") // the band swallows every leg
	pick, _ = PickQualityDirector()
	assert.Equal(t, c[0].Leg, pick.Leg, "no tier 2 → the frontier pick, said so")
}

func TestAutoPicksTheLeastUsedCapableLeg(t *testing.T) {
	now := time.Now()
	c := DirectorCandidates()
	require.GreaterOrEqual(t, len(c), 3)
	top, second := c[0].Leg, c[1].Leg
	usage := DirectorUsage{top: 5 * time.Hour, second: 10 * time.Minute}
	pick, ok := PickAutoDirector(usage, nil, "", now)
	require.True(t, ok)
	assert.NotEqual(t, top, pick.Leg, "the busiest leg is not underused")
	assert.Contains(t, pick.Reason, "least used")

	// A cooling leg is the opposite of underused.
	cooling := map[Leg]time.Time{pick.Leg: now.Add(time.Hour)}
	pick2, _ := PickAutoDirector(usage, cooling, "", now)
	assert.NotEqual(t, pick.Leg, pick2.Leg)

	// Hysteresis: the standing pick keeps the helm unless another leg has
	// used less than 60% of its time. Every other capable leg is busier.
	usage = DirectorUsage{top: 100 * time.Minute, second: 80 * time.Minute}
	for _, x := range c[2:] {
		usage[x.Leg] = 3 * time.Hour
	}
	keep, _ := PickAutoDirector(usage, nil, top, now)
	assert.Equal(t, top, keep.Leg, "80% of the current pick's usage is not clearly less")
	usage[second] = 30 * time.Minute
	move, _ := PickAutoDirector(usage, nil, top, now)
	assert.Equal(t, second, move.Leg, "30% is")
}

func TestOneCredentialIsOneCandidate(t *testing.T) {
	legs := map[Leg]bool{}
	for _, c := range DirectorCandidates() {
		legs[c.Leg] = true
	}
	assert.True(t, legs[LegGrok])
	assert.False(t, legs[LegGrokMax], "grok-max directs as the same grok-4.6 on the same credential as grok")
}

func TestDirectorWindowParses(t *testing.T) {
	t.Setenv("CAPTAIN_DIRECTOR_WINDOW", "")
	assert.Equal(t, 14*24*time.Hour, DirectorWindow())
	t.Setenv("CAPTAIN_DIRECTOR_WINDOW", "3w")
	assert.Equal(t, 21*24*time.Hour, DirectorWindow())
	t.Setenv("CAPTAIN_DIRECTOR_WINDOW", "36h")
	assert.Equal(t, 36*time.Hour, DirectorWindow())
	t.Setenv("CAPTAIN_DIRECTOR_WINDOW", "nonsense")
	assert.Equal(t, 14*24*time.Hour, DirectorWindow())
}
