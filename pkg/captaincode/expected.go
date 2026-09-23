package captaincode

// Expected cost per successful task (stage 3). The value score trades
// quality for cost and latency on a fixed scale; it cannot say that a cheap
// leg which fails one time in three costs MORE than a dearer one that does
// not, once the repair is counted. This ranking can. For every (leg, effort)
// pair on the menu:
//
//	expected = cost + (1 − P) · repair
//
// where P is the success estimate (estimate.go), cost is the estimated
// spend at that effort (reasoning tokens are output tokens: effort scales
// it), and repair is what a second attempt one effort rung up would cost.
// A subscription leg has no per-token price, so its window pressure is
// priced as a share of the reference cost: a closing window is not free.
// Latency past the class's tolerance is a multiplier, not a term: a
// ten-minute answer to a one-line question is a failure of a different kind.
//
// The ROADMAP's rule, stated in M2's decision behavior, is what this
// implements: "a cheap initial attempt is not worthwhile if its expected
// repair cost exceeds starting with a stronger worker".

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// ExpectedRow is one (leg, effort) arm with its terms.
type ExpectedRow struct {
	Leg    Leg    `json:"leg"`
	Effort Effort `json:"effort"`
	Model  string `json:"model,omitempty"`

	P         float64 `json:"p"`                    // success estimate
	N         int     `json:"n,omitempty"`          // neighbours behind P
	PriorOnly bool    `json:"prior_only,omitempty"` // P is the feed prior alone

	CostUSD   float64 `json:"cost_usd"`             // this attempt, at this effort
	RepairUSD float64 `json:"repair_usd,omitempty"` // a second attempt one rung up
	Pressure  float64 `json:"pressure,omitempty"`   // subscription window pressure priced into CostUSD
	Expected  float64 `json:"expected"`             // per successful task

	LatencyMs   int64 `json:"latency_ms,omitempty"`
	LatencyOver bool  `json:"latency_over,omitempty"` // past the class's tolerance

	Excluded string `json:"excluded,omitempty"`
}

// Eligible reports whether the arm may be dispatched to.
func (r ExpectedRow) Eligible() bool { return r.Excluded == "" }

// LatencyTolerance is how long a class may reasonably take; past it the
// expected cost is scaled up. CAPTAIN_LATENCY_TOL="trivial,medium,high" as
// Go durations overrides (default 90s, 10m, 30m).
func LatencyTolerance(c Class) time.Duration {
	trivial, medium, high := 90*time.Second, 10*time.Minute, 30*time.Minute
	if v := os.Getenv("CAPTAIN_LATENCY_TOL"); v != "" {
		f := strings.Split(v, ",")
		if len(f) == 3 {
			if d, err := time.ParseDuration(strings.TrimSpace(f[0])); err == nil && d > 0 {
				trivial = d
			}
			if d, err := time.ParseDuration(strings.TrimSpace(f[1])); err == nil && d > 0 {
				medium = d
			}
			if d, err := time.ParseDuration(strings.TrimSpace(f[2])); err == nil && d > 0 {
				high = d
			}
		}
	}
	switch c {
	case ClassTrivial:
		return trivial
	case ClassHigh:
		return high
	}
	return medium
}

// SuccessFloor is the least P an arm must carry to be dispatched to on the
// expected-cost path: a leg that is more likely than not to need a redo is
// not a first attempt, whatever it costs. CAPTAIN_SUCCESS_FLOOR="t,m,h"
// overrides (default 0.30, 0.45, 0.55).
func SuccessFloor(c Class) float64 {
	trivial, medium, high := 0.30, 0.45, 0.55
	if v := os.Getenv("CAPTAIN_SUCCESS_FLOOR"); v != "" {
		f := strings.Split(v, ",")
		if len(f) == 3 {
			fmt.Sscanf(strings.TrimSpace(f[0]), "%g", &trivial)
			fmt.Sscanf(strings.TrimSpace(f[1]), "%g", &medium)
			fmt.Sscanf(strings.TrimSpace(f[2]), "%g", &high)
		}
	}
	switch c {
	case ClassTrivial:
		return trivial
	case ClassHigh:
		return high
	}
	return medium
}

// ExpectedInput is everything the ranking reads.
type ExpectedInput struct {
	Task       string
	Class      Class
	Domain     Domain
	Legs       []Leg              // the eligible menu (hard constraints already applied)
	Efforts    func(Leg) []Effort // the effort rungs each leg may run at (nil → one rung from DecideEffort)
	Estimator  *SuccessEstimator  // nil → priors only
	Stats      map[Leg]LegStats   // for observed latency
	Tokens     int                // sizes the cost term
	Pressure   func(Leg) float64  // subscription window pressure 0..1 (nil → 0)
	MidTierP   float64            // the decision leg's mid-tier estimate, 0 when unknown
	Now        time.Time
	BaseEffort Effort // the per-task effort decision, the rung a nil Efforts uses
}

// ExpectedRank scores every (leg, effort) arm, cheapest expected cost per
// successful task first. Arms under the class's success floor are kept,
// marked and sorted last, the way ValueRank keeps its exclusions.
func ExpectedRank(in ExpectedInput) []ExpectedRow {
	floor := SuccessFloor(in.Class)
	tol := LatencyTolerance(in.Class)
	cRef := costRef()
	var rows []ExpectedRow
	for _, l := range in.Legs {
		efforts := []Effort{in.BaseEffort}
		if in.Efforts != nil {
			if e := in.Efforts(l); len(e) > 0 {
				efforts = e
			}
		}
		st := in.Stats[l]
		for _, e := range efforts {
			est := SuccessEstimate{P: EffortPrior(l, e), PriorOnly: true}
			if in.Estimator != nil {
				est = in.Estimator.Estimate(in.Task, in.Class, in.Domain, l, e, in.MidTierP, in.Now)
			} else if in.MidTierP > 0 && l != LegClaude && !IsFrontierClass(l) {
				est.P = 0.5*est.P + 0.5*in.MidTierP
			}
			est.Prior = EffortPrior(l, e)
			row := ExpectedRow{Leg: l, Effort: e, Model: ModelIDAt(l, e), P: est.P, N: est.N, PriorOnly: est.PriorOnly, LatencyMs: st.AvgDurationMs}
			cost := EstimateCost(l, in.Tokens) * e.CostMultiplier()
			if sp, ok := specs[l]; ok && sp.Subscription && in.Pressure != nil {
				row.Pressure = in.Pressure(l)
				cost += row.Pressure * cRef // a closing window priced against the reference
			}
			row.CostUSD = cost
			next := NextEffort(e)
			row.RepairUSD = EstimateCost(l, in.Tokens)*next.CostMultiplier() + row.Pressure*cRef
			if row.RepairUSD == 0 {
				// A free leg's redo is not free to the user: their time. Price
				// it at the reference so P still matters on free legs.
				row.RepairUSD = cRef * 0.5
			}
			row.Expected = row.CostUSD + (1-row.P)*row.RepairUSD
			if tol > 0 && row.LatencyMs > 0 && time.Duration(row.LatencyMs)*time.Millisecond > tol {
				over := float64(row.LatencyMs) / float64(tol.Milliseconds())
				row.LatencyOver = true
				row.Expected *= over
			}
			if row.P < floor {
				row.Excluded = fmt.Sprintf("success estimate %.2f below the %s floor %.2f", row.P, in.Class, floor)
			}
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := rows[i].Eligible(), rows[j].Eligible(); a != b {
			return a
		}
		if rows[i].Expected != rows[j].Expected {
			return rows[i].Expected < rows[j].Expected
		}
		if rows[i].P != rows[j].P {
			return rows[i].P > rows[j].P
		}
		return rows[i].CostUSD < rows[j].CostUSD
	})
	return rows
}

// EligibleExpected filters to the arms that may run.
func EligibleExpected(rows []ExpectedRow) []ExpectedRow {
	out := make([]ExpectedRow, 0, len(rows))
	for _, r := range rows {
		if r.Eligible() {
			out = append(out, r)
		}
	}
	return out
}

// BurnPressure is a subscription leg's window pressure from its burn rate:
// the wall-clock the leg spent answering inside the trailing window, against
// the allowance the window is assumed to hold. 0 when idle, 1 at or past the
// allowance. This is what replaces the rate-limit flag (1 when cooling, 0.5
// after a recent limit, 0 otherwise) as the quota term: a window that is
// eighty percent burnt is under pressure before the provider says so.
func BurnPressure(events []Event, leg Leg, now time.Time, window, allowance time.Duration) float64 {
	if allowance <= 0 || window <= 0 {
		return 0
	}
	var spent time.Duration
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if now.Sub(e.At) > window {
			break
		}
		if e.Leg != leg {
			continue
		}
		spent += time.Duration(e.Duration) * time.Millisecond
	}
	p := float64(spent) / float64(allowance)
	if p > 1 {
		p = 1
	}
	if p < 0 {
		p = 0
	}
	return p
}

// WindowAllowance is the worker wall-clock a subscription window is assumed
// to hold before it closes. CAPTAIN_WINDOW_ALLOWANCE (Go duration, default
// 90m per 5h window) - a conservative reading of the Max/Pro windows.
func WindowAllowance() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("CAPTAIN_WINDOW_ALLOWANCE"))); err == nil && d > 0 {
		return d
	}
	return 90 * time.Minute
}

// FormatExpected renders the arms for `captain why`.
func FormatExpected(rows []ExpectedRow, chosen Leg, chosenEffort Effort) string {
	var sb strings.Builder
	for i, r := range rows {
		mark := " "
		if r.Leg == chosen && r.Effort == chosenEffort {
			mark = "→"
		}
		if !r.Eligible() {
			fmt.Fprintf(&sb, "      ✗ %-8s %-6s %s\n", r.Leg, r.Effort, r.Excluded)
			continue
		}
		ev := fmt.Sprintf("%d nbrs", r.N)
		if r.PriorOnly {
			ev = "prior only"
		}
		lat := ""
		if r.LatencyOver {
			lat = " over-tolerance"
		}
		fmt.Fprintf(&sb, "    %s #%d %-8s %-6s E=$%.3f  p=%.2f (%s)  cost=$%.3f repair=$%.3f%s\n",
			mark, i+1, r.Leg, r.Effort, r.Expected, r.P, ev, r.CostUSD, r.RepairUSD, lat)
	}
	return sb.String()
}
