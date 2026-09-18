package captaincode

// Value routing (MM37 phase 3): which leg runs an un-prefixed task is decided
// by a score, not a hand-written list - ROUTING.md §5 with registry data:
//
//	value = w_q·quality/10 − w_c·cost − w_l·latency      (each term 0..1)
//
// quality is the per-domain prior blended with local scored runs in that
// domain; cost is the estimated $ for this task against an absolute reference
// (subscription legs carry window PRESSURE instead - an observed rate limit
// is the only usage signal we have); latency is the leg's median duration
// against a reference. A good-enough threshold τ(class) on quality keeps
// "cheap but predictably bad" first attempts off the ladder.

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cost and latency are normalized against ABSOLUTE references, not the
// priciest candidate: normalizing by the max gave a lone $0.02 model the full
// penalty while a subscription leg under pressure paid the same - so grok's
// closing window still beat qwen (found by the first test run).
// CAPTAIN_VALUE_COST_REF ($ per task, default 0.50) and
// CAPTAIN_VALUE_LAT_REF (ms, default 600000 = 10 min) override.
func costRef() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("CAPTAIN_VALUE_COST_REF"), 64); err == nil && v > 0 {
		return v
	}
	return 0.50
}

func latRef() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("CAPTAIN_VALUE_LAT_REF"), 64); err == nil && v > 0 {
		return v
	}
	return 600_000
}

// ValueWeights are the term weights. Per class (ROUTING.md §5: "tune per
// task-class"): trivial work weighs cost over quality - a typo fix must not
// buy a $0.05 model when a free one clears the bar - medium work weighs
// quality first. CAPTAIN_VALUE_WEIGHTS="q,c,l" overrides medium/high,
// CAPTAIN_VALUE_WEIGHTS_TRIVIAL="q,c,l" overrides trivial.
type ValueWeights struct{ Q, C, L float64 }

func valueWeights(c Class) ValueWeights {
	w, env := ValueWeights{Q: 0.5, C: 0.3, L: 0.1}, "CAPTAIN_VALUE_WEIGHTS"
	if c == ClassTrivial {
		w, env = ValueWeights{Q: 0.3, C: 0.6, L: 0.1}, "CAPTAIN_VALUE_WEIGHTS_TRIVIAL"
	}
	if v := os.Getenv(env); v != "" {
		f := strings.Split(v, ",")
		if len(f) == 3 {
			q, e1 := strconv.ParseFloat(strings.TrimSpace(f[0]), 64)
			c, e2 := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
			l, e3 := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
			if e1 == nil && e2 == nil && e3 == nil {
				w = ValueWeights{Q: q, C: c, L: l}
			}
		}
	}
	return w
}

// GoodEnough is the quality threshold τ a leg must clear to be a candidate
// for a class on the cheap path. CAPTAIN_VALUE_TAU="trivial,medium" overrides.
//
// Defaults 5.5 / 7.0 are calibrated to the LEADERBOARD scale (claude = 9.5,
// intelligence-index scaled): synced priors sit lower than the hand-written
// ones did - grok lands ~5.4, gemini ~8 - and 7.5 would have benched every
// budget leg on medium work.
func GoodEnough(c Class) float64 {
	trivial, medium := 5.5, 7.0
	if v := os.Getenv("CAPTAIN_VALUE_TAU"); v != "" {
		f := strings.Split(v, ",")
		if len(f) == 2 {
			if t, err := strconv.ParseFloat(strings.TrimSpace(f[0]), 64); err == nil {
				trivial = t
			}
			if m, err := strconv.ParseFloat(strings.TrimSpace(f[1]), 64); err == nil {
				medium = m
			}
		}
	}
	switch c {
	case ClassTrivial:
		return trivial
	case ClassHigh:
		return medium
	}
	return medium
}

// EstimateCost is the $ a task of roughly tokens tokens costs on a leg: 0 for
// subscription and free legs, price × tokens for API legs, assuming the usual
// 3:1 input:output split when only a total is known.
func EstimateCost(l Leg, tokens int) float64 {
	s, ok := specs[l]
	if !ok || s.Subscription || (s.PriceIn == 0 && s.PriceOut == 0) || tokens <= 0 {
		return 0
	}
	in := float64(tokens) * 0.75
	out := float64(tokens) * 0.25
	return (in*s.PriceIn + out*s.PriceOut) / 1e6
}

// Scored is one candidate with its terms, for logs and the director's menu.
//
// The evidence fields below exist for `captain why` (ROADMAP M2.1): a ranking
// that shows only the winning number cannot be argued with. Samples/ScoredRuns
// say how much local evidence stands behind Quality (5 pseudo-observations of
// prior otherwise - see priorWeight), LastRun says how old it is, and Excluded
// names why a candidate lost outright rather than on points.
type Scored struct {
	Leg        Leg
	Quality    float64 // blended, 0..10
	CostUSD    float64 // estimated for this task (0 for subscription legs)
	Pressure   float64 // subscription window pressure 0..1 (0 for API legs)
	LatencyMs  int64
	Value      float64
	Unreliable bool // failed ≥⅓ of its runs (provider faults, ≥2) - ranked last whatever its value

	Samples    int       // ok runs on the ledger backing the latency/cost observations
	ScoredRuns int       // of those, how many carry a quality assessment
	Prior      float64   // the benchmark prior Quality was blended from
	LastRun    time.Time // freshness of the evidence; zero when the leg has never run here
	Excluded   string    // why this candidate is not eligible; empty when it is

	Calibration Calibration // M5.2: calibrated quality/reliability with uncertainty and ageing
}

// Eligible reports whether this candidate may actually be dispatched to.
func (s Scored) Eligible() bool { return s.Excluded == "" }

// Eligible filters a ranking to the candidates that may run.
func Eligible(rows []Scored) []Scored {
	out := make([]Scored, 0, len(rows))
	for _, r := range rows {
		if r.Eligible() {
			out = append(out, r)
		}
	}
	return out
}

// Unreliable is the reroute net's reliability gate, applied to the cheap path
// too: a leg with ≥2 provider-fault failures making up a third or more of its
// runs sorts after every reliable candidate. Live 2026-09-10: minimax (NIM,
// $0, prior 7.5, 0 successes and 2 fails on the ledger) topped every value
// ranking on cost alone.
func Unreliable(s LegStats) bool {
	total := s.N + s.Fails
	return s.Fails >= 2 && total > 0 && s.Fails*3 >= total
}

// ValueRank scores candidates for (class, domain) and returns them best
// first, dropping any below the good-enough threshold. pressure reports a
// subscription leg's window pressure (nil → 0). estTokens sizes the cost term.
func ValueRank(c Class, d Domain, candidates []Leg, stats map[Leg]LegStats, estTokens int, pressure func(Leg) float64) []Scored {
	w := valueWeights(c)
	tau := GoodEnough(c)
	var rows []Scored
	cRef, lRef := costRef(), latRef()
	for _, l := range candidates {
		s := stats[l]
		q := BlendedQualityFor(l, s, d)
		row := Scored{Leg: l, Quality: q, CostUSD: EstimateCost(l, estTokens), LatencyMs: s.AvgDurationMs, Unreliable: Unreliable(s),
			Samples: s.N, ScoredRuns: s.Scored, Prior: QualityPriorFor(l, d), LastRun: s.LastAt}
		// Below τ is an exclusion, not an absence: the row is kept, marked and
		// sorted last, so `captain why` can say which legs were considered and
		// on what number they were ruled out (M2.1). Legs() drops them, so the
		// try-order the router walks is unchanged.
		if q < tau {
			row.Excluded = fmt.Sprintf("quality %.1f below the %s good-enough bar %.1f", q, c, tau)
		}
		if sp, ok := specs[l]; ok && sp.Subscription && pressure != nil {
			row.Pressure = pressure(l)
		}
		costNorm := row.Pressure
		if row.CostUSD > 0 {
			costNorm = row.CostUSD / cRef
			if costNorm > 1 {
				costNorm = 1
			}
		}
		latNorm := 0.0
		if row.LatencyMs > 0 {
			latNorm = float64(row.LatencyMs) / lRef
			if latNorm > 1 {
				latNorm = 1
			}
		}
		row.Value = w.Q*row.Quality/10 - w.C*costNorm - w.L*latNorm
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := rows[i].Eligible(), rows[j].Eligible(); a != b {
			return a // excluded candidates are reported, never ranked among the eligible
		}
		if rows[i].Unreliable != rows[j].Unreliable {
			return !rows[i].Unreliable // reliable legs first, always
		}
		if rows[i].Value != rows[j].Value {
			return rows[i].Value > rows[j].Value
		}
		return rows[i].CostUSD < rows[j].CostUSD // ties: cheaper first
	})
	return rows
}

// Legs flattens a ranking to its leg order.
func Legs(rows []Scored) []Leg {
	out := make([]Leg, 0, len(rows))
	for _, r := range rows {
		if r.Eligible() {
			out = append(out, r.Leg)
		}
	}
	return out
}
