package main

// The brain's side of the lanes (pkg/captaincode/lanes.go): which legs a
// /frontier, /quality or /save turn may use, and the note that counts each
// pick. A pick is noted under the same lock that read the counts, so two
// turns sent a moment apart split across the lane instead of both landing
// on the leg that was behind.

import (
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// routeLane is the lane a routed turn belongs to: /quality and /save (and
// their aliases). None when balancing is off, a leg was forced, or the user
// named legs - their words are binding. /frontier is not routed here; the
// frontier dispatch balances it (frontierLead).
func routeLane(req routeReq) captaincode.Lane {
	if req.Forced != "" || !captaincode.LanesEnabled() || len(captaincode.NamedAssignees(req.Task)) > 0 {
		return ""
	}
	switch lane := captaincode.LaneFor(req.Prefer); {
	case req.Prefer == "quality":
		return captaincode.LaneQuality // the menu below is narrowed only on the canonical word
	case lane == captaincode.LaneCheap:
		return lane
	}
	return ""
}

// laneOpen: a lane may use leg l when CAPTAIN_LEGS allows it, its window is
// open and it takes tasks. Called with b.mu held.
func (b *brain) laneOpen(l captaincode.Leg, now time.Time) bool {
	if len(b.allowed) > 0 && !b.allowed[l] {
		return false
	}
	if until, ok := b.ledger.Cooldowns[l]; ok && now.Before(until) {
		return false
	}
	return captaincode.KnownLeg(l) && captaincode.ServesTasks(l)
}

// reliableLane drops the legs that keep failing (value.go Unreliable) unless
// every candidate does: evening out turns onto a leg that stalls a third of
// the time would spend the user's turns on its stalls.
func reliableLane(cands []captaincode.LaneCandidate, stats map[captaincode.Leg]captaincode.LegStats) []captaincode.LaneCandidate {
	var out []captaincode.LaneCandidate
	for _, c := range cands {
		if !captaincode.Unreliable(stats[c.Leg]) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return cands
	}
	return out
}

// withoutFrontierClass drops the frontier-class legs (codex-cli, grok-max,
// the frontier pseudo-leg): they belong to /frontier. /quality is the best of
// the rest, at high effort - a social post sent to /quality ran gpt-6-astra,
// codex-cli's frontier model, because the lane rotated over it (2026-10-06).
func withoutFrontierClass(legs []captaincode.Leg) []captaincode.Leg {
	out := make([]captaincode.Leg, 0, len(legs))
	for _, l := range legs {
		if !captaincode.IsFrontierClass(l) {
			out = append(out, l)
		}
	}
	return out
}

// qualityMenuSize is how many legs /quality offers. Two meant claude and
// grok-max took 68 of 72 turns with near-equal scores; the lane's floor,
// not the menu, decides who shares the lane.
const qualityMenuSize = 4

// qualityMenu is /quality's menu: the best legs for the task's domain, plus
// the best open-weights leg when the routing mix owes open weights turns,
// so OSSTurn has one to give them to.
func (b *brain) qualityMenu(cand []captaincode.Leg, st map[captaincode.Leg]captaincode.LegStats, task string) []captaincode.Leg {
	d := captaincode.TriageTask(task).Domain
	cand = withoutFrontierClass(cand)
	menu := captaincode.TopQualityFor(cand, st, d, qualityMenuSize)
	if b.ossTarget() <= 0 {
		return menu
	}
	for _, l := range menu {
		if captaincode.OpenWeights(l) {
			return menu
		}
	}
	var open []captaincode.Leg
	for _, l := range cand {
		if captaincode.OpenWeights(l) {
			open = append(open, l)
		}
	}
	if best := captaincode.TopQualityFor(open, st, d, 1); len(best) == 1 && !captaincode.Unreliable(st[best[0]]) {
		menu = append(menu, best[0])
	}
	return menu
}

// ossTarget is the open-weights share /quality owes the routing mix: the
// saved mix, else the default 20%. CAPTAIN_STEER=0 turns it off with the
// rest of the mix.
func (b *brain) ossTarget() float64 {
	if !captaincode.SteerEnabled() || b.ledger == nil {
		return 0
	}
	return b.ledger.Steer.Resolved().OSS / 100
}

// frontierLead picks the leg a /frontier turn runs on and notes it: the
// frontier legs that are allowed, open and able to serve the task, ranked
// by perf index and evened out over the lane's recent turns. claude leads
// when balancing is off or no frontier leg qualifies, which is how
// /frontier behaved before lanes. Only the claude leg's own window counts
// here: a closed frontier tier (its pinned model's limit) is the reroute
// net's to handle, and it retries claude at standard settings before any
// other subscription (perf.go FrontierChainFor).
func (b *brain) frontierLead(task string) captaincode.LanePick {
	claude := captaincode.LanePick{Leg: captaincode.LegClaude, Band: []captaincode.Leg{captaincode.LegClaude}}
	if !captaincode.LanesEnabled() {
		claude.Reason = "frontier: claude (lane balancing off)"
		return claude
	}
	r := captaincode.Requirements{Vision: captaincode.TaskNeedsVision(task)}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	var cands []captaincode.LaneCandidate
	for _, c := range captaincode.FrontierLane(r) {
		if b.laneOpen(c.Leg, now) {
			cands = append(cands, c)
		}
	}
	cands = reliableLane(cands, b.ledger.Stats())
	// CAPTAIN_PICK=on: the lane chose the frontier legs; the time rule
	// picks among them (SCORING.md Phase 3).
	var legs []captaincode.Leg
	for _, c := range cands {
		legs = append(legs, c.Leg)
	}
	if tp, ok := b.decidePick(legs, captaincode.TriageTask(task).Domain); ok {
		b.ledger.NoteLane(captaincode.LaneFrontier, tp.Leg, now)
		return captaincode.LanePick{Leg: tp.Leg, Band: legs, Reason: "frontier " + tp.Reason, Time: &tp}
	}
	pick, ok := captaincode.BalanceLane(captaincode.LaneFrontier, cands,
		b.ledger.LaneCounts(captaincode.LaneFrontier, captaincode.LaneWindow()))
	if !ok {
		claude.Reason = "frontier: claude (no frontier leg is open for this task)"
		return claude
	}
	b.ledger.NoteLane(captaincode.LaneFrontier, pick.Leg, now)
	return pick
}

// pickLane balances a /quality or /save turn over the menu the route built
// for it - CAPTAIN_LEGS, cooldowns, vision, the /quality narrowing or the
// open-weight rule, the pool all applied. /quality scores by blended
// quality, as its menu was narrowed; /save by the same quality, over the
// open-weight legs that clear the class's good-enough bar. ok is false when
// nothing qualifies, and the director decides as it did before lanes.
// Called with b.mu held; the route notes the leg it settles on.
func (b *brain) pickLane(lane captaincode.Lane, task string, order []captaincode.Leg, class captaincode.Class, d captaincode.Domain) (captaincode.LanePick, bool) {
	stats := b.ledger.Stats()
	var cands []captaincode.LaneCandidate
	switch lane {
	case captaincode.LaneQuality:
		if d == "" {
			d = captaincode.TriageTask(task).Domain
		}
		for _, l := range withoutFrontierClass(order) {
			if captaincode.ClassCap(l, class) != "" {
				continue
			}
			cands = append(cands, captaincode.LaneCandidate{Leg: l, Score: captaincode.BlendedQualityFor(l, stats[l], d)})
		}
		cands = reliableLane(cands, stats)
		counts := b.ledger.LaneCounts(lane, captaincode.LaneWindow())
		if pick, ok := captaincode.OSSTurn(lane, cands, counts, b.ossTarget()); ok {
			return pick, true
		}
		return captaincode.BalanceLane(lane, cands, counts)
	case captaincode.LaneCheap:
		if d == "" {
			d = captaincode.TriageTask(task).Domain
		}
		for _, r := range captaincode.ValueRank(class, d, order, stats, estTokensFor(class), b.pressure) {
			if r.Eligible() && captaincode.OpenWeights(r.Leg) {
				cands = append(cands, captaincode.LaneCandidate{Leg: r.Leg, Score: r.Quality})
			}
		}
	default:
		return captaincode.LanePick{}, false
	}
	return captaincode.BalanceLane(lane, reliableLane(cands, stats), b.ledger.LaneCounts(lane, captaincode.LaneWindow()))
}
