package captaincode

// What a leg's quality number is made of ("Twelve Weeks of Routing",
// 2026-10-10). Three findings changed it:
//
//   - A grade from the worker's own vendor is not evidence. A Claude
//     director graded Claude's work; a score now counts in full only when a
//     judge from another vendor gave it, half when nobody recorded who gave
//     it (the director, before judges), and not at all when the grader
//     shares the worker's vendor.
//   - Acceptance sat at 96-99% for every busy leg: "accepted" mostly meant
//     the user committed or said nothing. The negative outcomes separate
//     legs - failed checks, corrective re-prompts, a reviewer's rejection, a
//     regression - so a leg's rejected share of its informative outcomes
//     lowers its blended quality, and silence is no longer a label.
//   - The benchmark priors overrated the frontier legs against what the
//     judges measured here (codex-cli 9.3 against 7.2). Once a week each
//     prior with enough cross-vendor evidence moves part of the way toward
//     its measured mean.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// qualityWeight is how much a run's quality score counts.
func qualityWeight(e Event) float64 {
	if e.Quality <= 0 {
		return 0
	}
	if e.Judge == "" {
		return 0.5 // graded by the director, or before graders were recorded
	}
	if VendorOf(e.Judge) == VendorOf(e.Leg) {
		return 0 // the worker's own vendor graded it
	}
	return 1
}

// PickJudges is the judge panel for a run by worker: up to n legs from
// distinct vendors, none the worker's, strongest first (PickJudge's rules).
func PickJudges(worker Leg, open func(Leg) bool, n int) []Leg {
	var out []Leg
	vendors := map[string]bool{VendorOf(worker): true}
	for len(out) < n {
		j, ok := PickJudge(worker, func(l Leg) bool {
			if vendors[VendorOf(l)] {
				return false
			}
			return open == nil || open(l)
		})
		if !ok {
			break
		}
		out = append(out, j)
		vendors[VendorOf(j)] = true
	}
	return out
}

// informativeOutcome: an outcome that says something about the work. Silence
// and a task that never reached delivery do not.
func informativeOutcome(o OutcomeEvidence) bool {
	if !o.Settled() {
		return false
	}
	switch o.DecidedBy {
	case DecidedBySilence, DecidedByLifecycle:
		return false
	}
	return true
}

// negativeOutcome: the work was turned down - a failed check, a corrective
// re-prompt, a reviewer's rejection, a regression.
func negativeOutcome(o OutcomeEvidence) bool {
	return o.Status == AcceptanceRejected || o.Status == AcceptanceRegressed
}

// outcomeMinDecided is how many informative outcomes a leg needs before its
// rejected share moves its quality.
const outcomeMinDecided = 5

// outcomePenaltyScale: quality points per unit of rejected share. ds4-flash,
// 3 rejected of 10, loses 1.2; claude, 3 of 109, 0.1.
const outcomePenaltyScale = 4.0

// OutcomePenalty is what a leg's rejected outcomes take off its quality.
func OutcomePenalty(s LegStats) float64 {
	if s.Decided < outcomeMinDecided {
		return 0
	}
	return outcomePenaltyScale * float64(s.Rejected) / float64(s.Decided)
}

// foldOutcomes adds each leg's informative and rejected outcomes (the latest
// state of each task) to its stats.
func foldOutcomes(stats map[Leg]LegStats, outcomes []OutcomeEvidence) {
	latest := map[string]OutcomeEvidence{}
	var order []string
	for _, o := range outcomes {
		if o.TaskID == "" || o.Leg == "" {
			continue
		}
		if _, seen := latest[o.TaskID]; !seen {
			order = append(order, o.TaskID)
		}
		latest[o.TaskID] = o
	}
	for _, id := range order {
		o := latest[id]
		if !informativeOutcome(o) {
			continue
		}
		st := stats[o.Leg]
		st.Decided++
		if negativeOutcome(o) {
			st.Rejected++
		}
		stats[o.Leg] = st
	}
}

// Prior refit.

// refitMinScored is the cross-vendor-weighted evidence a leg needs before
// its prior moves.
const refitMinScored = 20

// refitRate is the share of the gap between prior and measured mean closed
// per refit, and refitMaxStep the most one refit moves a prior.
const (
	refitRate    = 0.3
	refitMaxStep = 0.5
)

// refitEvery is how often the brain refits (CAPTAIN_PRIOR_REFIT, a Go
// duration, "0" off; default a week).
func refitEvery() time.Duration {
	v := os.Getenv("CAPTAIN_PRIOR_REFIT")
	if v == "0" || v == "off" {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return 7 * 24 * time.Hour
}

// PriorChange is one leg's refit.
type PriorChange struct {
	Leg      Leg     `json:"leg"`
	From     float64 `json:"from"`
	To       float64 `json:"to"`
	Measured float64 `json:"measured"`
	Scored   int     `json:"scored"`
}

// ProposeRefit is the refit the stats call for: every leg with
// refitMinScored weighted scores moves refitRate of the way from its prior
// toward its measured mean, at most refitMaxStep.
func ProposeRefit(stats map[Leg]LegStats) []PriorChange {
	var out []PriorChange
	for _, l := range AllLegs {
		s := stats[l]
		p := QualityPrior(l)
		if s.Scored < refitMinScored || p <= 0 {
			continue
		}
		step := refitRate * (s.AvgQuality - p)
		step = math.Max(-refitMaxStep, math.Min(refitMaxStep, step))
		to := math.Round((p+step)*10) / 10
		if to == p {
			continue
		}
		out = append(out, PriorChange{Leg: l, From: p, To: to, Measured: math.Round(s.AvgQuality*100) / 100, Scored: s.Scored})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Leg < out[j].Leg })
	return out
}

// refitLogPath records each applied refit, and when the last one ran.
func refitLogPath(priors string) string {
	return filepath.Join(filepath.Dir(priors), "priors-refit.jsonl")
}

// ApplyRefit writes the changes into the overrides file at path, shifting
// every domain value of a leg by its change, and reloads the priors. The
// file keeps its format; a leg it did not list gains an "all" entry.
func ApplyRefit(path string, changes []PriorChange, now time.Time) error {
	if len(changes) == 0 {
		return nil
	}
	raw := map[Leg]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, c := range changes {
		delta := c.To - c.From
		var dp DomainPriors
		if v, ok := raw[c.Leg]; ok && json.Unmarshal(v, &dp) == nil && len(dp) > 0 {
			for k, val := range dp {
				dp[k] = math.Round((val+delta)*10) / 10
			}
		} else {
			dp = DomainPriors{"all": c.To}
		}
		b, _ := json.Marshal(dp)
		raw[c.Leg] = b
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if f, err := os.OpenFile(refitLogPath(path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		line, _ := json.Marshal(struct {
			At      time.Time     `json:"at"`
			Changes []PriorChange `json:"changes"`
		}{now, changes})
		_, _ = f.Write(append(line, '\n'))
		f.Close()
	}
	_, err = LoadPriorOverridesFrom(path)
	return err
}

// LastRefit is when the priors were last refitted (zero: never).
func LastRefit(priors string) time.Time {
	data, err := os.ReadFile(refitLogPath(priors))
	if err != nil {
		return time.Time{}
	}
	var last time.Time
	for _, line := range splitLines(data) {
		var r struct {
			At time.Time `json:"at"`
		}
		if json.Unmarshal(line, &r) == nil && r.At.After(last) {
			last = r.At
		}
	}
	return last
}

// RefitDue: the brain refits when refitEvery has passed since the last one.
func RefitDue(priors string, now time.Time) bool {
	every := refitEvery()
	return every > 0 && now.Sub(LastRefit(priors)) >= every
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
