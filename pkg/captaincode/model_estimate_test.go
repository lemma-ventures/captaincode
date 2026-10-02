package captaincode

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// labelled builds a settled sample for leg/model on domain d.
func labelled(i int, leg Leg, model string, d Domain, ok bool, at time.Time) RoutingSample {
	status := AcceptanceAccepted
	if !ok {
		status = AcceptanceRejected
	}
	id := fmt.Sprintf("t%d", i)
	return RoutingSample{
		Decision: Decision{TaskID: id, Task: "task " + id, Chosen: leg, Domain: d, At: at},
		Outcome:  &OutcomeEvidence{TaskID: id, Status: status, DecidedBy: DecidedByCommit, UpdatedAt: at},
		Events:   []Event{{Leg: leg, Model: model, Outcome: "ok", Duration: 60000, At: at}},
	}
}

// A version with no runs borrows its leg's estimate; it does not stand on
// nothing, and it does not get a score of its own either.
func TestModelEstimateBorrowsFromItsLeg(t *testing.T) {
	now := time.Now()
	var hist []RoutingSample
	for i := 0; i < 30; i++ {
		hist = append(hist, labelled(i, LegGLM, "glm-5.2", DomainCode, i%10 != 0, now))
	}
	e := NewModelEstimator(hist, nil, now)
	old := e.Estimate(LegGLM, "glm-5.2", "", DomainCode)
	fresh := e.Estimate(LegGLM, "glm-5.3", "", DomainCode)
	assert.Greater(t, old.N, 20.0)
	assert.Zero(t, fresh.N)
	assert.True(t, fresh.Uncertain())
	assert.InDelta(t, e.Estimate(LegGLM, "", "", DomainCode).P(), fresh.P(), 1e-9, "the new version starts at its leg's level")
	assert.InDelta(t, 0.9, old.P(), 0.06)
}

// Code and prose split once each has enough evidence, and pool before.
func TestModelEstimateSplitsCodeFromProse(t *testing.T) {
	now := time.Now()
	var hist []RoutingSample
	for i := 0; i < 20; i++ {
		hist = append(hist, labelled(i, LegKimi, "kimi-k3", DomainCode, true, now))
		hist = append(hist, labelled(100+i, LegKimi, "kimi-k3", DomainResearch, false, now))
	}
	e := NewModelEstimator(hist, nil, now)
	assert.Greater(t, e.Estimate(LegKimi, "kimi-k3", "", DomainCode).P(), 0.8)
	assert.Less(t, e.Estimate(LegKimi, "kimi-k3", "", DomainResearch).P(), 0.2)

	few := NewModelEstimator(append(hist[:4:4], labelled(999, LegKimi, "kimi-k3", DomainResearch, false, now)), nil, now)
	assert.InDelta(t, few.Estimate(LegKimi, "kimi-k3", "", DomainCode).P(), few.Estimate(LegKimi, "kimi-k3", "", DomainResearch).P(), 1e-9,
		"too little to split: they pool")
}

// One squash commit credited 15 tasks: each holds 1/15 of it.
func TestSharedCommitsCreditAShare(t *testing.T) {
	now := time.Now()
	s := labelled(1, LegCodex, "gpt-6.1-sol", DomainCode, true, now)
	s.Outcome.Commit = &CommitRecord{SHA: "squash", Shared: 15}
	e := NewModelEstimator([]RoutingSample{s}, nil, now)
	assert.InDelta(t, 1.0/15, e.Estimate(LegCodex, "gpt-6.1-sol", "", DomainCode).N, 1e-9)
}

// Evidence fades with a 30-day half-life.
func TestModelEstimateEvidenceDecays(t *testing.T) {
	now := time.Now()
	e := NewModelEstimator([]RoutingSample{labelled(1, LegGLM, "glm-5.3", DomainCode, true, now.Add(-30*24*time.Hour))}, nil, now)
	assert.InDelta(t, 0.5, e.Estimate(LegGLM, "glm-5.3", "", DomainCode).N, 1e-6)
}

// A judge's verdict counts little until it has been checked against tests.
func TestJudgeVerdictsAreWeakUntilChecked(t *testing.T) {
	now := time.Now()
	s := RoutingSample{
		Decision: Decision{TaskID: "j", Task: "task j", Chosen: LegGLM, Domain: DomainCode, At: now},
		Outcome:  &OutcomeEvidence{TaskID: "j", Status: AcceptancePending},
		Events:   []Event{{Leg: LegGLM, Model: "glm-5.3", Outcome: "ok", Duration: 1000, At: now, Judge: LegGemini, JudgePass: true}},
	}
	e := NewModelEstimator([]RoutingSample{s}, nil, now)
	assert.InDelta(t, judgeWeightFloor, e.Estimate(LegGLM, "glm-5.3", "", DomainCode).N, 1e-9)

	var hist []RoutingSample
	for i := 0; i < 12; i++ {
		x := labelled(i, LegGLM, "glm-5.3", DomainCode, true, now)
		x.Outcome.Checks = []CheckResult{{Command: "go test", Source: "tests", Passed: true}}
		x.Events[0].Judge, x.Events[0].JudgePass = LegGemini, true
		hist = append(hist, x)
	}
	assert.InDelta(t, judgeWeightMax, judgeWeights(hist)[LegGemini], 1e-9, "a judge that agreed with every test")
}

func TestModelEstimateRunTime(t *testing.T) {
	now := time.Now()
	var evs []Event
	for i := 0; i < 20; i++ {
		evs = append(evs, Event{Leg: LegClaude, Model: "claude-opus-5-5", Outcome: "ok", Duration: (17 * time.Minute).Milliseconds(), At: now})
		evs = append(evs, Event{Leg: LegGrokMax, Model: "grok-4.7", Outcome: "ok", Duration: (3 * time.Minute).Milliseconds(), At: now})
	}
	e := NewModelEstimator(nil, evs, now)
	assert.InDelta(t, 17, e.Estimate(LegClaude, "claude-opus-5-5", "", DomainCode).Minutes(), 2)
	assert.InDelta(t, 3, e.Estimate(LegGrokMax, "grok-4.7", "", DomainCode).Minutes(), 1)
}

func TestBetaSampleMean(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var sum float64
	for i := 0; i < 20000; i++ {
		sum += betaSample(rng, 8, 2)
	}
	assert.InDelta(t, 0.8, sum/20000, 0.01)
	assert.False(t, math.IsNaN(betaSample(rng, 0.3, 0.2)))
}

// A version maps to its own benchmark row when the feed has it; otherwise
// the leg's row stands in, marked as a fallback.
func TestBenchmarkForMarksExactAndFallback(t *testing.T) {
	t.Cleanup(PerfResetToSnapshot)
	SetPerfModels([]AAModel{
		{Slug: "glm-5-3", Name: "GLM-5.3", Creator: "Z.ai", IntelligenceIndex: 45, CodingIndex: 75},
		{Slug: "glm-5-2", Name: "GLM-5.2", Creator: "Z.ai", IntelligenceIndex: 40, CodingIndex: 70},
	})
	exact := BenchmarkFor(LegGLM, "openrouter/z-ai/glm-5.2")
	assert.Equal(t, "exact", exact.Match)
	assert.Equal(t, "glm-5-2", exact.Slug)
	fb := BenchmarkFor(LegGLM, "z-ai/glm-5.4-preview")
	assert.Equal(t, "fallback", fb.Match)
	assert.Equal(t, "glm-5-3", fb.Slug)

	gap, ok := benchmarkGap(LegGLM, "z-ai/glm-5.2")
	assert.True(t, ok)
	assert.InDelta(t, -0.06, gap, 1e-9, "5 index points below the leg's row")

	e := NewModelEstimator(nil, nil, time.Now())
	assert.Less(t, e.Estimate(LegGLM, "z-ai/glm-5.2", "", DomainCode).P(), e.Estimate(LegGLM, "", "", DomainCode).P(),
		"the older version starts below the leg")
}

// The judge never shares the worker's vendor and never spends a
// subscription window.
func TestPickJudgeAvoidsTheWorkersVendor(t *testing.T) {
	for _, worker := range []Leg{LegClaude, LegGLM, LegGemini, LegCursor, LegCodexCLI} {
		j, ok := PickJudge(worker, nil)
		if !ok {
			t.Fatalf("no judge for %s", worker)
		}
		assert.NotEqual(t, VendorOf(worker), VendorOf(j), "%s judged by %s", worker, j)
		spec, _ := Spec(j)
		assert.False(t, spec.Subscription, "%s judged by subscription leg %s", worker, j)
	}
	assert.Equal(t, "xai", VendorOf(LegCursor), "cursor runs grok")
	_, ok := PickJudge(LegGLM, func(Leg) bool { return false })
	assert.False(t, ok, "nothing open: unjudged")
}
