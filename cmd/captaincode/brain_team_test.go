package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── captain/team: hierarchical team execution in the TUI wrapper ──
//
// "Share tasks with your team of agents" used to reach a single one-shot
// worker that could only plan or stall (2026-07-20). With captain/team the
// director plans a worker tree, the BRAIN executes the workers in parallel
// (each with the watchdog + reroute machinery), one AssessMulti call grades
// and synthesizes, and the ensemble scorecards fill from real TUI work.

// teamBrain is the shared route fixture. Exploration is pinned off: the live
// brain rolls a 10% (trivial) / 5% (medium) chance of trying the runner-up,
// which made leg assertions flake one run in ten. Tests that exercise the
// roll set exploreFn themselves.
func teamBrain() *brain {
	return &brain{
		ledger:        &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		allowed:       map[captaincode.Leg]bool{},
		exploreFn:     func(captaincode.Class) bool { return false },
		captureTestFn: func(context.Context, string) (*captaincode.CheckEvidence, error) { return nil, nil },
	}
}

func TestModelsIncludeTeam(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.models(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assert.Contains(t, rec.Body.String(), `"team"`, "the fork validates model ids - team must be advertised")
}

// A fan-out plan from the director must surface as model "team" and be cached
// so the wrapper doesn't pay a second director call.
func TestRoute_FanOutPlanReturnsTeamAndCachesPlan(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0") // this test exercises the DIRECTOR path; triage would (correctly) fast-path its fixture task
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		assert.True(t, allowFanOut, "the TUI route must allow fan-out now")
		return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "decomposes",
			Workers: []captaincode.Worker{
				{Leg: captaincode.LegCodex, Brief: "part A"},
				{Leg: captaincode.LegGLM, Brief: "part B"},
			}}, nil
	}
	body, _ := json.Marshal(routeReq{Task: "do A and B independently"})
	rec := httptest.NewRecorder()
	b.route(rec, httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "team", resp.Leg)
	assert.Equal(t, "team", resp.Model)
	assert.Equal(t, "captain", resp.Provider)

	p, ok := b.takeTeamPlan("do A and B independently")
	require.True(t, ok, "plan must be cached for the wrapper")
	assert.Len(t, p.Workers, 2)
	_, again := b.takeTeamPlan("do A and B independently")
	assert.False(t, again, "cache is take-once")
}

func TestRoute_TeamDisabledByEnv(t *testing.T) {
	t.Setenv("CAPTAIN_TEAM", "0")
	b := teamBrain()
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		assert.False(t, allowFanOut, "CAPTAIN_TEAM=0 → single-worker planning only")
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegCodex, Brief: "b"}}}, nil
	}
	body, _ := json.Marshal(routeReq{Task: "task"})
	rec := httptest.NewRecorder()
	b.route(rec, httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEqual(t, "team", resp.Leg)
}

// The wrapper executes the cached plan: workers run in PARALLEL, each brief
// standalone; AssessMulti synthesizes the answer and grades every worker;
// the ledger gets per-worker events (Team set) plus the ensemble aggregate.
func TestTeamChat_ExecutesWorkersGradesAndRecords(t *testing.T) {
	b := teamBrain()
	// Parallelism is proved by a rendezvous, not by a stopwatch: every worker
	// blocks until BOTH have arrived. Serial execution deadlocks the first one,
	// which fails loudly after the timeout. An earlier version asserted the
	// whole call finished inside 230ms and went red on any loaded machine -
	// wall-clock slack is not evidence of concurrency.
	arrived := make(chan captaincode.Leg, 2)
	release := make(chan struct{})
	go func() {
		<-arrived
		<-arrived
		close(release)
	}()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		arrived <- leg
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			t.Errorf("worker %s ran alone: the team did not dispatch in parallel", leg)
		}
		return leg, captaincode.Result{Text: "output of " + string(leg), Tokens: 10, DurationMs: 120}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		require.Len(t, outputs, 2)
		ma := captaincode.MultiAssessment{Synthesis: "SYNTHESIZED-ANSWER"}
		for title := range outputs {
			ma.Scores = append(ma.Scores, struct {
				Worker  string  `json:"worker"`
				Quality float64 `json:"quality"`
				Verdict string  `json:"verdict"`
				Notes   string  `json:"notes"`
			}{Worker: title, Quality: 8, Verdict: "good"})
		}
		return ma, nil
	}
	b.storeTeamPlan("do A and B", captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "split",
		Workers: []captaincode.Worker{
			{Leg: captaincode.LegCodex, Brief: "part A"},
			{Leg: captaincode.LegGLM, Brief: "part B"},
		}})

	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "do A and B"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))

	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "SYNTHESIZED-ANSWER")

	evs := b.ledger.Events
	require.Len(t, evs, 3, "2 worker events + 1 ensemble aggregate")
	var aggregate, workers int
	for _, e := range evs {
		assert.Equal(t, "codex+glm", e.Team)
		if e.Leg == "" {
			aggregate++
			assert.InDelta(t, 8.0, e.Quality, 0.01, "ensemble grade = mean of scored workers")
		} else {
			workers++
			assert.InDelta(t, 8.0, e.Quality, 0.01)
		}
	}
	assert.Equal(t, 1, aggregate)
	assert.Equal(t, 2, workers)
}

// One-worker plans are not teams - the wrapper must run the single leg
// through the normal path instead of the team machinery.
func TestTeamChat_SoloPlanRunsSingleWorker(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "solo answer from " + string(leg)}, nil
	}
	b.storeTeamPlan("simple task", captaincode.Plan{Class: captaincode.ClassMedium,
		Workers: []captaincode.Worker{{Leg: captaincode.LegCodex, Brief: "just do it"}}})

	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "simple task"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "solo answer from codex")
}

// Grading is not availability: if AssessMulti fails, the user still gets the
// workers' outputs (concatenated), and the run is recorded unscored.
func TestTeamChat_AssessFailureStillDeliversOutputs(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "out-" + string(leg)}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{}, errors.New("director down")
	}
	b.storeTeamPlan("t", captaincode.Plan{Class: captaincode.ClassMedium,
		Workers: []captaincode.Worker{
			{Leg: captaincode.LegCodex, Brief: "a"},
			{Leg: captaincode.LegGLM, Brief: "b"},
		}})
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "t"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	out := rec.Body.String()
	assert.True(t, strings.Contains(out, "out-codex") && strings.Contains(out, "out-glm"),
		"both workers' outputs must reach the user even when grading fails")
}

// The sidebar must show WHICH models a team is running - one activity entry
// per worker with the real model id, not a single opaque "team" row.
func TestTeamChat_PushesPerWorkerActivity(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "ok output long enough"}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "s"}, nil
	}
	b.storeTeamPlan("t2", captaincode.Plan{Class: captaincode.ClassHigh,
		Workers: []captaincode.Worker{
			{Leg: captaincode.LegCodex, Brief: "a"},
			{Leg: captaincode.LegGLM, Brief: "b"},
		}})
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "t2"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)

	b.amu.Lock()
	defer b.amu.Unlock()
	var legs []string
	for _, a := range b.acts {
		legs = append(legs, a.Leg+"/"+a.Model)
	}
	joined := strings.Join(legs, " ")
	assert.Contains(t, joined, "codex/"+captaincode.ModelID(captaincode.LegCodex), "worker rows must name the real model")
	assert.Contains(t, joined, "glm/"+captaincode.ModelID(captaincode.LegGLM))
}

// /frontier as a forced pseudo-model: route returns it, the wrapper runs the
// frontier claude and records under the claude leg.
func TestBrainRoute_ForcedFrontier(t *testing.T) {
	b := teamBrain()
	body, _ := json.Marshal(routeReq{Task: "hardest problem", Forced: "frontier"})
	rec := httptest.NewRecorder()
	b.route(rec, httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "frontier", resp.Model)
}

func TestFrontierChat_RunsAndRecordsUnderClaude(t *testing.T) {
	b := teamBrain()
	b.frontierFn = func(task string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		return captaincode.Result{Text: "FRONTIER-ANSWER: deep result with plenty of substance to pass gates", Tokens: 900, DurationMs: 60_000}, nil
	}
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 9.5, Verdict: "good"}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "frontier", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "hardest problem"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "FRONTIER-ANSWER")
	require.Eventually(t, func() bool { // recordRun is async by design
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.ledger.Events) > 0
	}, 2*time.Second, 20*time.Millisecond)
	b.mu.Lock()
	defer b.mu.Unlock()
	assert.Equal(t, captaincode.LegClaude, b.ledger.Events[0].Leg, "frontier runs build claude's scorecard")
}

func TestModelsIncludeFrontier(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.models(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assert.Contains(t, rec.Body.String(), `"frontier"`)
}

// A silent stream is a dead turn: during extended thinking the wrapper sends
// no text for minutes, an idle timer killed the connection, the TUI spinner
// stopped, and a 4-16 min frontier result was discarded (live 2026-07-25,
// "Expert peer review rounds"). The stream writer must emit SSE keepalive
// comments while quiet - invisible to the parser, fatal to idle timeouts.
func TestCompletionWriterEmitsKeepalives(t *testing.T) {
	old := sseKeepaliveEvery
	sseKeepaliveEvery = 25 * time.Millisecond
	defer func() { sseKeepaliveEvery = old }()

	rec := httptest.NewRecorder()
	emit, _, finish := newCompletionWriter(rec, oaiChatReq{Stream: true}, "frontier")
	emit("starting…")
	time.Sleep(120 * time.Millisecond) // silence - keepalives must flow
	emit("done")
	finish()
	body := rec.Body.String()
	assert.GreaterOrEqual(t, strings.Count(body, ": keepalive"), 2, "quiet periods must carry heartbeats")
	assert.Contains(t, body, "starting…")
	assert.Contains(t, body, "[DONE]")
	after := body[strings.LastIndex(body, "[DONE]"):]
	assert.NotContains(t, after, "keepalive", "no heartbeats after the stream closes")
}

// "/team /quality": the director must plan from a menu BOUND to the top-rated
// legs, with the quality preference passed through (2026-08-26).
func TestTeamQualityBindsMenuToTopLegs(t *testing.T) {
	b := teamBrain()
	var gotPrefer string
	var gotOpen []captaincode.Leg
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		gotPrefer, gotOpen = prefer, open
		return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "top legs",
			Workers: []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "part A"}, {Leg: captaincode.LegCursor, Brief: "part B"}}}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 10}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/team /quality design the launch plan"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Equal(t, "quality", gotPrefer, "preference reaches the director")
	require.NotEmpty(t, gotOpen)
	assert.LessOrEqual(t, len(gotOpen), 4, "menu bound to the top-quality legs")
	assert.Contains(t, gotOpen, captaincode.LegClaude, "the best-rated leg is on the menu")
}

func TestTeamPrefer(t *testing.T) {
	assert.Equal(t, "quality", teamPrefer("/team /quality build it"))
	assert.Equal(t, "quality", teamPrefer("/team /best build it"))
	assert.Equal(t, "speed", teamPrefer("/team /fast build it"))
	assert.Equal(t, "", teamPrefer("/team build it"))
	assert.Equal(t, "", teamPrefer("plain prose"))
}

// /frontier called the frontier runner directly, outside the reroute net: a
// rate-limited claude came back to the TUI as a 429, which retried four times
// in twenty seconds and gave up (live 2026-09-12). A /team /frontier plan
// already rerouted to codex-cli; plain /frontier must too, and cool claude.
func TestFrontierChat_ReroutesWhenClaudeIsRateLimited(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_LOGS", "0")
	b := teamBrain()
	b.frontierFn = func(task string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		return captaincode.Result{}, fmt.Errorf("claude: %w: You've reached your Fable limit", captaincode.ErrRateLimited)
	}
	var ran []captaincode.Leg
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = append(ran, leg)
		return leg, captaincode.Result{Text: "CODEX-ANSWER: shared Merkle authentication wired to native verification", DurationMs: 5000}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "frontier", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "do O01-a, shared Merkle authentication"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "CODEX-ANSWER", "the task moved to another leg instead of failing the turn")
	require.NotEmpty(t, ran)
	assert.NotEqual(t, captaincode.LegClaude, ran[0])
	b.mu.Lock()
	until, cooling := b.ledger.Cooldowns[captaincode.LegClaude]
	b.mu.Unlock()
	assert.True(t, cooling && until.After(time.Now()), "claude is benched for its window")
}

// "You've reached your Fable limit" is the frontier tier's window, not
// claude's: /frontier benched claude for 30 minutes at every attempt and
// every ordinary claude run then said "not available" while `claude -p`
// answered fine (2026-09-12). The tier is benched, claude answers at
// standard settings, and the next /frontier goes to claude directly.
func TestFrontierTierLimitBenchesTheTierNotClaude(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_LOGS", "0")
	b := teamBrain()
	frontierCalls := 0
	b.frontierFn = func(task string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		frontierCalls++
		return captaincode.Result{}, &captaincode.RateLimitError{Leg: captaincode.LegClaude, Msg: "You've reached your Fable limit. Switch to another model", Tier: "Fable"}
	}
	var ran []captaincode.Leg
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = append(ran, leg)
		return leg, captaincode.Result{Text: "OPUS-ANSWER: the decider contract, in full", DurationMs: 5000}, nil
	}
	ask := func() string {
		body, _ := json.Marshal(map[string]any{"model": "frontier", "stream": false,
			"messages": []map[string]string{{"role": "user", "content": "what's next for D01"}}})
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		return rec.Body.String()
	}
	assert.Contains(t, ask(), "OPUS-ANSWER")
	require.Equal(t, []captaincode.Leg{captaincode.LegClaude}, ran, "claude at standard settings is the first fallback")
	b.mu.Lock()
	_, claudeCooling := b.ledger.Cooldowns[captaincode.LegClaude]
	until, frontierCooling := b.ledger.Cooldowns[captaincode.LegFrontier]
	b.mu.Unlock()
	assert.False(t, claudeCooling, "claude itself is open")
	assert.True(t, frontierCooling && until.After(time.Now()), "the frontier tier is benched")

	before := frontierCalls
	assert.Contains(t, ask(), "OPUS-ANSWER")
	assert.Equal(t, before, frontierCalls, "while the tier is benched /frontier does not knock again")
	assert.Equal(t, []captaincode.Leg{captaincode.LegClaude, captaincode.LegClaude}, ran)
}
