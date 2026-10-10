package captaincode

// The /speed lane. /speed used to share the cheap tier's models and ask the
// director for "the fastest row" of a menu that had no time on it; the two
// /speed turns on record both went to Cursor, whose cheapest model still
// took 53 s on a one-line task, while GPT-6 Luna answered in 2.7 s ("Twelve
// Weeks of Routing", 2026-10-10). The lane ranks the legs by the median time
// of their own runs of the task's class, on the host they run on now, and
// takes the fastest. An agent CLI (claude, codex, cursor) starts an agent and
// reads the repository before it answers: on trivial work it is never the
// fast leg, whatever its model.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// speedMinRuns is how many timed runs make a leg's median count.
const speedMinRuns = 3

// speedExploreEvery: one /speed turn in this many tries a leg with no timing
// yet, so a new leg or a leg on a new host gets measured.
const speedExploreEvery = 10

// IsAgentCLI: the leg runs a coding agent's CLI, not a model call.
func IsAgentCLI(l Leg) bool {
	spec, ok := Spec(l)
	if !ok {
		return false
	}
	switch spec.Transport {
	case TransportClaudeCLI, TransportCodexCLI, TransportCursorCLI:
		return true
	}
	return IsFrontier(l)
}

// SpeedOf is a leg's timing for a class: its own runs of that class when it
// has enough, else all its runs. ok is false when the leg is not timed.
func SpeedOf(s LegStats, c Class) (SpeedStat, bool) {
	if st := s.Speed[c]; st.N >= speedMinRuns {
		return st, true
	}
	if st := s.Speed[""]; st.N >= speedMinRuns {
		return st, true
	}
	return SpeedStat{}, false
}

// SpeedPick picks the /speed leg among legs (already open, allowed and good
// enough for the class): the fastest timed one, or every
// speedExploreEvery-th turn (turns = the lane's recent turn count) an untimed
// one. ok is false when no leg qualifies.
func SpeedPick(legs []Leg, stats map[Leg]LegStats, c Class, turns int) (LanePick, bool) {
	type timed struct {
		leg Leg
		st  SpeedStat
	}
	var fast []timed
	var untimed []Leg
	for _, l := range legs {
		if c == ClassTrivial && IsAgentCLI(l) {
			continue
		}
		if st, ok := SpeedOf(stats[l], c); ok {
			fast = append(fast, timed{l, st})
		} else {
			untimed = append(untimed, l)
		}
	}
	if len(untimed) > 0 && (len(fast) == 0 || turns%speedExploreEvery == speedExploreEvery-1) {
		return LanePick{Leg: untimed[0], Band: untimed, Reason: fmt.Sprintf("speed lane: %s (not timed here yet: trying it)", untimed[0])}, true
	}
	if len(fast) == 0 {
		return LanePick{}, false
	}
	sort.SliceStable(fast, func(i, j int) bool { return fast[i].st.MedianMs < fast[j].st.MedianMs })
	band := make([]Leg, 0, len(fast))
	var tally []string
	for i, t := range fast {
		band = append(band, t.leg)
		if i > 0 && i < 4 {
			tally = append(tally, fmt.Sprintf("%s %s", t.leg, fmtMs(t.st.MedianMs)))
		}
	}
	best := fast[0]
	reason := fmt.Sprintf("speed lane: %s (median %s over %d runs", best.leg, fmtMs(best.st.MedianMs), best.st.N)
	if len(tally) > 0 {
		reason += "; " + strings.Join(tally, " · ")
	}
	return LanePick{Leg: best.leg, Band: band, Reason: reason + ")"}, true
}

func fmtMs(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// medianMs is the median of v (v is reordered).
func medianMs(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	m := len(v) / 2
	if len(v)%2 == 1 {
		return v[m]
	}
	return (v[m-1] + v[m]) / 2
}
