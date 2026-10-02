package captaincode

// One picking rule (SCORING.md Phase 3).
//
// A turn goes to the leg expected to give the user an accepted answer
// soonest: its run time, plus P(rework) times a repair and a re-run, plus a
// quota cost for a subscription window that is filling, minus a pull toward
// any routing-mix axis the leg would serve that is below its target share.
// Each leg's P is drawn from its Phase 2 posterior (Thompson sampling), so a
// leg with little evidence still gets tried in proportion to the chance it
// is the best - but a user waiting on the turn is only offered legs whose
// posterior mean is within liveMargin of the best. The lanes (/frontier,
// /quality, /save) only choose the candidates; this rule picks among them.

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
)

// Rule tunables.
const (
	quotaMinutes = 15.0 // minutes a full subscription window adds to a turn
	mixMinutes   = 20.0 // minutes a unit of routing-mix deficit is worth
	liveMargin   = 0.15 // P below the best's that a waiting user is still offered
	pickDraws    = 400  // draws behind the recorded propensities
)

// PickModeEnv selects the rule: "on" decides, "off" is silent, anything
// else (the default) records its pick beside the decision without acting.
const PickModeEnv = "CAPTAIN_PICK"

// PickMode is the rule's mode: "on", "off" or "shadow".
func PickMode() string {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(PickModeEnv))); v {
	case "on", "off":
		return v
	}
	return "shadow"
}

// PickCandidate is a leg the rule may pick, with what it costs.
type PickCandidate struct {
	Leg Leg
	Est ModelEstimate
	// Pressure is how full the leg's subscription window is (0 for legs
	// paid per call); MixDeficit how far below target the mix axes it
	// serves are.
	Pressure   float64
	MixDeficit float64
}

// TimePick is the rule's answer, recorded on the decision.
type TimePick struct {
	Leg  Leg    `json:"leg"`
	Mode string `json:"mode"`
	// Minutes is each candidate's expected minutes to an accepted answer at
	// its posterior mean; Propensities the share of draws each won.
	Minutes      map[Leg]float64 `json:"minutes"`
	Propensities map[Leg]float64 `json:"propensities"`
	// Explored: the draw picked a leg other than the best by mean.
	Explored bool   `json:"explored,omitempty"`
	Reason   string `json:"reason"`
}

// ExpectedMinutes is a candidate's expected minutes to an accepted answer
// when its success probability is p.
func ExpectedMinutes(c PickCandidate, p float64) float64 {
	t := c.Est.Minutes()
	return t + (1-p)*2*t + quotaMinutes*c.Pressure - mixMinutes*c.MixDeficit
}

// PickByTime runs the rule over the candidates. explore false takes the best
// by mean (the exploration budget is spent); ok is false with no candidate.
func PickByTime(cands []PickCandidate, explore bool, rng *rand.Rand) (TimePick, bool) {
	if len(cands) == 0 {
		return TimePick{}, false
	}
	best := 0.0
	for _, c := range cands {
		if p := c.Est.P(); p > best {
			best = p
		}
	}
	var live []PickCandidate
	for _, c := range cands {
		if c.Est.P() >= best-liveMargin {
			live = append(live, c)
		}
	}
	tp := TimePick{Minutes: map[Leg]float64{}, Propensities: map[Leg]float64{}}
	meanBest := live[0]
	for _, c := range live {
		tp.Minutes[c.Leg] = ExpectedMinutes(c, c.Est.P())
		if tp.Minutes[c.Leg] < tp.Minutes[meanBest.Leg] {
			meanBest = c
		}
	}
	draw := func() Leg {
		pick, least := live[0].Leg, 0.0
		for i, c := range live {
			m := ExpectedMinutes(c, c.Est.SampleP(rng))
			if i == 0 || m < least {
				pick, least = c.Leg, m
			}
		}
		return pick
	}
	wins := map[Leg]int{}
	for i := 0; i < pickDraws; i++ {
		wins[draw()]++
	}
	for l, n := range wins {
		tp.Propensities[l] = float64(n) / pickDraws
	}
	tp.Leg = meanBest.Leg
	if explore {
		tp.Leg = draw()
	}
	tp.Explored = tp.Leg != meanBest.Leg
	tp.Reason = pickReason(tp, live)
	return tp, true
}

func pickReason(tp TimePick, live []PickCandidate) string {
	legs := make([]Leg, 0, len(live))
	for _, c := range live {
		legs = append(legs, c.Leg)
	}
	sort.SliceStable(legs, func(i, j int) bool { return tp.Minutes[legs[i]] < tp.Minutes[legs[j]] })
	parts := make([]string, 0, len(legs))
	for _, l := range legs {
		parts = append(parts, fmt.Sprintf("%s %.1fm", l, tp.Minutes[l]))
	}
	how := "fewest expected minutes"
	if tp.Explored {
		how = fmt.Sprintf("Thompson draw (wins %.0f%% of draws)", tp.Propensities[tp.Leg]*100)
	}
	return fmt.Sprintf("time rule: %s - %s; %s", tp.Leg, how, strings.Join(parts, " · "))
}

// MixDeficit is how far below their target share the routing-mix axes leg
// serves are, over the recent picks: the pull the mix puts on it. An unset
// mix pulls with the default 20% per axis; CAPTAIN_STEER=0 removes it.
func MixDeficit(mix SteerMix, recent []Leg, leg Leg) float64 {
	if !SteerEnabled() {
		return 0
	}
	target := mix.Resolved()
	observed := steerObserved(recent)
	var d float64
	for _, ax := range steerLegAxes(leg) {
		if gap := target.get(ax)/100 - observed[ax]; gap > 0 {
			d += gap
		}
	}
	return d
}
