package captaincode

// Success estimation per candidate (stage 2). The value ranking scores a
// leg's QUALITY; what a router actually needs is the probability that this
// leg, at this effort, completes THIS task acceptably - and the cost when it
// does not. The estimate here is a kNN over the routing history: a task is
// hashed into a bag-of-tokens vector (no model, no network, a few
// microseconds), its nearest labelled neighbours vote per (leg, effort),
// and a per-effort prior read off the performance feed's own rows carries
// the cold start. Observations from a different model version than the one
// the leg runs today are discounted (the ROADMAP's "reset or discount
// evidence when model/runtime versions change"), and old ones decay.
//
// The literature's finding this leans on: a simple kNN router beats the
// learned ones on the routing benchmarks (Rethinking Predictive Modeling for
// LLM Routing, 2025), and the quality estimator - not the cascade logic - is
// what decides whether cheap-then-escalate pays (cascade routing, 2024).

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// taskVectorDims is the hashed feature space. 512 buckets over unigrams and
// bigrams of a task's words: collisions are rare enough at prompt length
// and the vector stays small enough to keep thousands in memory.
const taskVectorDims = 512

// TaskVector hashes a task into a unit vector over word unigrams + bigrams.
func TaskVector(task string) []float32 {
	v := make([]float32, taskVectorDims)
	words := tokenizeTask(task)
	add := func(s string) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(s))
		v[h.Sum32()%taskVectorDims]++
	}
	for i, w := range words {
		add(w)
		if i+1 < len(words) {
			add(w + " " + words[i+1])
		}
	}
	var norm float64
	for _, x := range v {
		norm += float64(x * x)
	}
	if norm > 0 {
		n := float32(math.Sqrt(norm))
		for i := range v {
			v[i] /= n
		}
	}
	return v
}

// tokenizeTask lowercases and splits on non-letters/digits, dropping
// one-character tokens and a handful of stop words that carry no task
// signal but dominate cosine scores.
func tokenizeTask(task string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 1 {
			w := cur.String()
			if !taskStop[w] {
				out = append(out, w)
			}
		}
		cur.Reset()
	}
	for _, r := range strings.ToLower(task) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

var taskStop = map[string]bool{"the": true, "a": true, "an": true, "to": true, "of": true, "and": true, "in": true,
	"it": true, "is": true, "for": true, "on": true, "this": true, "that": true, "with": true, "be": true, "as": true}

// Cosine is the similarity of two unit vectors.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i] * b[i])
	}
	return s
}

// SuccessEstimate is P(success) for one (leg, effort) on a task, with how
// much evidence stands behind it. PriorOnly says no neighbour voted: the
// number is the feed's prior and must be reported as one.
type SuccessEstimate struct {
	P         float64 `json:"p"`
	N         int     `json:"n"`                    // neighbours that voted (weight > 0.2)
	Weight    float64 `json:"weight,omitempty"`     // total evidence weight behind P (excluding the prior)
	Prior     float64 `json:"prior"`                // the cold-start prior P was pulled toward
	PriorOnly bool    `json:"prior_only,omitempty"` // no local evidence at all
}

// estSample is one labelled past task.
type estSample struct {
	vec     []float32
	class   Class
	domain  Domain
	leg     Leg
	effort  Effort
	model   string
	success bool
	at      time.Time
}

// SuccessEstimator answers success estimates from the routing history.
type SuccessEstimator struct {
	samples []estSample
}

// NewSuccessEstimator indexes the labelled samples. Unlabelled ones (pending
// outcomes) are skipped: a pending outcome is not a weak success.
func NewSuccessEstimator(history []RoutingSample) *SuccessEstimator {
	e := &SuccessEstimator{}
	for _, s := range history {
		if !s.Labeled() {
			continue
		}
		d := s.Decision
		leg, effort, model := d.Chosen, d.Effort, ""
		for _, ev := range s.Events {
			if ev.Leg == "" || ev.Outcome != "ok" {
				continue
			}
			leg, model = ev.Leg, ev.Model
			if ev.Effort != "" {
				effort = ev.Effort
			}
		}
		if leg == "" {
			continue
		}
		task := d.Task
		if task == "" && s.Outcome != nil {
			task = s.Outcome.Task
		}
		if task == "" {
			continue
		}
		at := d.At
		if at.IsZero() && s.Outcome != nil {
			at = s.Outcome.UpdatedAt
		}
		e.samples = append(e.samples, estSample{vec: TaskVector(task), class: d.Class, domain: d.Domain,
			leg: leg, effort: effort, model: model, success: s.Success(), at: at})
	}
	return e
}

// Samples is how many labelled tasks the estimator holds.
func (e *SuccessEstimator) Samples() int {
	if e == nil {
		return 0
	}
	return len(e.samples)
}

// Estimator tunables.
const (
	estimateK           = 25   // neighbours consulted
	estimatePriorWeight = 5.0  // pseudo-observations behind the prior (priors.go uses the same 5)
	estimateVersionDisc = 0.3  // weight of an observation from another model version
	estimateEffortDisc  = 0.5  // weight of an observation at another effort on the same leg
	estimateHalfLife    = 30.0 // days at which an observation counts as half a fresh one
	estimateMinSim      = 0.05 // below this cosine a neighbour is unrelated
)

// Estimate is P(success) for leg at effort on task. midTierP, when > 0, is
// the decision leg's own ex-ante estimate for a mid-tier worker, blended
// into the prior for non-frontier legs (stage 2's third question).
func (e *SuccessEstimator) Estimate(task string, class Class, domain Domain, leg Leg, effort Effort, midTierP float64, now time.Time) SuccessEstimate {
	prior := EffortPrior(leg, effort)
	if midTierP > 0 && leg != LegClaude && !IsFrontierClass(leg) {
		prior = 0.5*prior + 0.5*midTierP
	}
	out := SuccessEstimate{P: prior, Prior: prior, PriorOnly: true}
	if e == nil || len(e.samples) == 0 {
		return out
	}
	model := ModelIDAt(leg, effort)
	vec := TaskVector(task)
	type cand struct {
		w       float64
		success bool
	}
	var cands []cand
	for _, s := range e.samples {
		if s.leg != leg {
			continue
		}
		sim := Cosine(vec, s.vec)
		if s.class == class {
			sim += 0.1
		}
		if s.domain == domain {
			sim += 0.05
		}
		if sim < estimateMinSim {
			continue
		}
		w := sim
		if s.effort != "" && effort != "" && s.effort != effort {
			w *= estimateEffortDisc
		}
		if s.model != "" && model != "" && s.model != model {
			w *= estimateVersionDisc
		}
		if !s.at.IsZero() {
			age := now.Sub(s.at).Hours() / 24
			if age > 0 {
				w *= math.Pow(0.5, age/estimateHalfLife)
			}
		}
		cands = append(cands, cand{w: w, success: s.success})
	}
	if len(cands) == 0 {
		return out
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].w > cands[j].w })
	if len(cands) > estimateK {
		cands = cands[:estimateK]
	}
	num, den := prior*estimatePriorWeight, estimatePriorWeight
	for _, c := range cands {
		if c.success {
			num += c.w
		}
		den += c.w
		out.Weight += c.w
		if c.w > 0.2 {
			out.N++
		}
	}
	out.P = num / den
	out.PriorOnly = out.N == 0
	return out
}

// EffortPrior is the cold-start P(success) for a leg at an effort, read off
// the performance feed's per-effort rows (claude-opus-5-5-medium, ...) when
// it has them and the leg's plain row otherwise, mapped onto 0.15..0.92 by a
// straight line through the index. A prior, labelled as one everywhere it
// is shown; it exists so a leg nobody has tried is not scored as hopeless.
func EffortPrior(leg Leg, effort Effort) float64 {
	idx, ok := EffortIndex(leg, effort)
	if !ok || idx <= 0 {
		idx = perfOrPrior(leg)
	}
	p := 0.25 + (idx-25)*0.012
	if p < 0.15 {
		p = 0.15
	}
	if p > 0.92 {
		p = 0.92
	}
	return p
}
