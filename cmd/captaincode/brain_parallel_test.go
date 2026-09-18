package main

import (
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The launching turn must return AT ONCE - that is the whole point: the TUI
// stays free while the run proceeds.
func TestParallelStartReturnsImmediately(t *testing.T) {
	b := teamBrain()
	release := make(chan struct{})
	var started atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		started.Add(1)
		<-release // hold the background run open
		return leg, captaincode.Result{Text: "deep research result", DurationMs: 10}, nil
	}
	t0 := time.Now()
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/parallel research the pricing page"))
	require.Equal(t, 200, rec.Code)
	assert.Less(t, time.Since(t0), 3*time.Second, "ack must not wait for the run")
	assert.Contains(t, rec.Body.String(), "parallel run pl_")

	require.Eventually(t, func() bool { return started.Load() == 1 }, 10*time.Second, 50*time.Millisecond,
		"the run really started in the background")

	// While in flight, status says running and show refuses.
	st := httptest.NewRecorder()
	b.chatCompletions(st, repeatReq("/parallel status"))
	assert.Contains(t, st.Body.String(), "running")

	close(release)
	require.Eventually(t, func() bool {
		s := httptest.NewRecorder()
		b.chatCompletions(s, repeatReq("/parallel status"))
		return strings.Contains(s.Body.String(), "✓ done")
	}, 15*time.Second, 100*time.Millisecond)

	// The result is readable in full, in the TUI.
	show := httptest.NewRecorder()
	b.chatCompletions(show, repeatReq("/parallel show last"))
	assert.Contains(t, show.Body.String(), "deep research result")
}

// A finished run is announced exactly once, and never by splicing it into an
// unrelated answer.
func TestParallelNoticeFiresOnce(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "done", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/parallel check the feed"))
	require.Equal(t, 200, rec.Code)
	require.Eventually(t, func() bool { return b.parallelNoticeAvailable() }, 15*time.Second, 100*time.Millisecond)

	first := b.parallelNotice(defaultWorkspace().Dir)
	assert.Contains(t, first, "finished")
	assert.Contains(t, first, "/parallel show pl_")
	assert.Empty(t, b.parallelNotice(defaultWorkspace().Dir), "announced once, never repeated every turn")
}

// Concurrency is bounded - each detached run is real spend.
func TestParallelIsBounded(t *testing.T) {
	b := teamBrain()
	release := make(chan struct{})
	defer close(release)
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		<-release
		return leg, captaincode.Result{Text: "x", DurationMs: 5}, nil
	}
	for i := 0; i < parallelMax; i++ {
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, repeatReq("/parallel task"))
		require.Contains(t, rec.Body.String(), "parallel run pl_")
	}
	over := httptest.NewRecorder()
	b.chatCompletions(over, repeatReq("/parallel one too many"))
	assert.Contains(t, over.Body.String(), "max", "refuses past the cap instead of piling on spend")
}

func TestParallelStatusEmpty(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/parallel status"))
	assert.Contains(t, rec.Body.String(), "no parallel runs")
}
