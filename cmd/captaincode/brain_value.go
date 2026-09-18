package main

// Value routing + exploration in the brain (MM37 phases 3–4).
//
// For every un-prefixed prompt the cheap path (tier 0/1) used to walk a
// hand-written FastLadder; it now ranks the open, allowed, capable legs by
// ROUTING.md §5 value (quality − cost − latency, per class and domain) with a
// good-enough threshold - so a new cheap model with a strong index lands at
// the top of medium/editorial without anyone editing a list. The director
// (high class, /team) gets the same numbers as hints on its menu.
//
// Exploration: with probability ε the cheap path runs the SECOND-ranked
// candidate and grades it as usual. A leg that is never picked never earns a
// score (the counterfactual blindness the frontier itself pointed out); the
// reroute net and the assess loop bound the downside.

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// valueRoutingEnabled: CAPTAIN_VALUE_ROUTING=0 restores the hand-written ladders.
func valueRoutingEnabled() bool { return os.Getenv("CAPTAIN_VALUE_ROUTING") != "0" }

// exploreRate is ε per class; CAPTAIN_EXPLORE="trivial,medium" overrides
// (default 0.10, 0.05); CAPTAIN_EXPLORE=0 disables.
func exploreRate(c captaincode.Class) float64 {
	trivial, medium := 0.10, 0.05
	if v := os.Getenv("CAPTAIN_EXPLORE"); v != "" {
		f := strings.Split(v, ",")
		if len(f) == 1 {
			if x, err := strconv.ParseFloat(strings.TrimSpace(f[0]), 64); err == nil {
				trivial, medium = x, x
			}
		} else if len(f) == 2 {
			if x, err := strconv.ParseFloat(strings.TrimSpace(f[0]), 64); err == nil {
				trivial = x
			}
			if x, err := strconv.ParseFloat(strings.TrimSpace(f[1]), 64); err == nil {
				medium = x
			}
		}
	}
	if c == captaincode.ClassTrivial {
		return trivial
	}
	return medium
}

// estTokensFor sizes the cost term: the class's replay budget (chars → tokens)
// plus a typical answer.
func estTokensFor(c captaincode.Class) int {
	return replayBudget(c)/4 + 4000
}

// pressure is a subscription leg's window pressure from the only signal we
// observe: rate limits. Cooling right now → 1; rate-limited in the last 5h
// (the common window length) → 0.5; else 0. Callers hold b.mu.
func (b *brain) pressure(l captaincode.Leg) float64 {
	now := time.Now()
	if until, ok := b.ledger.Cooldowns[l]; ok && now.Before(until) {
		return 1
	}
	for i := len(b.ledger.Events) - 1; i >= 0 && i >= len(b.ledger.Events)-200; i-- {
		e := b.ledger.Events[i]
		if e.Leg != l || e.Outcome == "ok" {
			continue
		}
		if now.Sub(e.At) > 5*time.Hour {
			break
		}
		if strings.Contains(strings.ToLower(e.Error), "rate limited") {
			return 0.5
		}
	}
	return 0
}

// quotaExhausted reports whether a leg's quota observation says it has no
// remaining allowance. A measured 0 (the adapter said the window is closed)
// or an inferred 0 (the cooldown window implies it) excludes the leg from the
// routing gate. An unknown observation does NOT — treating an unmeasured leg
// as exhausted would starve a healthy one (ROADMAP M2.3 proactive gate).
// Stale observations are ignored: the figure may have reset since.
func (b *brain) quotaExhausted(l captaincode.Leg, now time.Time) bool {
	q := b.ledger.QuotaFor(l)
	if q.Status == captaincode.QuotaUnknown {
		return false
	}
	if q.Stale(now) {
		return false
	}
	return q.Exhausted()
}

// quotaLow reports whether a leg's measured quota is below the steering
// threshold (default 10% of limit or 5 remaining when no limit is known).
// This is the proactive signal: the leg is not exhausted but is close enough
// that routing to an alternative avoids discovering it after dispatch.
func (b *brain) quotaLow(l captaincode.Leg, now time.Time) bool {
	q := b.ledger.QuotaFor(l)
	threshold := envInt("CAPTAIN_QUOTA_LOW_THRESHOLD", 5)
	if q.Limit > 0 && threshold < q.Limit/10 {
		threshold = q.Limit / 10
	}
	return captaincode.QuotaLow(q, threshold, now)
}

// quotaSuffix renders a short suffix for the exclusion reason.
func quotaSuffix(q captaincode.Quota, now time.Time) string {
	if q.ResetAt.IsZero() {
		return ""
	}
	if now.Before(q.ResetAt) {
		return fmt.Sprintf(", resets %s", q.ResetAt.Local().Format("15:04"))
	}
	return ""
}

// valueCandidates are the legs the cheap path may pick: worker rungs that are
// allowed, open, not the director, never claude or a frontier-class leg (the
// bazooka gate), vision-capable when the task needs it.
//
// It returns the survivors AND the rejects with their reasons. These are HARD
// constraints - they are applied before any ranking, so a leg filtered here
// never appears in a score at all, and used to leave no trace whatsoever. "The
// leg I expected wasn't chosen" and "the leg I expected wasn't eligible" are
// different answers, and `captain why` could give neither (ROADMAP M2.1).
func (b *brain) valueCandidates(needVision bool) ([]captaincode.Leg, []captaincode.Scored) {
	var out []captaincode.Leg
	var excluded []captaincode.Scored
	now := time.Now()
	// Capability constraints are asked of the M2.2 registry rather than of a
	// single vision boolean, so a leg ruled out for any runtime reason names
	// the capability it lacks and the transport that answered.
	req := captaincode.Requirements{Vision: needVision}
	// Strict-mode cost cap (ROADMAP M2.4 remaining): when the operator sets a
	// hard dollar ceiling and CAPTAIN_STRICT=1, legs whose transport cannot
	// report per-turn cost are rejected before dispatch — a cap the adapter
	// cannot measure is a cap the system cannot enforce.
	strictCost := b.strictCostMode()
	reject := func(l captaincode.Leg, why string) {
		excluded = append(excluded, captaincode.Scored{Leg: l, Excluded: why})
	}
	for _, l := range captaincode.Rungs {
		lacks := req.Missing(l)
		switch {
		case l == captaincode.Director:
			continue // not a worker rung; it has no place on a try-order
		case l == captaincode.LegClaude || captaincode.IsFrontierClass(l):
			reject(l, "frontier-class: reserved for /frontier, /quality and the director")
		case len(b.allowed) > 0 && !b.allowed[l]:
			reject(l, "outside CAPTAIN_LEGS")
		case lacks != "":
			reject(l, lacks)
		case strictCost != "" && captaincode.CostEnforceable(l) != "":
			reject(l, captaincode.CostEnforceable(l))
		case now.Before(b.ledger.Cooldowns[l]):
			reject(l, "cooling down until "+b.ledger.Cooldowns[l].Format("15:04"))
		case b.quotaExhausted(l, now):
			q := b.ledger.QuotaFor(l)
			reject(l, "quota exhausted"+quotaSuffix(q, now))
		case b.quotaLow(l, now):
			q := b.ledger.QuotaFor(l)
			reject(l, fmt.Sprintf("quota low: %d remaining", q.Remaining))
		default:
			out = append(out, l)
		}
	}
	return out, excluded
}

// valueLadder ranks the candidates for (class, domain), best first, with the
// legs that never reached the ranking appended as exclusions. Callers hold b.mu.
func (b *brain) valueLadder(c captaincode.Class, d captaincode.Domain, needVision bool) []captaincode.Scored {
	open, excluded := b.valueCandidates(needVision)
	rows := append(captaincode.ValueRank(c, d, open, b.ledger.Stats(), estTokensFor(c), b.pressure), excluded...)
	return b.calibrateRows(c, d, rows)
}

// cheapLadder is the tier-0/1 try-order: value-ranked when enabled, else the
// hand-written FastLadder. Callers hold b.mu.
func (b *brain) cheapLadder(c captaincode.Class, d captaincode.Domain, needVision bool) []captaincode.Leg {
	return b.ladderFrom(c, d, needVision, b.valueLadder(c, d, needVision))
}

// ladderFrom is cheapLadder over a ranking the caller already has, so a route
// that also records the decision evidence does not rank the field twice.
func (b *brain) ladderFrom(c captaincode.Class, d captaincode.Domain, needVision bool, rows []captaincode.Scored) []captaincode.Leg {
	if valueRoutingEnabled() {
		if legs := captaincode.Legs(rows); len(legs) > 0 {
			return legs
		}
		// Nothing clears τ: fall through so a task still runs somewhere.
	}
	ladder := b.filterAllowed(b.openOnly(captaincode.FastLadder(c, d)))
	if needVision {
		if vo := captaincode.FilterVision(ladder); len(vo) > 0 {
			ladder = vo
		}
	}
	return ladder
}

// explore decides whether this turn tries the runner-up. Test seam: exploreFn.
func (b *brain) explore(c captaincode.Class) bool {
	if b.exploreFn != nil {
		return b.exploreFn(c)
	}
	eps := exploreRate(c)
	if eps <= 0 {
		return false
	}
	if b.rng == nil {
		b.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return b.rng.Float64() < eps
}

// markExplored tags a task so its ledger event records the pick as
// exploration (dashboards separate explored from exploited quality).
func (b *brain) markExplored(task string) {
	if b.explored == nil {
		b.explored = map[string]time.Time{}
	}
	b.explored[truncate(task, 120)] = time.Now()
	for k, at := range b.explored { // bounded: forget after an hour
		if time.Since(at) > time.Hour {
			delete(b.explored, k)
		}
	}
}

func (b *brain) wasExplored(task string) bool {
	at, ok := b.explored[truncate(task, 120)]
	return ok && time.Since(at) <= time.Hour
}

// valueHints renders one line per leg for the director's menu: estimated $
// for this task, window pressure, and the value rank for this class/domain.
func (b *brain) valueHints(c captaincode.Class, d captaincode.Domain, legs []captaincode.Leg) map[captaincode.Leg]string {
	stats := b.ledger.Stats()
	rows := captaincode.Eligible(captaincode.ValueRank(c, d, legs, stats, estTokensFor(c), b.pressure))
	rank := map[captaincode.Leg]int{}
	for i, r := range rows {
		rank[r.Leg] = i + 1
	}
	out := map[captaincode.Leg]string{}
	for _, l := range legs {
		cost := captaincode.EstimateCost(l, estTokensFor(c))
		var parts []string
		if cost > 0 {
			parts = append(parts, fmt.Sprintf("est $%.3f/task", cost))
		} else if sp, ok := captaincode.Spec(l); ok && sp.Subscription {
			if p := b.pressure(l); p > 0 {
				parts = append(parts, fmt.Sprintf("subscription window under pressure (%.0f%%)", p*100))
			} else {
				parts = append(parts, "subscription (no per-token cost)")
			}
		}
		if r, ok := rank[l]; ok {
			parts = append(parts, fmt.Sprintf("value rank #%d of %d for %s/%s", r, len(rows), c, d))
		} else {
			parts = append(parts, fmt.Sprintf("below the good-enough bar for %s/%s", c, d))
		}
		out[l] = strings.Join(parts, " · ")
	}
	return out
}

// calibrateRows enriches Scored rows with M5.2 calibrated estimates using the
// ledger's raw events. Callers hold b.mu.
func (b *brain) calibrateRows(_ captaincode.Class, d captaincode.Domain, rows []captaincode.Scored) []captaincode.Scored {
	now := time.Now()
	for i := range rows {
		rows[i].Calibration = captaincode.Calibrate(rows[i].Leg, d, b.ledger.Events, now)
	}
	return rows
}
