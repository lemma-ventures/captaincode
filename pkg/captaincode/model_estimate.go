package captaincode

// One estimate per model (SCORING.md Phase 2).
//
// For a leg, then the model version it ran, then the effort it ran at, the
// estimator holds two things:
//
//   - P(accepted without rework): a Beta posterior. The leg level starts
//     from the benchmark (EffortPrior, shifted to the observed success
//     level) worth benchmarkWeight observations; each lower level starts
//     from its parent's mean worth poolWeight observations. A version with
//     three runs therefore borrows from its leg instead of standing on
//     three runs - splitting by version and domain directly left most cells
//     with 0-2 grades.
//   - Run time: a log-normal, its mean shrunk the same way from leg to
//     version to effort.
//
// Evidence decays with a 30-day half-life, not with a count cap. Code and
// prose are estimated apart at the leg level once each has domainMinWeight
// of evidence; below that they pool. A commit shared by several tasks
// credits each 1/Shared. A judge's pass/fail counts as a weak observation,
// weighted by how often that judge agreed with tests it could be checked
// against.

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// Estimator tunables.
const (
	benchmarkWeight   = 2.5  // observations the benchmark prior is worth at the leg level
	poolWeight        = 3.0  // observations a parent level lends its child
	evidenceHalfLife  = 30.0 // days
	domainMinWeight   = 10.0 // decayed observations before code and prose split
	judgeWeightFloor  = 0.1  // a judge not yet checked against tests
	judgeWeightMax    = 0.3  // a judge that agrees with tests every time
	judgeCheckMin     = 10   // judge verdicts with a test verdict beside them before agreement counts
	timeSigmaFloor    = 0.3  // log-ms: no leg is more predictable than this
	defaultRunMinutes = 5.0  // a leg with no timed run at all
)

// ModelKey is the identity an estimate is held under.
type ModelKey struct {
	Leg    Leg
	Model  string
	Effort Effort
}

// ModelEstimate is the posterior for one (leg, model, effort) on a domain.
type ModelEstimate struct {
	Key ModelKey
	// A, B: Beta posterior for P(accepted without rework).
	A, B float64
	// N is the decayed evidence at the key itself (0 = pure borrowing).
	N float64
	// LogMu, LogSigma: run time in log milliseconds.
	LogMu, LogSigma float64
}

// P is the posterior mean.
func (e ModelEstimate) P() float64 { return e.A / (e.A + e.B) }

// Minutes is the expected run time.
func (e ModelEstimate) Minutes() float64 { return math.Exp(e.LogMu) / 60000 }

// SampleP draws from the posterior.
func (e ModelEstimate) SampleP(rng *rand.Rand) float64 { return betaSample(rng, e.A, e.B) }

// Uncertain says the estimate rests mostly on borrowing: less than ten
// observations of its own, or a 90% interval wider than 0.3.
func (e ModelEstimate) Uncertain() bool {
	if e.N < 10 {
		return true
	}
	n := e.A + e.B
	sd := math.Sqrt(e.A * e.B / (n * n * (n + 1)))
	return 3.3*sd > 0.3
}

type modelObs struct {
	leg     Leg
	model   string
	effort  Effort
	code    bool
	success bool
	w       float64
}

type timeObs struct {
	leg    Leg
	model  string
	effort Effort
	logMs  float64
	w      float64
}

// ModelEstimator holds the evidence the estimates are computed from.
type ModelEstimator struct {
	obs   []modelObs
	times []timeObs
	shift float64
	judge map[Leg]float64 // weight each judge's verdict carries
}

// NewModelEstimator reads labelled outcomes and judge verdicts from the
// routing history and run times from the events.
func NewModelEstimator(hist []RoutingSample, events []Event, now time.Time) *ModelEstimator {
	e := &ModelEstimator{judge: judgeWeights(hist)}
	var est []estSample
	for _, s := range hist {
		leg, model, effort, ok := sampleIdentity(s)
		if !ok {
			continue
		}
		at := s.Decision.At
		if at.IsZero() && s.Outcome != nil {
			at = s.Outcome.UpdatedAt
		}
		w := decay(now, at)
		code := s.Decision.Domain == DomainCode
		if s.Labeled() {
			ow := w
			if c := s.Outcome.Commit; c != nil && c.Shared > 1 && s.Outcome.DecidedBy == DecidedByCommit {
				ow /= float64(c.Shared)
			}
			ok := s.Success() && s.Outcome.Rework == 0
			e.obs = append(e.obs, modelObs{leg: leg, model: model, effort: effort, code: code, success: ok, w: ow})
			est = append(est, estSample{leg: leg, effort: effort, success: ok})
			continue
		}
		for _, ev := range s.Events {
			if ev.Judge == "" {
				continue
			}
			jw := e.judge[ev.Judge]
			if jw == 0 {
				jw = judgeWeightFloor
			}
			e.obs = append(e.obs, modelObs{leg: leg, model: model, effort: effort, code: code, success: ev.JudgePass, w: w * jw})
		}
	}
	e.shift = calibrationShift(est)
	for _, ev := range events {
		if ev.Leg == "" || ev.Outcome != "ok" || ev.Duration <= 0 {
			continue
		}
		e.times = append(e.times, timeObs{leg: ev.Leg, model: ev.Model, effort: ev.Effort,
			logMs: math.Log(float64(ev.Duration)), w: decay(now, ev.At)})
	}
	return e
}

// sampleIdentity is the (leg, model, effort) a sample's delivering run had.
func sampleIdentity(s RoutingSample) (Leg, string, Effort, bool) {
	leg, model, effort := s.Decision.Chosen, "", s.Decision.Effort
	for _, ev := range s.Events {
		if ev.Leg == "" || ev.Outcome != "ok" {
			continue
		}
		leg, model = ev.Leg, ev.Model
		if ev.Effort != "" {
			effort = ev.Effort
		}
	}
	return leg, model, effort, leg != ""
}

// decay is the weight of evidence recorded at `at`.
func decay(now, at time.Time) float64 {
	if at.IsZero() {
		return 0.5
	}
	age := now.Sub(at).Hours() / 24
	if age <= 0 {
		return 1
	}
	return math.Pow(0.5, age/evidenceHalfLife)
}

// Estimate is the posterior for leg running model at effort on domain d.
// An empty model or effort borrows from the level above.
func (e *ModelEstimator) Estimate(leg Leg, model string, effort Effort, d Domain) ModelEstimate {
	code := d == DomainCode
	p0 := math.Min(0.97, math.Max(0.05, EffortPrior(leg, effort)+e.shift))

	legObs := e.filter(func(o modelObs) bool { return o.leg == leg })
	if sameDomain := filterObs(legObs, func(o modelObs) bool { return o.code == code }); weightOf(sameDomain) >= domainMinWeight {
		legObs = sameDomain
	}
	a, b := beta(p0, benchmarkWeight, legObs)

	n := 0.0
	if model != "" {
		mo := filterObs(legObs, func(o modelObs) bool { return o.model == model })
		// The version's own benchmark row, when the feed has it exactly,
		// moves its starting point by its gap from the leg's row; a
		// fallback lends less, so the version's evidence takes over sooner.
		// Neither resets evidence: the version still starts from its leg.
		mean, weight := a/(a+b), poolWeight
		if gap, exact := benchmarkGap(leg, model); exact {
			mean = math.Min(0.97, math.Max(0.05, mean+gap))
		} else {
			weight = poolWeight / 2
		}
		a, b = beta(mean, weight, mo)
		n = weightOf(mo)
		if effort != "" {
			eo := filterObs(mo, func(o modelObs) bool { return o.effort == effort })
			a, b = beta(a/(a+b), poolWeight, eo)
			n = weightOf(eo)
		}
	}
	mu, sigma := e.runTime(leg, model, effort)
	return ModelEstimate{Key: ModelKey{Leg: leg, Model: model, Effort: effort}, A: a, B: b, N: n, LogMu: mu, LogSigma: sigma}
}

func (e *ModelEstimator) filter(keep func(modelObs) bool) []modelObs { return filterObs(e.obs, keep) }

func filterObs(obs []modelObs, keep func(modelObs) bool) []modelObs {
	var out []modelObs
	for _, o := range obs {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

func weightOf(obs []modelObs) float64 {
	var w float64
	for _, o := range obs {
		w += o.w
	}
	return w
}

// beta is the posterior from a prior mean worth `weight` observations.
func beta(mean, weight float64, obs []modelObs) (float64, float64) {
	a, b := mean*weight, (1-mean)*weight
	for _, o := range obs {
		if o.success {
			a += o.w
		} else {
			b += o.w
		}
	}
	return a, b
}

// runTime is the log-normal for (leg, model, effort): each level's mean
// shrunk toward its parent's, the spread pooled at the leg level.
func (e *ModelEstimator) runTime(leg Leg, model string, effort Effort) (float64, float64) {
	global := math.Log(defaultRunMinutes * 60000)
	if m, _, w := moments(e.times); w > 0 {
		global = m
	}
	legT := filterTimes(e.times, func(t timeObs) bool { return t.leg == leg })
	mu, sigma, w := moments(legT)
	mu = shrink(global, mu, w)
	if model != "" {
		mt := filterTimes(legT, func(t timeObs) bool { return t.model == model })
		m, _, mw := moments(mt)
		mu = shrink(mu, m, mw)
		if effort != "" {
			et := filterTimes(mt, func(t timeObs) bool { return t.effort == effort })
			m, _, ew := moments(et)
			mu = shrink(mu, m, ew)
		}
	}
	if w < 2 {
		return mu, 1.0 // too few runs to know the spread
	}
	return mu, math.Max(sigma, timeSigmaFloor)
}

func filterTimes(ts []timeObs, keep func(timeObs) bool) []timeObs {
	var out []timeObs
	for _, t := range ts {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}

// moments is the weighted mean, standard deviation and total weight.
func moments(ts []timeObs) (float64, float64, float64) {
	var w, sum float64
	for _, t := range ts {
		w += t.w
		sum += t.w * t.logMs
	}
	if w == 0 {
		return 0, 0, 0
	}
	mean := sum / w
	var v float64
	for _, t := range ts {
		v += t.w * (t.logMs - mean) * (t.logMs - mean)
	}
	return mean, math.Sqrt(v / w), w
}

// shrink pulls an estimate with weight w toward its parent, which is worth
// poolWeight observations.
func shrink(parent, own, w float64) float64 {
	if w <= 0 {
		return parent
	}
	return (parent*poolWeight + own*w) / (poolWeight + w)
}

// judgeWeights is the weight each judge's verdict carries: judgeWeightFloor
// until judgeCheckMin of its verdicts sit beside a labelling test check,
// then up to judgeWeightMax in proportion to how often it agreed.
func judgeWeights(hist []RoutingSample) map[Leg]float64 {
	agree, total := map[Leg]float64{}, map[Leg]float64{}
	for _, s := range hist {
		if s.Outcome == nil {
			continue
		}
		var test *CheckResult
		for i := range s.Outcome.Checks {
			if c := s.Outcome.Checks[i]; c.Source == "tests" && c.Labels() {
				test = &s.Outcome.Checks[i]
			}
		}
		if test == nil {
			continue
		}
		for _, ev := range s.Events {
			if ev.Judge == "" {
				continue
			}
			total[ev.Judge]++
			if ev.JudgePass == test.Passed {
				agree[ev.Judge]++
			}
		}
	}
	out := map[Leg]float64{}
	for j, n := range total {
		if n < judgeCheckMin {
			out[j] = judgeWeightFloor
			continue
		}
		rate := agree[j] / n
		// Agreement at chance (0.5) is worth the floor; perfect is the max.
		w := judgeWeightFloor + (judgeWeightMax-judgeWeightFloor)*math.Max(0, (rate-0.5)/0.5)
		out[j] = w
	}
	return out
}

// betaSample draws from Beta(a, b) through two gamma draws.
func betaSample(rng *rand.Rand, a, b float64) float64 {
	x, y := gammaSample(rng, a), gammaSample(rng, b)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

// gammaSample is Marsaglia and Tsang's method (shape < 1 boosted).
func gammaSample(rng *rand.Rand, shape float64) float64 {
	if shape <= 0 {
		return 0
	}
	if shape < 1 {
		return gammaSample(rng, shape+1) * math.Pow(rng.Float64(), 1/shape)
	}
	d := shape - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// RankByEstimate orders estimates by posterior mean, best first.
func RankByEstimate(es []ModelEstimate) []ModelEstimate {
	out := append([]ModelEstimate(nil), es...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].P() > out[j].P() })
	return out
}

// BenchmarkMatch says how a model version maps to a benchmark row: "exact"
// when the feed has a row for that version, else "fallback" (the leg's own
// row stands in), or "none".
type BenchmarkMatch struct {
	Slug  string
	Index float64
	Match string
}

// BenchmarkFor maps the model a run reported to the feed's row for it.
func BenchmarkFor(leg Leg, model string) BenchmarkMatch {
	models, _, _ := PerfModels()
	if slug := ModelSlug(model); slug != "" {
		for _, m := range models {
			if strings.EqualFold(m.Slug, slug) && m.IntelligenceIndex > 0 {
				return BenchmarkMatch{Slug: m.Slug, Index: m.IntelligenceIndex, Match: "exact"}
			}
		}
	}
	if m, ok := MatchAA(models, leg); ok && m.IntelligenceIndex > 0 {
		return BenchmarkMatch{Slug: m.Slug, Index: m.IntelligenceIndex, Match: "fallback"}
	}
	return BenchmarkMatch{Match: "none"}
}

// ModelSlug is a model id in the feed's slug form: the last path segment,
// lower case, dots as dashes ("openrouter/z-ai/glm-5.3" -> "glm-5-3").
func ModelSlug(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.ReplaceAll(m, ".", "-")
}

// benchmarkGap is how far the version's exact benchmark row sits from the
// leg's own row, on EffortPrior's probability line; exact is false when
// the feed has no row for the version.
func benchmarkGap(leg Leg, model string) (float64, bool) {
	v := BenchmarkFor(leg, model)
	if v.Match != "exact" {
		return 0, false
	}
	base, ok := MatchAA(func() []AAModel { m, _, _ := PerfModels(); return m }(), leg)
	if !ok || base.IntelligenceIndex <= 0 {
		return 0, true
	}
	return (v.Index - base.IntelligenceIndex) * 0.012, true
}
