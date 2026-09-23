package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Lanes, the brain's side (brain_lanes.go): /frontier, /quality and /save
// spread their turns over the legs that qualify and count each one, where
// each used to land on one leg nearly every time (lanes.go).

// laneTotal is how many turns the lane counted, whichever legs took them.
func laneTotal(b *brain, lane captaincode.Lane) (map[captaincode.Leg]int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	counts := b.ledger.LaneCounts(lane, captaincode.LaneWindow())
	n := 0
	for _, c := range counts {
		n += c
	}
	return counts, n
}

// feedSays reports whether the activity feed carries a line of kind with
// text containing want.
func feedSays(b *brain, kind, want string) bool {
	b.amu.Lock()
	defer b.amu.Unlock()
	for _, a := range b.acts {
		if a.Kind == kind && strings.Contains(a.Text, want) {
			return true
		}
	}
	return false
}

// /frontier was claude every time: codex-cli ran 13 of 153 frontier turns
// (12-23 Sep), each a reroute after claude's window closed. The lane now
// takes turns between the two, the better one leading by a run, and a leg
// other than claude runs at max effort under its own name.
func TestFrontierLaneTakesTurnsAcrossTheFrontierLegs(t *testing.T) {
	t.Setenv("CAPTAIN_LANE_FLOOR", "0.5") // the index moves; this is about the turns, not the band
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegClaude: true, captaincode.LegCodexCLI: true}
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 8, Verdict: "good"}, nil
	}
	var mu sync.Mutex
	var ran []captaincode.Leg
	var effort captaincode.Effort
	b.frontierFn = func(task string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, captaincode.LegClaude)
		return captaincode.Result{Text: "refactored the lexer", DurationMs: 5}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, leg)
		effort = b.active.snapshot()[leg].effort
		return leg, captaincode.Result{Text: "refactored the lexer", DurationMs: 5}, nil
	}
	// The better of the two by perf index leads; the test does not pin
	// which one that is today.
	var lane []captaincode.Leg
	for _, c := range captaincode.FrontierLane(captaincode.Requirements{}) {
		if b.allowed[c.Leg] {
			lane = append(lane, c.Leg)
		}
	}
	require.Len(t, lane, 2)
	lead, other := lane[0], lane[1]

	dir := t.TempDir()
	turn := func() {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"model": "frontier", "stream": false,
			"messages": []map[string]string{{"role": "user", "content": "refactor the lexer"}}})
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		r.Header.Set(workspaceHeader, dir)
		b.chatCompletions(rec, r)
		require.Equal(t, 200, rec.Code, rec.Body.String())
	}
	for i := 0; i < 4; i++ {
		turn()
	}
	mu.Lock()
	assert.Equal(t, []captaincode.Leg{lead, lead, other, lead}, ran)
	assert.Equal(t, captaincode.EffortMax, effort, "codex-cli runs at its frontier setting (xhigh)")
	mu.Unlock()
	counts, n := laneTotal(b, captaincode.LaneFrontier)
	assert.Equal(t, map[captaincode.Leg]int{lead: 3, other: 1}, counts)
	assert.Equal(t, 4, n)
	assert.True(t, feedSays(b, "route", "frontier lane: "+string(other)+" (under-used"), "the turn says why it went where it did")

	// Lanes off: /frontier is claude, as it was, and nothing is counted.
	t.Setenv("CAPTAIN_LANES", "0")
	turn()
	turn()
	mu.Lock()
	assert.Equal(t, []captaincode.Leg{captaincode.LegClaude, captaincode.LegClaude}, ran[4:])
	mu.Unlock()
	_, n = laneTotal(b, captaincode.LaneFrontier)
	assert.Equal(t, 4, n)
}

// A frontier leg whose window is closed is out of the lane for the turn;
// with none open, claude leads and the reroute net takes it from there.
func TestFrontierLaneSkipsAClosedLeg(t *testing.T) {
	t.Setenv("CAPTAIN_LANE_FLOOR", "0.5")
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegClaude: true, captaincode.LegCodexCLI: true}
	b.ledger.Cooldowns[captaincode.LegCodexCLI] = time.Now().Add(time.Hour)
	for i := 0; i < 3; i++ {
		assert.Equal(t, captaincode.LegClaude, b.frontierLead("refactor the lexer").Leg)
	}
	b.ledger.Cooldowns[captaincode.LegClaude] = time.Now().Add(time.Hour)
	p := b.frontierLead("refactor the lexer")
	assert.Equal(t, captaincode.LegClaude, p.Leg)
	assert.Contains(t, p.Reason, "no frontier leg is open")
	_, n := laneTotal(b, captaincode.LaneFrontier)
	assert.Equal(t, 3, n, "the fallback is not a lane turn")
}

// /save ran the cheapest row of the whole ladder, subscription legs
// included, on each leg's flash sibling. The cheap lane runs open-weight
// legs only, at their quality tier (medium: the leg's own model), spread
// over the ones that clear the class's bar, and asks no director.
func TestSaveLaneRunsOpenWeightsAtTheirQualityTier(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	seen := map[string]int{}
	// Twelve turns: with n legs in the band the best runs the first n, and
	// the test's priors put five open-weight legs within 85% of glm.
	for i := 0; i < 12; i++ {
		prefer := "save"
		if i%3 == 2 {
			prefer = "cheap" // a direct caller's word for the same lane
		}
		resp := routeBody(t, b, "refactor the lexer", map[string]any{"prefer": prefer})
		leg := captaincode.Leg(resp["leg"].(string))
		assert.True(t, captaincode.OpenWeights(leg), "turn %d: %s is not open-weight", i, leg)
		assert.Equal(t, "medium", resp["effort"], "turn %d: the leg's own model, not its flash sibling", i)
		assert.Contains(t, resp["rationale"], "cheap lane:", "turn %d", i)
		seen[string(leg)]++
	}
	assert.GreaterOrEqual(t, len(seen), 3, "the lane spreads its turns: %v", seen)
	_, n := laneTotal(b, captaincode.LaneCheap)
	assert.Equal(t, 12, n)

	resp := routeBody(t, b, "refactor the lexer, /save tokens", nil)
	assert.Contains(t, resp["rationale"], "cheap lane:", "a mid-prompt /save is the same lane")
	p := b.pendingDecisions[truncate("refactor the lexer, /save tokens", 120)]
	assert.Equal(t, captaincode.PathLane, p.dec.Path, "the record says the lane settled it")
}

// No open-weight leg open: /save still runs, over the whole ladder as it
// did before lanes, at low effort, and the feed says why.
func TestSaveLaneWithNoOpenWeightLegFallsBack(t *testing.T) {
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegClaude: true, captaincode.LegCursor: true, captaincode.LegCodexCLI: true}
	asked := false
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		asked = true
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: open[0], Brief: task}}}, nil
	}
	resp := routeBody(t, b, "refactor the lexer", map[string]any{"prefer": "save"})
	assert.True(t, asked, "the director decides, as before lanes")
	assert.False(t, captaincode.OpenWeights(captaincode.Leg(resp["leg"].(string))))
	assert.Equal(t, "low", resp["effort"])
	assert.True(t, feedSays(b, "route", "save lane: no open-weight leg is open"))
	_, n := laneTotal(b, captaincode.LaneCheap)
	assert.Zero(t, n, "nothing to balance, nothing counted")
}

// /quality asked the director for "the strongest row" and got the top row
// every time. The lane spreads its turns over the top legs by blended
// quality and settles the leg without an LLM call.
func TestQualityLaneSpreadsOverTheTopLegs(t *testing.T) {
	b := teamBrain()
	noDirector(t, b)
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		resp := routeBody(t, b, "refactor the lexer", map[string]any{"prefer": "quality"})
		assert.Contains(t, resp["rationale"], "quality lane:")
		assert.Equal(t, "high", resp["effort"])
		seen[resp["leg"].(string)]++
	}
	assert.Len(t, seen, 2, "the two top legs share the lane: %v", seen)
	p := b.pendingDecisions[truncate("refactor the lexer", 120)]
	assert.Equal(t, captaincode.PathLane, p.dec.Path)

	resp := routeBody(t, b, "/q refactor the lexer", nil)
	assert.Contains(t, resp["rationale"], "quality lane:", "the short alias at the head is /quality")
}

// A high-class /quality turn had the director's leg on its menu twice: the
// high-class ladder adds it, and /quality added it again, so the top two by
// quality were "claude, claude" - a lane of one.
func TestQualityLaneHoldsTheDirectorsLegOnce(t *testing.T) {
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
	b := teamBrain()
	noDirector(t, b)
	seen := map[string]int{}
	for i := 0; i < 3; i++ {
		resp := routeBody(t, b, "audit the security of the auth proxy across the codebase", map[string]any{"prefer": "quality"})
		seen[resp["leg"].(string)]++
	}
	assert.Len(t, seen, 2, "two legs, not claude twice: %v", seen)
	assert.Contains(t, seen, "claude", "the director's own leg is on a /quality menu")
}

// Only turns the lane sends are counted: a plan-only route sends nothing, a
// forced leg and legs the user named are the user's own choice.
func TestLaneCountsOnlyTheTurnsItSends(t *testing.T) {
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: task}}}, nil
	}
	_, fail := b.decideRoute(routeReq{Task: "refactor the lexer", Prefer: "save", planOnly: true})
	require.Nil(t, fail)
	resp := routeBody(t, b, "refactor the lexer", map[string]any{"prefer": "save", "forced": "claude"})
	assert.Equal(t, "claude", resp["leg"])
	resp = routeBody(t, b, "have claude and grok review the doctor output", map[string]any{"prefer": "quality"})
	assert.NotContains(t, resp["rationale"], "quality lane:", "named legs bind")
	for _, lane := range []captaincode.Lane{captaincode.LaneCheap, captaincode.LaneQuality} {
		_, n := laneTotal(b, lane)
		assert.Zero(t, n, lane)
	}
}

// A leg that keeps failing is out of the lane unless every candidate is.
func TestReliableLaneDropsLegsThatKeepFailing(t *testing.T) {
	cands := []captaincode.LaneCandidate{{Leg: captaincode.LegKimi, Score: 8}, {Leg: captaincode.LegGLM, Score: 7.7}}
	failing := map[captaincode.Leg]captaincode.LegStats{captaincode.LegKimi: {N: 2, Fails: 2}}
	require.True(t, captaincode.Unreliable(failing[captaincode.LegKimi]))
	assert.Equal(t, cands[1:], reliableLane(cands, failing))
	failing[captaincode.LegGLM] = captaincode.LegStats{Fails: 3}
	assert.Equal(t, cands, reliableLane(cands, failing), "all failing: the lane keeps them all")
}
