package captaincode

// Calibrated quality and reliability estimates (ROADMAP M5.2).
//
// The existing BlendedQuality does simple shrinkage: prior counts as
// priorWeight (5) pseudo-observations, local scored runs add their actual
// count, and the weighted mean is the estimate. That is a point estimate
// with no uncertainty, no ageing and no reliability signal — a leg with two
// scored runs at q9.0 reads the same as one with forty, and a leg whose
// scores are three weeks old reads the same as one scored yesterday.
//
// Calibration adds what the point estimate hides:
//
//   - a 95% confidence interval on quality, so `captain why` can say
//     "8.2 ± 0.4 (n=12)" rather than a bare 8.2
//   - a reliability estimate (success rate) with its own interval, separate
//     from quality because a leg can score well on the runs that succeed and
//     still fail half the time
//   - exponential time-decay: a run from t days ago counts as
//     exp(-t/halfLife) of a fresh run, so evidence goes stale gracefully
//     rather than being either fully trusted or forgotten
//   - an effective sample size (sum of the decay weights) so the CI widens
//     as evidence ages even when the raw count stays the same
//   - the fallback prior, named in the record so a reader knows when the
//     estimate is mostly prior rather than observation
//
// The calibration is computed from the Events the ledger already stores; no
// new persistence is needed. It is a read at decision time, not a write on
// every run.

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

// Calibration is a calibrated quality/reliability estimate for one leg in one
// (class, domain) context, with the uncertainty and evidence age the point
// estimate does not carry.
type Calibration struct {
	Quality       float64       `json:"quality"`        // point estimate, 0-10 (blended prior + decayed local)
	CILower       float64       `json:"ci_lower"`       // 95% lower bound on quality
	CIUpper       float64       `json:"ci_upper"`       // 95% upper bound on quality
	SampleSize    int           `json:"sample_size"`    // raw scored observation count
	EffectiveN    float64       `json:"effective_n"`    // time-decayed effective sample size
	Reliability   float64       `json:"reliability"`    // success rate 0-1 (ok / total, decayed)
	ReliabilityN  int           `json:"reliability_n"`  // total attempts (ok + fail, raw)
	ReliabilityCI string        `json:"reliability_ci"` // Wilson 95% interval on reliability, human-readable
	Age           time.Duration `json:"age"`            // since the most recent observation of any outcome
	Prior         float64       `json:"prior"`          // the fallback prior used when SampleSize is small
	Stale         bool          `json:"stale"`          // EffectiveN < staleThreshold (evidence is mostly decayed)
}

// CalibHalfLife is the age at which an observation counts as half a fresh one.
// CAPTAIN_CALIB_HALFLIFE_DAYS overrides (default 14).
func CalibHalfLife() float64 { return calibHalfLife() }

func calibHalfLife() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("CAPTAIN_CALIB_HALFLIFE_DAYS"), 64); err == nil && v > 0 {
		return v
	}
	return 14
}

// StaleThreshold is the effective sample size below which evidence is flagged
// stale (the estimate is mostly prior). CAPTAIN_CALIB_STALE overrides.
func StaleThreshold() float64 { return staleThreshold() }

func staleThreshold() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("CAPTAIN_CALIB_STALE"), 64); err == nil && v > 0 {
		return v
	}
	return 3
}

// z95 is the z-score for a 95% two-sided confidence interval.
const z95 = 1.959964

// Calibrate produces a calibrated estimate for one leg from its events in the
// given domain. events is the raw ledger Events slice. now is injected for
// testing.
func Calibrate(leg Leg, d Domain, events []Event, now time.Time) Calibration {
	prior := QualityPriorFor(leg, d)
	halfLife := calibHalfLife()

	type obs struct {
		q       float64
		at      time.Time
		success bool
	}
	var scored []obs
	var allAttempts []time.Time
	var lastAt time.Time

	for _, e := range events {
		if e.Leg != leg {
			continue
		}
		if d != "" && e.Domain != "" && Domain(e.Domain) != d {
			continue
		}
		if !e.At.IsZero() && e.At.After(lastAt) {
			lastAt = e.At
		}
		allAttempts = append(allAttempts, e.At)
		if e.Outcome != "ok" {
			continue
		}
		if e.Quality > 0 {
			scored = append(scored, obs{q: e.Quality, at: e.At, success: true})
		}
	}

	if len(scored) == 0 {
		return Calibration{
			Quality: prior,
			CILower: prior,
			CIUpper: prior,
			Prior:   prior,
			Stale:   true,
			Age:     ageSince(lastAt, now),
		}
	}

	var wSum, wqSum float64
	for _, o := range scored {
		w := decayWeight(o.at, now, halfLife)
		wSum += w
		wqSum += w * o.q
	}

	effN := wSum
	priorW := float64(priorWeight) * priorDecayFactor(effN)
	quality := (prior*priorW + wqSum) / (priorW + effN)

	variance := 0.0
	for _, o := range scored {
		w := decayWeight(o.at, now, halfLife)
		variance += w * (o.q - quality) * (o.q - quality)
	}
	variance /= effN
	se := math.Sqrt(variance / effN)
	if math.IsNaN(se) || se <= 0 {
		se = 2.0
	}
	ciLower := clamp(quality-z95*se, 0, 10)
	ciUpper := clamp(quality+z95*se, 0, 10)

	rN := len(allAttempts)
	var rwSum, rSuccess float64
	for _, at := range allAttempts {
		w := decayWeight(at, now, halfLife)
		rwSum += w
	}
	for _, e := range events {
		if e.Leg != leg {
			continue
		}
		if d != "" && e.Domain != "" && Domain(e.Domain) != d {
			continue
		}
		if e.Outcome == "ok" {
			rSuccess += decayWeight(e.At, now, halfLife)
		}
	}
	reliability := 1.0
	if rwSum > 0 {
		reliability = rSuccess / rwSum
	}
	rCI := wilsonInterval(reliability, rN)

	age := ageSince(lastAt, now)
	stale := effN < staleThreshold()

	return Calibration{
		Quality:       quality,
		CILower:       ciLower,
		CIUpper:       ciUpper,
		SampleSize:    len(scored),
		EffectiveN:    effN,
		Reliability:   reliability,
		ReliabilityN:  rN,
		ReliabilityCI: rCI,
		Age:           age,
		Prior:         prior,
		Stale:         stale,
	}
}

// decayWeight is the exponential time-decay weight for an observation at time
// at, relative to now, with the given halfLife in days.
func decayWeight(at time.Time, now time.Time, halfLifeDays float64) float64 {
	if at.IsZero() {
		return 0
	}
	days := now.Sub(at).Hours() / 24
	if days <= 0 {
		return 1
	}
	return math.Exp(-days * math.Ln2 / halfLifeDays)
}

// priorDecayFactor reduces the prior's weight as effective local evidence
// grows, so the prior dominates at n=0 and is a tie-breaker by n=20.
func priorDecayFactor(effN float64) float64 {
	if effN >= 20 {
		return 0.5
	}
	return 1.0 - effN*0.025
}

func ageSince(last time.Time, now time.Time) time.Duration {
	if last.IsZero() {
		return 0
	}
	d := now.Sub(last)
	if d < 0 {
		return 0
	}
	return d
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// wilsonInterval returns a human-readable 95% Wilson score interval for a
// proportion, robust at small n where the normal approximation is wrong.
func wilsonInterval(p float64, n int) string {
	if n == 0 {
		return "?"
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	nf := float64(n)
	denom := 1 + z95*z95/nf
	center := (p + z95*z95/(2*nf)) / denom
	margin := z95 * math.Sqrt(p*(1-p)/nf+z95*z95/(4*nf*nf)) / denom
	lo := clamp(center-margin, 0, 1)
	hi := clamp(center+margin, 0, 1)
	return fmt.Sprintf("%.0f%%–%.0f%%", lo*100, hi*100)
}

// FormatCalibration renders the estimate for `captain why` and `captain stats`.
func (c Calibration) Format() string {
	if c.SampleSize == 0 {
		s := fmt.Sprintf("%.1f (prior, no local evidence", c.Quality)
		if c.Age > 0 {
			s += fmt.Sprintf(", last run %s ago", c.Age.Round(24*time.Hour))
		}
		return s + ")"
	}
	s := fmt.Sprintf("%.1f [%.1f–%.1f] (n=%d, eff=%.1f", c.Quality, c.CILower, c.CIUpper, c.SampleSize, c.EffectiveN)
	if c.Stale {
		s += ", stale"
	}
	if c.Age > 0 {
		s += fmt.Sprintf(", age %s", c.Age.Round(24*time.Hour))
	}
	s += fmt.Sprintf(", reliability %s on %d)", c.ReliabilityCI, c.ReliabilityN)
	return s
}
