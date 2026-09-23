package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWindowPrompt: the wrapper replays the WHOLE TUI conversation to the
// worker every turn, so a long-lived session eventually exceeds the worker
// model's context (live 2026-07-18: 193 msgs ≈ 200k tokens → codex
// ContextOverflowError on every attempt). windowPrompt must keep the head
// (system framing) and the tail (the turns being answered) within budget,
// eliding the middle - and leave small prompts untouched.
func TestWindowPrompt(t *testing.T) {
	small := "[system]\nrules\n\n[user]\nhi\n\n"
	assert.Equal(t, small, windowPrompt(small, 1000), "under-budget prompts must pass through unchanged")

	head := "[system]\nimportant framing\n\n"
	var middle string
	for i := 0; i < 2000; i++ {
		middle += "[user]\nold turn old turn old turn\n\n"
	}
	tail := "[user]\nTHE-CURRENT-QUESTION\n\n"
	windowed := windowPrompt(head+middle+tail, 5000)

	assert.LessOrEqual(t, len(windowed), 5200, "must land within budget (plus a small elision marker)")
	assert.Contains(t, windowed, "important framing", "system framing at the head must survive")
	assert.Contains(t, windowed, "THE-CURRENT-QUESTION", "the newest turns must survive - they're what's being answered")
	assert.Contains(t, windowed, "truncated", "the elision must be visible to the model")

	// A cut inside a multibyte rune reached codex exec as invalid UTF-8 and
	// its CLI refused the run (2026-09-15). Every budget must yield valid text.
	runes := strings.Repeat("route → glm · done — ✓ ", 400)
	for budget := 100; budget < 400; budget++ {
		assert.True(t, utf8.ValidString(windowPrompt(runes, budget)), "budget %d split a rune", budget)
	}
}

// TestWriteWorkerError_ContextOverflowIs400: overflow is the CONVERSATION's
// fault, not the worker's - a 5xx makes the fork's SDK blind-retry (8× live).
// It must map to a non-retryable 400 whose message tells the user the actual
// way out (new session / smaller-context leg).
func TestWriteWorkerError_ContextOverflowIs400(t *testing.T) {
	rec := httptest.NewRecorder()
	writeWorkerError(rec, captaincode.LegCodex, fmt.Errorf("wrap: %w", captaincode.ErrContextOverflow))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "client error → SDK must not retry")
	body := rec.Body.String()
	assert.Contains(t, body, "context_overflow")
	assert.Contains(t, body, "conversation is too large")
}

func TestBrainRoute_DirectorClassAndLeg(t *testing.T) {
	b := &brain{
		ledger:  &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}, Events: nil},
		allowed: map[captaincode.Leg]bool{},
		planFn: func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			assert.Equal(t, "", string(class), "director owns complexity - empty class passed in")
			return captaincode.Plan{
				Class:     captaincode.ClassHigh,
				Rationale: "hard concurrency",
				Workers:   []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "fix the race carefully"}},
			}, nil
		},
	}
	// Ensure ladder has multiple legs so director path is taken.
	body, _ := json.Marshal(routeReq{Task: "debug the race in worker drain"})
	req := httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	b.route(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "high", resp.Class, "director class is authoritative")
	assert.Equal(t, "claude", resp.Leg)
	assert.Equal(t, "fix the race carefully", resp.Brief)
	assert.Equal(t, "hard concurrency", resp.Rationale)
}

func TestBrainRoute_FallbackOnDirectorError(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0") // this test exercises the DIRECTOR path; triage would (correctly) fast-path its fixture task
	// Heuristic for this task is trivial → ladder starts at free.
	b := &brain{
		ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		planFn: func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			return captaincode.Plan{}, errors.New("director down")
		},
	}
	body, _ := json.Marshal(routeReq{Task: "fix typo in README"})
	req := httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	b.route(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "trivial", resp.Class, "heuristic classHint on director failure")
	assert.Equal(t, "free", resp.Leg, "ladder first open leg")
	assert.Equal(t, "fix typo in README", resp.Brief)
	assert.Equal(t, "director unavailable → ladder", resp.Rationale)
}

func TestBrainRoute_ForcedSkipsDirector(t *testing.T) {
	called := false
	b := &brain{
		ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		planFn: func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			called = true
			return captaincode.Plan{}, nil
		},
	}
	body, _ := json.Marshal(routeReq{Task: "anything", Forced: "codex"})
	req := httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	b.route(rec, req)
	require.Equal(t, 200, rec.Code)
	assert.False(t, called, "forced leg must not call director")

	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "codex", resp.Leg)
	assert.Equal(t, "forced leg", resp.Rationale)
}

func TestBrainRoute_FastRouteSkipsDirector(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0") // this test exercises the DIRECTOR path; triage would (correctly) fast-path its fixture task
	t.Setenv("CAPTAIN_FAST_ROUTE", "1")
	called := false
	b := &brain{
		ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		planFn: func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			called = true
			return captaincode.Plan{}, nil
		},
	}
	body, _ := json.Marshal(routeReq{Task: "add a null check"})
	req := httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	b.route(rec, req)
	require.Equal(t, 200, rec.Code)
	assert.False(t, called)
	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Contains(t, resp.Rationale, "fast route")
}

func TestBrainRoute_SingleAllowedLegSkipsDirector(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0") // this test exercises the DIRECTOR path; triage would (correctly) fast-path its fixture task
	called := false
	b := &brain{
		ledger:  &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		allowed: map[captaincode.Leg]bool{captaincode.LegCodex: true},
		planFn: func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			called = true
			return captaincode.Plan{}, nil
		},
	}
	body, _ := json.Marshal(routeReq{Task: "implement feature X"})
	req := httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	b.route(rec, req)
	require.Equal(t, 200, rec.Code)
	assert.False(t, called)
	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "codex", resp.Leg)
	assert.Equal(t, "only runnable option", resp.Rationale)
}

// TestBrainRoute_VisionTaskNeverRoutesToBlindLeg: an image-referencing task
// must land on a vision-capable leg even when the director (or ladder) picks a
// text-only model - live 2026-07-19: a "see screens/bug-logo.png" task went to
// free/deepseek-flash, which replied "I can't view the image" and stalled.
func TestBrainRoute_VisionTaskNeverRoutesToBlindLeg(t *testing.T) {
	for _, task := range []string{
		"logo is still not readable as captain code, the typo is not exact, sse screen capture in screens/bug-logo.png",
		"can you inspect this sse screen-shot from /tmp/bug-logo.png",
		"please read the sse screen shot in the bug report",
	} {
		task := task
		t.Run(task, func(t *testing.T) {
			b := &brain{
				ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
				planFn: func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
					for _, l := range open {
						assert.True(t, captaincode.LegSupportsVision(l), "director must only be offered vision-capable legs for an image task, got %s", l)
					}
					return captaincode.Plan{
						Class:   captaincode.ClassMedium,
						Workers: []captaincode.Worker{{Leg: captaincode.LegFree, Brief: "look at the logo"}}, // director misbehaves: picks a blind leg
					}, nil
				},
			}
			body, _ := json.Marshal(routeReq{Task: task})
			req := httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			b.route(rec, req)
			require.Equal(t, 200, rec.Code, rec.Body.String())

			var resp routeResp
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.True(t, captaincode.LegSupportsVision(captaincode.Leg(resp.Leg)),
				"picked leg %s cannot see images", resp.Leg)
		})
	}
}

// ── recordRun: the brain's self-learning loop ──
//
// Live 2026-07-19: the ledger was frozen at 2026-07-16 (nothing ever calls
// /v1/assess from the TUI path) and its only contents were smoke-test trivia
// that scored the free leg q8.8 - above claude. The brain must record its OWN
// wrapper runs: every substantial ok-run gets an event, quality comes from the
// director for runs worth judging, and internal/trivial runs never pollute.

func newRecordBrain(quality float64, assessErr error) *brain {
	return &brain{
		ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		assessFn: func(task, output, objective string) (captaincode.Assessment, error) {
			return captaincode.Assessment{Quality: quality, Verdict: "good"}, assessErr
		},
	}
}

func TestRecordRun_SubstantialRunIsRecordedAndScored(t *testing.T) {
	b := newRecordBrain(8.0, nil)
	out := make([]byte, 600)
	for i := range out {
		out[i] = 'x'
	}
	b.recordRun(captaincode.LegGLM, "[system]\nsys\n\n[user]\nfix the flaky auth test\n\n",
		captaincode.Result{Text: string(out), Tokens: 1200, DurationMs: 20_000}, "")
	evs := b.ledger.Events
	if assert.Len(t, evs, 1) {
		assert.Equal(t, captaincode.LegGLM, evs[0].Leg)
		assert.Equal(t, "fix the flaky auth test", evs[0].Task, "task = last [user] turn, not the whole replayed prompt")
		assert.Equal(t, 8.0, evs[0].Quality)
		assert.Equal(t, "ok", evs[0].Outcome)
	}
}

func TestRecordRun_TitleGeneratorRunsAreNeverRecorded(t *testing.T) {
	b := newRecordBrain(9.0, nil)
	b.recordRun(captaincode.LegFree, "[system]\nYou are a title generator. You output ONLY a thread title.\n\n[user]\nGenerate a brief title\n\n",
		captaincode.Result{Text: "Fix NIM Kimi 403 auth failure", DurationMs: 3000}, "")
	assert.Empty(t, b.ledger.Events, "internal title-generator runs poisoned the free leg's stats once - never again")
}

func TestRecordRun_TrivialRunRecordedButNotScored(t *testing.T) {
	b := newRecordBrain(9.0, nil)
	b.recordRun(captaincode.LegFree, "[user]\nreply with: PONG\n\n",
		captaincode.Result{Text: "PONG", DurationMs: 900}, "")
	evs := b.ledger.Events
	if assert.Len(t, evs, 1) {
		assert.Zero(t, evs[0].Quality, "micro-outputs are not worth a director call and must not inflate quality")
	}
}

func TestRecordRun_AssessFailureStillRecords(t *testing.T) {
	b := newRecordBrain(0, errors.New("director down"))
	out := make([]byte, 600)
	for i := range out {
		out[i] = 'y'
	}
	b.recordRun(captaincode.LegCodex, "[user]\nrefactor the auth seam\n\n",
		captaincode.Result{Text: string(out), DurationMs: 30_000}, "")
	evs := b.ledger.Events
	if assert.Len(t, evs, 1) {
		assert.Zero(t, evs[0].Quality)
		assert.Equal(t, "ok", evs[0].Outcome, "the run itself succeeded; only scoring failed")
	}
}

// ── assessment cost policy ──
//
// A director Assess call costs real quota, so it must not fire on every run:
// pay for information only where it's scarce. While a leg's scorecard is
// sparse (< minScored graded runs) every substantial run is scored; once
// converged, only an occasional refresh keeps the estimate honest.

func seedScored(b *brain, leg captaincode.Leg, n int) {
	for i := 0; i < n; i++ {
		b.ledger.Record(captaincode.Event{Task: "seed", Leg: leg, Outcome: "ok", Quality: 8, Verdict: "good"})
	}
}

func bigResult() captaincode.Result {
	out := make([]byte, 600)
	for i := range out {
		out[i] = 'x'
	}
	return captaincode.Result{Text: string(out), DurationMs: 20_000}
}

func TestAssessPolicy_SparseLegIsAlwaysScored(t *testing.T) {
	calls := 0
	b := newRecordBrain(8.0, nil)
	base := b.assessFn
	b.assessFn = func(task, output, obj string) (captaincode.Assessment, error) {
		calls++
		return base(task, output, obj)
	}
	seedScored(b, captaincode.LegGLM, 3) // sparse: 3 < 10
	b.recordRun(captaincode.LegGLM, "[user]\nreal task\n\n", bigResult(), "")
	assert.Equal(t, 1, calls, "sparse scorecard → buy the signal")
}

func TestAssessPolicy_ConvergedLegIsNotScoredEveryRun(t *testing.T) {
	calls := 0
	b := newRecordBrain(8.0, nil)
	base := b.assessFn
	b.assessFn = func(task, output, obj string) (captaincode.Assessment, error) {
		calls++
		return base(task, output, obj)
	}
	seedScored(b, captaincode.LegGLM, 10) // converged: 10 scored, N=10
	b.recordRun(captaincode.LegGLM, "[user]\nreal task\n\n", bigResult(), "")
	assert.Equal(t, 0, calls, "converged scorecard → no director call")
	if evs := b.ledger.Events; assert.Len(t, evs, 11) {
		assert.Zero(t, evs[10].Quality)
		assert.Equal(t, "ok", evs[10].Outcome, "run still recorded for freshness")
	}
}

func TestAssessPolicy_ConvergedLegGetsPeriodicRefresh(t *testing.T) {
	calls := 0
	b := newRecordBrain(8.0, nil)
	base := b.assessFn
	b.assessFn = func(task, output, obj string) (captaincode.Assessment, error) {
		calls++
		return base(task, output, obj)
	}
	seedScored(b, captaincode.LegGLM, 19) // N=19 → this run is the 20th: refresh tick
	b.recordRun(captaincode.LegGLM, "[user]\nreal task\n\n", bigResult(), "")
	assert.Equal(t, 1, calls, "every refreshEvery-th run keeps a converged estimate honest")
}

func TestAssessPolicy_EnvDisablesScoring(t *testing.T) {
	t.Setenv("CAPTAIN_ASSESS_MIN_SCORED", "0")
	t.Setenv("CAPTAIN_ASSESS_REFRESH_EVERY", "0")
	calls := 0
	b := newRecordBrain(8.0, nil)
	base := b.assessFn
	b.assessFn = func(task, output, obj string) (captaincode.Assessment, error) {
		calls++
		return base(task, output, obj)
	}
	b.recordRun(captaincode.LegGLM, "[user]\nreal task\n\n", bigResult(), "")
	assert.Equal(t, 0, calls)
	assert.Len(t, b.ledger.Events, 1, "recording is free and always on; only SCORING is disabled")
}

// ── provider-down cooldown + fallback director (xAI outage 2026-07-19) ──

func TestWriteWorkerError_ProviderDownIs503WithClearMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeWorkerError(rec, captaincode.LegGrok, fmt.Errorf("wrap: %w", captaincode.ErrProviderDown))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "temporarily unavailable")
	assert.Contains(t, rec.Body.String(), "provider_unavailable")
}

func TestOnWorkerError_ProviderDownGetsShortCooldown(t *testing.T) {
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}}
	b.onWorkerError(captaincode.LegGrok, fmt.Errorf("x: %w", captaincode.ErrProviderDown))
	until, ok := b.ledger.Cooldowns[captaincode.LegGrok]
	require.True(t, ok, "a down provider must cool down so routing steers around it")
	d := time.Until(until)
	assert.Greater(t, d, 5*time.Minute)
	assert.Less(t, d, 15*time.Minute, "outages are transient - must be far shorter than the 30m rate-limit window")
}

// The DIRECTOR failing repeatedly must not degrade routing to the dumb ladder
// for the whole outage: the brain demotes down an ordered director ladder
// (user preference 2026-07-19: claude → codex → grok) and retries the primary
// once the fallback window expires.
func TestDirectorLadder_DemotesThroughChoicesAndReverts(t *testing.T) {
	ladder := []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok}
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}, dirLadder: ladder}
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector(), "healthy: first choice directs")

	b.noteDirectorOutcome(errors.New("unavailable"))
	b.noteDirectorOutcome(errors.New("unavailable"))
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector(), "two failures: not yet")

	b.noteDirectorOutcome(nil) // success resets the streak
	b.noteDirectorOutcome(errors.New("unavailable"))
	b.noteDirectorOutcome(errors.New("unavailable"))
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector(), "streak was reset by the success")

	b.noteDirectorOutcome(errors.New("unavailable"))
	assert.Equal(t, captaincode.LegCodex, b.effectiveDirector(), "3 consecutive failures → second choice")

	// second choice also failing → third choice
	b.noteDirectorOutcome(errors.New("down"))
	b.noteDirectorOutcome(errors.New("down"))
	b.noteDirectorOutcome(errors.New("down"))
	assert.Equal(t, captaincode.LegGrok, b.effectiveDirector(), "second choice down too → third choice")

	// third choice keeps failing → stays on the last rung, never past it
	b.noteDirectorOutcome(errors.New("down"))
	b.noteDirectorOutcome(errors.New("down"))
	b.noteDirectorOutcome(errors.New("down"))
	assert.Equal(t, captaincode.LegGrok, b.effectiveDirector())

	b.dirFallbackUntil = time.Now().Add(-time.Second) // outage window elapsed
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector(), "window expires → first choice retried")
	b.noteDirectorOutcome(nil)
	assert.Equal(t, captaincode.LegClaude, b.effectiveDirector(), "healthy again on the primary")
}

func TestDirectorLadderFromEnv(t *testing.T) {
	t.Setenv("CAPTAIN_DIRECTOR", "claude,codex,grok")
	got := directorLadderFromEnv()
	assert.Equal(t, []captaincode.Leg{captaincode.LegClaude, captaincode.LegCodex, captaincode.LegGrok}, got[:3],
		"the stated preference leads, in order")

	t.Setenv("CAPTAIN_DIRECTOR", "claude")
	got = directorLadderFromEnv()
	assert.Equal(t, captaincode.LegClaude, got[0])
	assert.Contains(t, got, captaincode.LegCodex, "defaults fill the ladder so there is always a fallback")
}

// ── automatic worker reroute (xAI capacity errors, 2026-07-19) ──
//
// When a worker leg's provider is down/at capacity, the turn must not die
// with an error the user has to retry by hand: cool the leg, pick the next
// best available leg, and rerun the same task there.

func TestRerouteTarget_SkipsFailedCooledAndDisallowed(t *testing.T) {
	b := &brain{
		ledger:  &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		allowed: map[captaincode.Leg]bool{captaincode.LegGrok: true, captaincode.LegCodex: true, captaincode.LegGLM: true},
	}
	b.ledger.Cooldown(captaincode.LegGrok, 10*time.Minute)  // the leg that just failed
	b.ledger.Cooldown(captaincode.LegCodex, 10*time.Minute) // also cooling
	fb, ok := b.rerouteTarget(captaincode.LegGrok, "fix the webhook retry")
	require.True(t, ok)
	assert.Equal(t, captaincode.LegGLM, fb, "next allowed, non-cooled leg")
}

func TestRerouteTarget_NoneAvailable(t *testing.T) {
	b := &brain{
		ledger:  &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		allowed: map[captaincode.Leg]bool{captaincode.LegGrok: true},
	}
	b.ledger.Cooldown(captaincode.LegGrok, 10*time.Minute)
	_, ok := b.rerouteTarget(captaincode.LegGrok, "task")
	assert.False(t, ok, "nowhere to reroute → surface the original error")
}

func TestRerouteTarget_ImageTaskGoesToVisionLeg(t *testing.T) {
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}}
	b.ledger.Cooldown(captaincode.LegGrok, 10*time.Minute)
	fb, ok := b.rerouteTarget(captaincode.LegGrok, "fix the logo per screens/bug-logo.png")
	require.True(t, ok)
	assert.True(t, captaincode.LegSupportsVision(fb), "rerouting an image task to a blind leg recreates the vision bug")
}

// TestBrainRoute_PreferIsUsersNotLadderAnchor: the route handler used to pass
// the deterministic ladder's first open leg as the director's "Preference
// hint" - with qwen/kimi excluded that was "grok" on every medium task, and
// the director dutifully followed it ("matches the preference hint") instead
// of weighing glm/minimax/codex on merit (live 2026-07-19). The hint must be
// the USER's preference (quality|speed|save) or nothing.
func TestBrainRoute_PreferIsUsersNotLadderAnchor(t *testing.T) {
	t.Setenv("CAPTAIN_LANES", "0") // the director's hint; the quality lane asks no director
	var gotPrefer string
	b := &brain{
		ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		planFn: func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			gotPrefer = prefer
			return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGLM, Brief: "b"}}}, nil
		},
	}
	body, _ := json.Marshal(routeReq{Task: "update the deck blurb"})
	rec := httptest.NewRecorder()
	b.route(rec, httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, "", gotPrefer, "no user preference → no hint; a leg name here anchors the director")

	body, _ = json.Marshal(routeReq{Task: "update the deck blurb", Prefer: "quality"})
	rec = httptest.NewRecorder()
	b.route(rec, httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, "quality", gotPrefer, "the USER's preference passes through untouched")
}

// A leg whose runs stall repeatedly (dead provider stream, wedged turns)
// deserves the same short bench as a down provider - live 2026-07-24: GLM/NIM
// stalled twice in a row and the turn errored instead of rerouting.
func TestOnWorkerError_StallGetsShortCooldown(t *testing.T) {
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}}
	b.onWorkerError(captaincode.LegGLM, fmt.Errorf("x: %w", captaincode.ErrWorkerStalled))
	until, ok := b.ledger.Cooldowns[captaincode.LegGLM]
	require.True(t, ok)
	assert.Less(t, time.Until(until), 15*time.Minute)
}

// Every worker failure the brain handles must land on the ledger - reliability
// is a routing signal, not just an error message.
func TestOnWorkerError_RecordsFailureEvent(t *testing.T) {
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}}
	b.onWorkerError(captaincode.LegGLM, fmt.Errorf("x: %w", captaincode.ErrWorkerStalled))
	b.onWorkerError(captaincode.LegCodex, errors.New("some generic explosion"))
	require.Len(t, b.ledger.Events, 2, "stalls AND generic errors both recorded")
	assert.Equal(t, "fail", b.ledger.Events[0].Outcome)
	assert.Equal(t, captaincode.LegGLM, b.ledger.Events[0].Leg)
	assert.Contains(t, b.ledger.Events[0].Error, "stalled")
	_, cooled := b.ledger.Cooldowns[captaincode.LegCodex]
	assert.False(t, cooled, "generic errors record but don't bench the leg")
}

// ── narration-only outputs (2026-07-25) ──
//
// cursor-agent ended a turn with "I'll map the artifacts… Drafting the plan…"
// and no plan - the user had to type "Do it you stopped". The wrapper must
// detect intention-only outputs and nudge the worker once.
func TestNarrationOnly(t *testing.T) {
	yes := []string{
		"I'll map Lma and PoR artifacts in-repo, then draft a concrete convergence implementation plan.",
		"Let me gather the standing surfaces first. Drafting the convergence plan from the verified seams.",
		"I'm going to check the harness profiles now.",
	}
	for _, s := range yes {
		assert.True(t, narrationOnly(s), s)
	}
	no := []string{
		"I'll keep this short: the plan is below.\n\n## Phase 1\n- Extract the shared CCT schema\n- Bind DailyStanding to coverage certificates\n\n## Phase 2\n- Bridge demo: harness profile emits D1 derivations\n- Oracle-relative honesty checks reuse the Lma custodian signature path\n\n## Phase 3\n- Selective disclosure fold across both artifact families with versioned @policy compilation into circuits, plus a migration note for existing standing consumers.",
		"The claim is false as engineering fact.", // short but a real verdict, no intention-opener
		"Done. Created 11-afa/opensfdr-wedge.md.", // short completion report
	}
	for _, s := range no {
		assert.False(t, narrationOnly(s), s)
	}
}

// /quality must constrain the director's MENU to the top-quality legs - the
// binding prompt language alone let it rationalize the free leg off its
// trivial-class average (live 2026-07-25).
func TestBrainRoute_QualityConstrainsMenu(t *testing.T) {
	// The director's menu; the lane over the same menu: brain_lanes_test.go.
	t.Setenv("CAPTAIN_LANES", "0")
	defer captaincode.SetDirector(captaincode.Director) // restore
	captaincode.SetDirector(captaincode.LegClaude)      // production config: claude directs
	var gotOpen []captaincode.Leg
	b := &brain{
		ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		planFn: func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			gotOpen = open
			return captaincode.Plan{Class: captaincode.ClassTrivial, Workers: []captaincode.Worker{{Leg: open[0], Brief: "b"}}}, nil
		},
	}
	body, _ := json.Marshal(routeReq{Task: "tiny docstring tweak", Prefer: "quality"})
	rec := httptest.NewRecorder()
	b.route(rec, httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.NotEmpty(t, gotOpen)
	assert.LessOrEqual(t, len(gotOpen), 2, "menu holds only the strongest legs")
	for _, l := range gotOpen {
		assert.NotEqual(t, captaincode.LegFree, l, "/quality can never offer the free leg")
	}
	// The user asked for the BEST - that includes the director's own model as
	// a worker ("director never assigns itself" yields to an explicit user
	// quality request; claude runs via claude -p like a forced leg). Live
	// 2026-07-25: /quality picked cursor because claude wasn't in the pool.
	assert.Contains(t, gotOpen, captaincode.Director, "/quality menu must include the director's model")
}

// /team as a FORCED choice: the user explicitly wants an ensemble; the
// wrapper plans it (cache miss → director fan-out plan) and executes.
func TestBrainRoute_ForcedTeam(t *testing.T) {
	b := &brain{ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}}}
	body, _ := json.Marshal(routeReq{Task: "audit everything", Forced: "team"})
	rec := httptest.NewRecorder()
	b.route(rec, httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp routeResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "team", resp.Model)
	assert.Equal(t, "captain", resp.Provider)
}

// The route carries the effort: the preference the user stated, else the
// class the router settled on (effort.go, 2026-09-13).
func TestBrainRoute_CarriesTheEffort(t *testing.T) {
	b := &brain{
		ledger: &captaincode.Ledger{Cooldowns: map[captaincode.Leg]time.Time{}},
		planFn: func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			return captaincode.Plan{Class: captaincode.ClassHigh}, nil
		},
	}
	route := func(task, forced, prefer string) routeResp {
		body, _ := json.Marshal(routeReq{Task: task, Forced: forced, Prefer: prefer})
		rec := httptest.NewRecorder()
		b.route(rec, httptest.NewRequest(http.MethodPost, "/v1/route", bytes.NewReader(body)))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var resp routeResp
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return resp
	}
	assert.Equal(t, "low", route("fix typo in README", "codex", "").Effort, "a trivial task thinks little")
	assert.Equal(t, "high", route("fix typo in README", "codex", "quality").Effort, "/quality outranks the rating")
	assert.Equal(t, "high", route("/quality fix typo in README", "codex", "").Effort, "…also mid-prompt")
	assert.Equal(t, "low", route("refactor the concurrency architecture /speed", "codex", "").Effort, "/speed outranks the rating")
	assert.Equal(t, "high", route("refactor the concurrency architecture across the codebase", "codex", "").Effort, "a hard task thinks hard")
}
