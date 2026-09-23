package captaincode

// Lanes: spreading /frontier, /quality and /save across their legs
// (2026-09-23).
//
// A stated preference names a lane, not a leg. Before this file each lane
// resolved to one leg nearly every time. /frontier was claude at max effort,
// and codex-cli ran only when claude's window closed: 140 frontier turns on
// claude and 13 on codex-cli between 12 and 23 September, every one of the
// 13 a reroute. /quality told the director to take "the strongest row",
// which is claude whenever claude is on the menu. /save took the cheapest
// row of the whole ladder, subscription legs included, on each leg's cheap
// sibling. Two legs a few points apart are both good answers; sending every
// turn to one of them spends one subscription window, leaves the other's
// quality unmeasured, and turns an outage of the favourite into an outage
// of the lane.
//
// The balancer counts where each lane sent its recent turns and nudges the
// next one toward the leg that is behind its share:
//
//   - Only legs scoring within LaneFloor of the lane's best take part. A leg
//     far below the best is not a fair substitute, whatever its count.
//   - Shares are equal. A lower-ranked leg is preferred only once it is a
//     full run behind its share, so a tie never flaps and goes to the best
//     leg: it runs the lane's first n turns (n legs in the band), then the
//     legs take turns. Over a full window each has its share, the best at
//     most a run ahead (frontier's two legs: 20 each of the last 40).
//   - The count covers the lane's last LaneWindow turns: a week of claude
//     does not buy codex-cli the next forty turns in a row. There is no
//     seeding from older history, for the same reason.
//
// The lanes:
//
//   - frontier: the frontier-class legs and claude, ranked by perf index.
//     claude runs as the frontier pseudo-leg (its pinned strongest model,
//     max thinking); any other leg runs at max effort (codex-cli: its
//     frontier model at xhigh).
//   - quality: the top legs by blended quality (TopQuality), the director's
//     own leg included, at high effort.
//   - cheap: open-weight legs only, each at its quality tier - its own model
//     at medium effort rather than its flash sibling. The lane saves by
//     running open models, not small ones. A leg below the class's
//     good-enough bar (τ) stays out; when no open-weight leg is open the
//     turn falls back to the old /save path, so the lane is "almost only"
//     open weights, never "nothing ran".
//
// CAPTAIN_LANES=0 turns all of it off: /frontier is claude again, /quality
// and /save go back to the director, and /save runs at low effort.

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Lane is the set of legs a stated preference may send a turn to.
type Lane string

const (
	LaneFrontier Lane = "frontier"
	LaneQuality  Lane = "quality"
	LaneCheap    Lane = "cheap"
)

// LaneFor maps a stated preference to its lane; "" when the preference has
// none. /speed has no lane: the fastest leg is one leg.
func LaneFor(prefer string) Lane {
	switch strings.ToLower(strings.TrimSpace(prefer)) {
	case "frontier":
		return LaneFrontier
	case "quality", "q", "best":
		return LaneQuality
	case "save", "cheap":
		return LaneCheap
	}
	return ""
}

// LanesEnabled: CAPTAIN_LANES=0 turns lane balancing off, and with it the
// cheap lane's open-weight rule.
func LanesEnabled() bool { return os.Getenv("CAPTAIN_LANES") != "0" }

// LaneWindow is how many of a lane's recent turns the balancer counts.
// CAPTAIN_LANE_WINDOW, default 40.
func LaneWindow() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CAPTAIN_LANE_WINDOW"))); err == nil && n > 0 {
		return n
	}
	return 40
}

// LaneFloor: a leg shares a lane when its score is at least this fraction of
// the lane's best. CAPTAIN_LANE_FLOOR, default 0.85.
func LaneFloor() float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("CAPTAIN_LANE_FLOOR")), 64); err == nil && f > 0 && f <= 1 {
		return f
	}
	return 0.85
}

// LaneRun is one turn a lane sent to a leg, recorded at dispatch.
type LaneRun struct {
	At   time.Time `json:"at"`
	Lane Lane      `json:"lane"`
	Leg  Leg       `json:"leg"`
}

// maxLaneRuns caps the log: three lanes, each read LaneWindow deep.
const maxLaneRuns = 600

// NoteLane records that the lane sent a turn to leg. The caller holds
// whatever lock guards the ledger, and records at dispatch: counting only
// finished runs would let two turns sent a second apart both see the same
// counts and both land on the same leg.
func (l *Ledger) NoteLane(lane Lane, leg Leg, at time.Time) {
	if lane == "" || leg == "" {
		return
	}
	l.LaneRuns = append(l.LaneRuns, LaneRun{At: at, Lane: lane, Leg: leg})
	if len(l.LaneRuns) > maxLaneRuns {
		l.LaneRuns = l.LaneRuns[len(l.LaneRuns)-maxLaneRuns:]
	}
}

// LaneCounts is how many of the lane's last n turns each leg took.
func (l *Ledger) LaneCounts(lane Lane, n int) map[Leg]int {
	out := map[Leg]int{}
	for i := len(l.LaneRuns) - 1; i >= 0 && n > 0; i-- {
		if r := l.LaneRuns[i]; r.Lane == lane {
			out[r.Leg]++
			n--
		}
	}
	return out
}

// LaneCandidate is a leg a lane may use, with the score that ranks it: the
// perf index on the frontier lane, blended quality on the others.
type LaneCandidate struct {
	Leg   Leg
	Score float64
}

// FrontierLane is the frontier lane's legs that can serve the task, scored
// by perf index: FrontierLegs, capability-filtered. Empty when none can.
func FrontierLane(r Requirements) []LaneCandidate {
	var out []LaneCandidate
	for _, l := range FilterCapable(FrontierLegs(), r) {
		out = append(out, LaneCandidate{Leg: l, Score: perfOrPrior(l)})
	}
	return out
}

// LanePick is the balancer's answer.
type LanePick struct {
	Leg    Leg
	Band   []Leg // the legs that shared the lane, best first
	Reason string
}

// BalanceLane picks the leg for a lane's next turn. cands may come in any
// order; equal scores keep the order given. counts is LaneCounts for the
// lane. ok is false only when there is no candidate.
func BalanceLane(lane Lane, cands []LaneCandidate, counts map[Leg]int) (LanePick, bool) {
	if len(cands) == 0 {
		return LanePick{}, false
	}
	ranked := append([]LaneCandidate(nil), cands...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	floor := ranked[0].Score * LaneFloor()
	var band []LaneCandidate
	total := 0
	for _, c := range ranked {
		if c.Score >= floor {
			band = append(band, c)
			total += counts[c.Leg]
		}
	}
	legs := make([]Leg, 0, len(band))
	for _, c := range band {
		legs = append(legs, c.Leg)
	}
	if len(band) == 1 {
		why := "the only candidate open"
		if len(ranked) > 1 {
			why = fmt.Sprintf("no other leg within %.0f%% of its score", LaneFloor()*100)
		}
		return LanePick{Leg: band[0].Leg, Band: legs, Reason: fmt.Sprintf("%s lane: %s (%s)", lane, band[0].Leg, why)}, true
	}
	// The deficit is how far a leg is behind an equal share of the band's
	// turns. The best leg holds the lane unless another is a full run
	// behind; then the one furthest behind takes it (ties go to rank).
	share := float64(total) / float64(len(band))
	pick := band[0]
	most := share - float64(counts[pick.Leg])
	for _, c := range band[1:] {
		if d := share - float64(counts[c.Leg]); d >= 1 && d > most {
			pick, most = c, d
		}
	}
	var why string
	switch {
	case total == 0:
		why = "best score, no runs counted yet"
	case pick.Leg != band[0].Leg || most >= 1:
		why = fmt.Sprintf("under-used: %d of the last %d, share %.1f", counts[pick.Leg], total, share)
	default:
		why = "best score, no leg a full run behind its share"
	}
	return LanePick{Leg: pick.Leg, Band: legs,
		Reason: fmt.Sprintf("%s lane: %s (%s; %s)", lane, pick.Leg, why, laneTally(band, counts))}, true
}

// laneTally renders "claude 3 · codex-cli 2" for a reason line.
func laneTally(band []LaneCandidate, counts map[Leg]int) string {
	parts := make([]string, 0, len(band))
	for _, c := range band {
		parts = append(parts, fmt.Sprintf("%s %d", c.Leg, counts[c.Leg]))
	}
	return strings.Join(parts, " · ")
}
