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

// Lanes (2026-09-23): /frontier, /quality and /save spread their turns over
// the legs that qualify instead of sending every one to the top row.

// laneRun plays n turns through the balancer, feeding each pick back into
// the counts the way the brain's NoteLane does.
func laneRun(t *testing.T, cands []LaneCandidate, counts map[Leg]int, n int) []Leg {
	t.Helper()
	var got []Leg
	for i := 0; i < n; i++ {
		p, ok := BalanceLane(LaneFrontier, cands, counts)
		require.True(t, ok)
		got = append(got, p.Leg)
		counts[p.Leg]++
	}
	return got
}

// Two legs a few points apart: the best leads by one run, then they take
// turns. Not a strict round-robin - the best keeps its lead - and never the
// same leg every time, which is what /frontier did (140 claude, 13
// codex-cli, 12-23 Sep).
func TestBalanceLaneLeadsByOneRunThenAlternates(t *testing.T) {
	cands := []LaneCandidate{{LegClaude, 57.6}, {LegCodexCLI, 52.4}}
	got := laneRun(t, cands, map[Leg]int{}, 8)
	assert.Equal(t, []Leg{LegClaude, LegClaude, LegCodexCLI, LegClaude, LegCodexCLI, LegClaude, LegCodexCLI, LegClaude}, got)
}

// Through the ledger's own window, turn after turn: the best leg runs the
// lane's first n turns, then the legs take turns, and a full window holds
// equal shares - the live frontier and cheap lanes (23 Sep scores).
func TestBalanceLaneEvensOutOverTheWindow(t *testing.T) {
	for _, tc := range []struct {
		cands []LaneCandidate
		start []Leg
		share int
	}{
		{[]LaneCandidate{{LegClaude, 57.6}, {LegCodexCLI, 52.4}},
			[]Leg{LegClaude, LegClaude, LegCodexCLI, LegClaude, LegCodexCLI}, 20},
		{[]LaneCandidate{{LegGLM, 7.71}, {LegMiniMax, 7.58}, {LegStep, 7.14}, {LegDeepSeek, 7.14}},
			[]Leg{LegGLM, LegGLM, LegGLM, LegGLM, LegMiniMax, LegStep, LegDeepSeek, LegGLM}, 10},
	} {
		l := &Ledger{}
		at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
		var got []Leg
		for i := 0; i < 400; i++ {
			p, ok := BalanceLane(LaneCheap, tc.cands, l.LaneCounts(LaneCheap, 40))
			require.True(t, ok)
			l.NoteLane(LaneCheap, p.Leg, at.Add(time.Duration(i)*time.Second))
			got = append(got, p.Leg)
		}
		assert.Equal(t, tc.start, got[:len(tc.start)])
		for _, c := range tc.cands {
			assert.Equal(t, tc.share, l.LaneCounts(LaneCheap, 40)[c.Leg], "%s's share of the last 40", c.Leg)
		}
	}
}

// A leg far behind its share catches up first, the one furthest behind
// before the next; ties go to rank.
func TestBalanceLaneCatchesUpTheLegFurthestBehind(t *testing.T) {
	cands := []LaneCandidate{{LegClaude, 10}, {LegCodexCLI, 9.8}, {LegGrokMax, 9.6}}
	counts := map[Leg]int{LegClaude: 6}
	got := laneRun(t, cands, counts, 4)
	assert.Equal(t, []Leg{LegCodexCLI, LegGrokMax, LegCodexCLI, LegGrokMax}, got, "the two behind share the catch-up, rank breaking the ties")
	p, _ := BalanceLane(LaneFrontier, cands, counts)
	assert.Contains(t, p.Reason, "claude 6 · codex-cli 2 · grok-max 2", "the reason carries the tally")
}

// Only legs within LaneFloor of the best share the lane: the live frontier
// numbers put grok-max (46.4) outside claude's (57.6) 85% band, whatever
// its count.
func TestBalanceLaneBandLeavesFarLegsOut(t *testing.T) {
	cands := []LaneCandidate{{LegGrokMax, 46.4}, {LegCodexCLI, 52.4}, {LegClaude, 57.6}}
	p, ok := BalanceLane(LaneFrontier, cands, map[Leg]int{LegClaude: 10})
	require.True(t, ok)
	assert.Equal(t, []Leg{LegClaude, LegCodexCLI}, p.Band, "ranked, and grok-max below the floor")
	assert.Equal(t, LegCodexCLI, p.Leg)
	assert.Contains(t, p.Reason, "frontier lane: codex-cli (under-used: 0 of the last 10, share 5.0")

	p, _ = BalanceLane(LaneFrontier, cands, map[Leg]int{LegClaude: 10, LegCodexCLI: 10})
	assert.Equal(t, LegClaude, p.Leg, "grok-max's zero does not count: it is not in the band")

	t.Setenv("CAPTAIN_LANE_FLOOR", "0.5")
	p, _ = BalanceLane(LaneFrontier, cands, map[Leg]int{LegClaude: 10, LegCodexCLI: 10})
	assert.Equal(t, LegGrokMax, p.Leg, "a wider floor lets it in")
}

func TestBalanceLaneEdges(t *testing.T) {
	_, ok := BalanceLane(LaneCheap, nil, nil)
	assert.False(t, ok, "no candidate, no pick")

	p, ok := BalanceLane(LaneCheap, []LaneCandidate{{LegGLM, 7.7}}, nil)
	require.True(t, ok)
	assert.Equal(t, LegGLM, p.Leg)
	assert.Equal(t, "cheap lane: glm (the only candidate open)", p.Reason)

	p, _ = BalanceLane(LaneCheap, []LaneCandidate{{LegGLM, 7.7}, {LegFree, 3}}, map[Leg]int{LegGLM: 9})
	assert.Equal(t, LegGLM, p.Leg)
	assert.Equal(t, "cheap lane: glm (no other leg within 85% of its score)", p.Reason)

	p, _ = BalanceLane(LaneQuality, []LaneCandidate{{LegGrokMax, 8}, {LegClaude, 8}}, nil)
	assert.Equal(t, LegGrokMax, p.Leg, "equal scores keep the order given")
	assert.Contains(t, p.Reason, "best score, no runs counted yet")

	p, _ = BalanceLane(LaneQuality, []LaneCandidate{{LegGrokMax, 8}, {LegClaude, 8}}, map[Leg]int{LegGrokMax: 1})
	assert.Equal(t, LegGrokMax, p.Leg, "half a run behind is not a run behind")
	assert.Contains(t, p.Reason, "best score, no leg a full run behind its share")
}

// The count is the lane's own last n turns: other lanes' rows are skipped,
// older rows fall out of the window.
func TestLaneCountsReadTheLanesRecentTurns(t *testing.T) {
	l := &Ledger{}
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	for i, r := range []struct {
		lane Lane
		leg  Leg
	}{
		{LaneFrontier, LegClaude}, {LaneFrontier, LegClaude}, {LaneCheap, LegGLM},
		{LaneFrontier, LegCodexCLI}, {LaneQuality, LegGrokMax}, {LaneFrontier, LegClaude},
	} {
		l.NoteLane(r.lane, r.leg, at.Add(time.Duration(i)*time.Minute))
	}
	assert.Equal(t, map[Leg]int{LegClaude: 3, LegCodexCLI: 1}, l.LaneCounts(LaneFrontier, 40))
	assert.Equal(t, map[Leg]int{LegClaude: 1, LegCodexCLI: 1}, l.LaneCounts(LaneFrontier, 2))
	assert.Equal(t, map[Leg]int{LegGLM: 1}, l.LaneCounts(LaneCheap, 40))
	assert.Empty(t, l.LaneCounts(LaneQuality, 0))

	l.NoteLane("", LegClaude, at)
	l.NoteLane(LaneFrontier, "", at)
	assert.Len(t, l.LaneRuns, 6, "a turn with no lane or no leg is not a lane turn")
}

func TestNoteLaneKeepsTheNewestRows(t *testing.T) {
	l := &Ledger{}
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	for i := 0; i < maxLaneRuns+10; i++ {
		l.NoteLane(LaneCheap, LegGLM, at.Add(time.Duration(i)*time.Second))
	}
	l.NoteLane(LaneCheap, LegMiniMax, at.Add(time.Hour))
	require.Len(t, l.LaneRuns, maxLaneRuns)
	assert.Equal(t, LegMiniMax, l.LaneRuns[len(l.LaneRuns)-1].Leg)
}

func TestLaneWindowAndFloorEnv(t *testing.T) {
	t.Setenv("CAPTAIN_LANE_WINDOW", "")
	t.Setenv("CAPTAIN_LANE_FLOOR", "")
	assert.Equal(t, 40, LaneWindow())
	assert.Equal(t, 0.85, LaneFloor())
	t.Setenv("CAPTAIN_LANE_WINDOW", "12")
	t.Setenv("CAPTAIN_LANE_FLOOR", "0.7")
	assert.Equal(t, 12, LaneWindow())
	assert.Equal(t, 0.7, LaneFloor())
	for _, bad := range []string{"0", "-3", "x"} {
		t.Setenv("CAPTAIN_LANE_WINDOW", bad)
		assert.Equal(t, 40, LaneWindow(), bad)
	}
	for _, bad := range []string{"0", "1.5", "-1", "x"} {
		t.Setenv("CAPTAIN_LANE_FLOOR", bad)
		assert.Equal(t, 0.85, LaneFloor(), bad)
	}
}

func TestLaneFor(t *testing.T) {
	for in, want := range map[string]Lane{
		"frontier": LaneFrontier, "quality": LaneQuality, "q": LaneQuality, "best": LaneQuality,
		"save": LaneCheap, "cheap": LaneCheap, " Save ": LaneCheap, "speed": "", "fast": "", "": "",
	} {
		assert.Equal(t, want, LaneFor(in), in)
	}
}

// Another process's lane turns survive this one's save, and the log comes
// back in time order: the balancer reads it from the tail.
func TestLaneRunsMergeAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	open := func() *Ledger {
		l := &Ledger{Sessions: map[string]string{}, Threads: map[string]ThreadRef{}, Cooldowns: map[Leg]time.Time{}, path: path}
		if data, err := os.ReadFile(path); err == nil {
			require.NoError(t, json.Unmarshal(data, l))
		}
		return l
	}
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	brain, cli := open(), open()
	brain.NoteLane(LaneFrontier, LegClaude, at)
	brain.NoteLane(LaneFrontier, LegCodexCLI, at.Add(2*time.Minute))
	cli.NoteLane(LaneFrontier, LegClaude, at.Add(time.Minute))
	require.NoError(t, cli.Save())
	require.NoError(t, brain.Save())

	got := open()
	require.Len(t, got.LaneRuns, 3, "neither writer dropped the other's turns")
	assert.Equal(t, []Leg{LegClaude, LegClaude, LegCodexCLI}, []Leg{got.LaneRuns[0].Leg, got.LaneRuns[1].Leg, got.LaneRuns[2].Leg}, "time order")
	assert.Equal(t, map[Leg]int{LegCodexCLI: 1, LegClaude: 1}, got.LaneCounts(LaneFrontier, 2), "the tail is the newest")

	require.NoError(t, got.Save())
	assert.Len(t, open().LaneRuns, 3, "a save of rows it already holds adds none")
}

// /save runs an open-weight leg at medium - its quality tier, its own model
// rather than its flash sibling. Any other leg, or lanes off, stays low.
func TestSaveEffortIsTheQualityTierOnOpenWeights(t *testing.T) {
	require.True(t, OpenWeights(LegGLM))
	require.False(t, OpenWeights(LegClaude))
	for _, p := range []string{"save", "cheap"} {
		assert.Equal(t, EffortMedium, DecideEffort(p, ClassTrivial, LegGLM, false, 1), p)
		assert.Equal(t, TierQuality, TierOf(DecideEffort(p, ClassMedium, LegGLM, false, 1)), p)
		assert.Equal(t, EffortLow, DecideEffort(p, ClassMedium, LegClaude, false, 1), p)
	}
	assert.Equal(t, EffortLow, DecideEffort("speed", ClassMedium, LegGLM, false, 1), "/speed is not a lane")
	t.Setenv("CAPTAIN_LANES", "0")
	assert.Equal(t, EffortLow, DecideEffort("save", ClassMedium, LegGLM, false, 1), "lanes off: /save as before")
}

// The frontier lane is FrontierLegs scored by the perf index, best first.
func TestFrontierLaneIsTheFrontierLegsByPerf(t *testing.T) {
	lane := FrontierLane(Requirements{})
	require.NotEmpty(t, lane)
	want := FrontierLegs()
	require.Len(t, lane, len(want))
	for i, c := range lane {
		assert.Equal(t, want[i], c.Leg)
		assert.Equal(t, perfOrPrior(c.Leg), c.Score)
		assert.True(t, c.Leg == LegClaude || IsFrontierClass(c.Leg), "%s is not a frontier leg", c.Leg)
		if i > 0 {
			assert.GreaterOrEqual(t, lane[i-1].Score, c.Score, "best first")
		}
	}
}
