package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func zurich(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Europe/Zurich")
	require.NoError(t, err)
	return loc
}

// The cooldown follows the provider's reset time (+1m slack), not a flat 30m:
// claude was benched until 01:17 while its window reopened at 02:50.
func TestOnWorkerErrorCooldownFollowsResetTime(t *testing.T) {
	b := teamBrain()
	reset := time.Now().Add(2 * time.Hour)
	err := captaincode.NewRateLimitError(captaincode.LegClaude, "You've hit your session limit · resets 2:50am", reset)
	b.onWorkerError(captaincode.LegClaude, err)
	b.mu.Lock()
	defer b.mu.Unlock()
	until := b.ledger.Cooldowns[captaincode.LegClaude]
	assert.WithinDuration(t, reset.Add(time.Minute), until, 5*time.Second)
	require.NotEmpty(t, b.ledger.Events)
	assert.Contains(t, b.ledger.Events[len(b.ledger.Events)-1].Error, "session limit", "the provider's message reaches the ledger")

	// No reset time → the 30m default; a reset absurdly far out is capped.
	b2 := teamBrain()
	b2.onWorkerError(captaincode.LegGrok, captaincode.NewRateLimitError(captaincode.LegGrok, "429", time.Time{}))
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), b2.ledger.Cooldowns[captaincode.LegGrok], 5*time.Second)
	b3 := teamBrain()
	b3.onWorkerError(captaincode.LegGrok, captaincode.NewRateLimitError(captaincode.LegGrok, "x", time.Now().Add(48*time.Hour)))
	assert.WithinDuration(t, time.Now().Add(6*time.Hour), b3.ledger.Cooldowns[captaincode.LegGrok], 5*time.Second)
}

// Solo path: a rate limit after real work delivers the partial text, marks
// it, benches the leg, and does NOT reroute (a fresh leg would start over).
func TestSoloRateLimitAfterWorkKeepsPartialAndBenchesLeg(t *testing.T) {
	b := teamBrain()
	calls := 0
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		calls++
		return leg, captaincode.Result{Text: "D00 contracts written. Starting D01. " + strings.Repeat("Contract D00 pins the verifier cap and the fold arity. ", 30), DurationMs: 45 * 60 * 1000},
			captaincode.NewRateLimitError(leg, "You've hit your session limit · resets 2:50am", time.Now().Add(2*time.Hour))
	}
	for _, stream := range []bool{false, true} {
		// Distinct tasks: identical prompts attach to the solo dedupe cache.
		body, _ := json.Marshal(map[string]any{"model": "claude", "stream": stream,
			"messages": []map[string]string{{"role": "user", "content": fmt.Sprintf("implement the plan (stream=%v)", stream)}}})
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
		require.Equal(t, 200, rec.Code, "stream=%v: %s", stream, rec.Body.String())
		out := rec.Body.String()
		assert.Contains(t, out, "D00 contracts written", "stream=%v", stream)
		assert.Contains(t, out, "partial", "stream=%v: the user is told it is incomplete", stream)
		assert.Contains(t, out, "limit", "stream=%v: and why", stream)
	}
	assert.Equal(t, 2, calls, "one worker call per turn - no reroute after 45 minutes of real work")
	b.mu.Lock()
	defer b.mu.Unlock()
	assert.True(t, b.ledger.Cooldowns[captaincode.LegClaude].After(time.Now().Add(time.Hour)), "the leg is still benched until its window reopens")
}

func TestRerouteStillHappensForAnImmediateRateLimit(t *testing.T) {
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegClaude: true, captaincode.LegCursor: true} // grok is the test-default director, never a worker
	var legs []captaincode.Leg
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		legs = append(legs, leg)
		if leg == captaincode.LegClaude {
			return leg, captaincode.Result{Text: "I'll start by", DurationMs: 2000}, captaincode.NewRateLimitError(leg, "session limit", time.Time{})
		}
		return leg, captaincode.Result{Text: "cursor did the whole thing properly", DurationMs: 30000}, nil
	}
	_, res, err := b.runWorkerRerouted(defaultWorkspace(), captaincode.LegClaude, "fix the bug", nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, []captaincode.Leg{captaincode.LegClaude, captaincode.LegCursor}, legs)
	assert.Contains(t, res.Text, "cursor did")
}

// Frontier-bound work fails over to the OTHER frontier-class leg, not the
// fast ladder: last night claude's closed window sent an architecture task
// to cursor.
func TestRerouteTarget_FrontierFailsOverToCodexCLI(t *testing.T) {
	b := &brain{
		ledger:  &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		allowed: map[captaincode.Leg]bool{captaincode.LegClaude: true, captaincode.LegCursor: true, captaincode.LegGrok: true, captaincode.LegCodexCLI: true},
	}
	b.ledger.Cooldown(captaincode.LegClaude, 2*time.Hour)
	fb, ok := b.rerouteTarget(captaincode.LegFrontier, "implement the multi-package plan")
	require.True(t, ok)
	assert.Equal(t, captaincode.LegCodexCLI, fb)

	// And the other way round: codex-cli's window closed → claude.
	b.ledger.Cooldowns = map[captaincode.Leg]time.Time{}
	b.ledger.Cooldown(captaincode.LegCodexCLI, 2*time.Hour)
	fb, ok = b.rerouteTarget(captaincode.LegCodexCLI, "implement the multi-package plan")
	require.True(t, ok)
	assert.Equal(t, captaincode.LegClaude, fb)

	// Neither frontier leg open → the ordinary ladder still answers.
	b.ledger.Cooldown(captaincode.LegClaude, 2*time.Hour)
	fb, ok = b.rerouteTarget(captaincode.LegFrontier, "implement the multi-package plan")
	require.True(t, ok)
	assert.False(t, captaincode.IsFrontierClass(fb))
}

// Every worker run leaves a log on disk as it streams - solo AND team - so a
// killed brain, a dead TUI turn or a discarded result never loses what the
// agent said and did. The history record points at it.
func TestWorkerRunsLeaveLogsOnDisk(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if onStatus != nil {
			onStatus("⚙ Read docs/plan.md")
		}
		if onDelta != nil {
			onDelta("first half of the answer, ")
			onDelta("second half.")
		}
		return leg, captaincode.Result{Text: "first half of the answer, second half.", DurationMs: 8000, Streamed: onDelta != nil}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "grok", "stream": true,
		"messages": []map[string]string{{"role": "user", "content": "write the thing"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)

	logs, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".captaincode", "runs", "*-grok.log"))
	require.NotEmpty(t, logs, "a worker log exists under ~/.captaincode/runs")
	var content string
	for _, l := range logs {
		bts, _ := os.ReadFile(l)
		content += string(bts)
	}
	assert.Contains(t, content, "⚙ Read docs/plan.md", "tool activity is logged")
	assert.Contains(t, content, "first half of the answer, second half.", "the streamed text is logged")
	assert.Contains(t, content, "write the thing", "and the task it was for")
	assert.Contains(t, content, "done", "with the outcome")

	// The history record names the log.
	files, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".captaincode", "history", "*.jsonl"))
	require.NotEmpty(t, files)
	var found bool
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "write the thing") && strings.Contains(line, "-grok.log") {
				found = true
			}
		}
	}
	assert.True(t, found, "history → log path")
}

func TestTeamWorkerLogsSurviveAFailedWorker(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if onStatus != nil {
			onStatus("⚙ Bash go test ./...")
		}
		if leg == captaincode.LegGLM {
			return leg, captaincode.Result{Text: "glm got this far before dying", DurationMs: 9000}, captaincode.ErrProviderDown
		}
		return leg, captaincode.Result{Text: "grok answer with enough words to be a deliverable", DurationMs: 9000}, nil
	}
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "r", Workers: []captaincode.Worker{{Leg: captaincode.LegGLM, Brief: "a"}, {Leg: captaincode.LegGrok, Brief: "b"}}}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "SYNTH"}, nil
	}
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGLM: true, captaincode.LegGrok: true}
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/team build the feature"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	logs, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".captaincode", "runs", "*-glm.log"))
	require.NotEmpty(t, logs, "the FAILED worker's log is on disk")
	var mine string
	for _, l := range logs { // other tests write glm logs too - find this run's
		bts, _ := os.ReadFile(l)
		if strings.Contains(string(bts), "build the feature") {
			mine = string(bts)
		}
	}
	require.NotEmpty(t, mine, "a log for this task exists")
	assert.Contains(t, mine, "glm got this far", "its partial text was kept")
	assert.Contains(t, mine, "provider", "and the failure reason")
}

// Buffered (stream=false) solo turns record the log too - the live probe
// that verified logging had logs:null on its history record.
func TestBufferedSoloRecordsWorkerLog(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "buffered answer with enough words to be a deliverable", DurationMs: 8000}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "grok", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "buffered log check"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	files, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".captaincode", "history", "*.jsonl"))
	found := false
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "buffered log check") && strings.Contains(line, `"logs":["`) {
				found = true
			}
		}
	}
	assert.True(t, found, "buffered solo history → log path")
}

// A brain restart during a single-worker team run cut an 18-minute Arc
// cohort turn (2026-09-10): the log's last line was not "running", so the
// deploy's idle check believed it. The brain now counts in-flight runs and
// reports them on /v1/health.
func TestHealthReportsInFlightRuns(t *testing.T) {
	b := teamBrain()
	var seenBusy int32
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		seenBusy = b.inflightRuns.Load()
		return leg, captaincode.Result{Text: "an answer with enough words to be a real deliverable for the test", DurationMs: 100}, nil
	}
	_, _, err := b.runWorkerRerouted(defaultWorkspace(), captaincode.LegGrok, "do the thing", nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, int32(1), seenBusy, "busy while a worker runs")
	assert.Equal(t, int32(0), b.inflightRuns.Load(), "idle after")
}

// A billing refusal benches the leg for the day with the fix in the reason;
// `captain legs reopen` lifts it without a restart.
func TestBillingRefusalBenchesForTheDayAndReopenLiftsIt(t *testing.T) {
	b := teamBrain()
	b.onWorkerError(captaincode.LegGLM, fmt.Errorf("openrouter/x: %w: depleted your monthly included credits", captaincode.ErrProviderBilling))
	until := b.ledger.Cooldowns[captaincode.LegGLM]
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), until, 2*time.Minute)
	rec := httptest.NewRecorder()
	b.legReopenHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/legs/reopen?leg=glm", nil))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "glm reopened")
	_, cooling := b.ledger.Cooldowns[captaincode.LegGLM]
	assert.False(t, cooling)
	rec = httptest.NewRecorder()
	b.legReopenHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/legs/reopen?leg=nope", nil))
	assert.Equal(t, 400, rec.Code)
}
