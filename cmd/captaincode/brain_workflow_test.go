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
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Captain Workflow Language executor - spec: docs/WORKFLOW_LANGUAGE.md.
// A workflow is the USER's topology: stages in sequence, legs in parallel, and
// exactly ONE output - the director's review of the terminal stage.

func wfReq(stream bool, turns ...string) *http.Request {
	msgs := make([]map[string]string, 0, len(turns))
	for i, t := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]string{"role": role, "content": t})
	}
	body, _ := json.Marshal(map[string]any{"model": "claude", "stream": stream, "messages": msgs})
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
}

func answerOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var got struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
	require.Len(t, got.Choices, 1)
	return got.Choices[0].Message.Content
}

// A typed expression must reach the workflow executor without any fork change:
// the fork forwards the message verbatim, so the wrapper detects the grammar.
func TestTypedExpressionRunsAsWorkflow(t *testing.T) {
	b := teamBrain()
	var mu sync.Mutex
	var order []string
	var prompts []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		order = append(order, string(leg))
		prompts = append(prompts, prompt)
		mu.Unlock()
		return leg, captaincode.Result{Text: "output of " + string(leg), DurationMs: 10, Tokens: 5}, nil
	}
	var reviewed map[string]captaincode.WorkerOutput
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		reviewed = outputs
		assert.Contains(t, objective, "ONLY the deliverable", "the review instruction is executor-supplied")
		assert.Contains(t, objective, "adjudicate", "two terminal workers ⇒ attribution + adjudication")
		ma := captaincode.MultiAssessment{Synthesis: "REVIEWED-AGGREGATE"}
		for title := range outputs {
			ma.Scores = append(ma.Scores, struct {
				Worker  string  `json:"worker"`
				Quality float64 `json:"quality"`
				Verdict string  `json:"verdict"`
				Notes   string  `json:"notes"`
			}{Worker: title, Quality: 9, Verdict: "good"})
		}
		return ma, nil
	}

	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false,
		"/grok analyse the queue > /cursor review the analysis > /codex red-team it + /claude red-team it"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, order, 4, "4 worker runs")
	assert.Equal(t, []string{"grok", "cursor"}, order[:2], "stages run in order")
	assert.ElementsMatch(t, []string{"codex", "claude"}, order[2:], "terminal stage runs in parallel")

	// Only the terminal stage is reviewed, and only the review is delivered.
	require.Len(t, reviewed, 2)
	answer := answerOf(t, rec)
	assert.Contains(t, answer, "REVIEWED-AGGREGATE")
	assert.NotContains(t, answer, "output of grok", "no worker text reaches the answer")
	assert.NotContains(t, answer, "output of codex")
	assert.Contains(t, answer, "wf", "provenance footer names the workflow")

	// Stage 2+ must receive the upstream output, labeled, plus its assignment.
	assert.Contains(t, prompts[1], "output of grok")
	assert.Contains(t, prompts[1], "review the analysis")
	assert.Contains(t, prompts[1], "stage 2 of 3")
	// Stage 1 has no upstream.
	assert.NotContains(t, prompts[0], "stage 1 of 3 - inputs")
	// Routing syntax never reaches a worker.
	for _, p := range prompts {
		assert.NotContains(t, p, "/cursor review")
		assert.NotContains(t, p, "> /codex")
	}
}

func TestInheritedStageGetsTheFixedInstruction(t *testing.T) {
	b := teamBrain()
	var mu sync.Mutex
	var prompts []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 10}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok write the docstring > /claude"))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, prompts, 2)
	// Semantic change 2026-08-24 (user request): a bare leg REPEATS the
	// previous stage's assignment (with upstream outputs visible) instead of
	// receiving the fixed "improve the previous output" instruction.
	assert.Contains(t, prompts[1], "Your assignment: write the docstring")
	assert.Contains(t, prompts[1], "out grok", "upstream output is fed to the bare stage")
}

// The user must be able to see which step the team is on at any moment (§5).
func TestWorkflowStreamsAStepTracker(t *testing.T) {
	fastProgress(t)
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		onStatus("⚙ Read queue.ts")
		time.Sleep(60 * time.Millisecond)
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 60}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(true, "/grok analyse it > /cursor review it"))

	content, reasoning := sseDeltas(rec.Body.String())
	assert.Contains(t, content, "AGG")
	assert.NotContains(t, content, "out grok", "worker text stays out of the answer")
	// Checklist: every stage listed, with progress markers and the review step.
	assert.Contains(t, reasoning, "[1/2]")
	assert.Contains(t, reasoning, "[2/2]")
	assert.Contains(t, reasoning, "[rev]")
	assert.Contains(t, reasoning, "grok")
	assert.Contains(t, reasoning, "cursor")
	assert.Contains(t, reasoning, "✓", "finished steps are marked done")
	assert.Contains(t, reasoning, "⚙ Read queue.ts", "worker tool activity is visible")
	assert.GreaterOrEqual(t, strings.Count(reasoning, "[1/2]"), 2, "the checklist re-prints on transitions")
}

func TestWorkflowRecordsLedgerEventsAndAggregate(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 10, Tokens: 7}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		ma := captaincode.MultiAssessment{Synthesis: "AGG"}
		for title := range outputs {
			ma.Scores = append(ma.Scores, struct {
				Worker  string  `json:"worker"`
				Quality float64 `json:"quality"`
				Verdict string  `json:"verdict"`
				Notes   string  `json:"notes"`
			}{Worker: title, Quality: 8, Verdict: "ok"})
		}
		return ma, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok analyse it > /cursor review it"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	b.mu.Lock()
	defer b.mu.Unlock()
	key := "grok>cursor"
	var workers, aggregates int
	for _, e := range b.ledger.Events {
		require.Equal(t, key, e.Workflow, "every event carries the workflow key")
		if e.Leg == "" {
			aggregates++
			assert.Greater(t, e.Quality, 0.0, "the aggregate carries the review's score")
			continue
		}
		workers++
		assert.Equal(t, "ok", e.Outcome)
	}
	assert.Equal(t, 2, workers)
	assert.Equal(t, 1, aggregates, "exactly one workflow-level aggregate")

	// The terminal worker is scored by the review; the non-terminal one is not.
	for _, e := range b.ledger.Events {
		switch e.Leg {
		case captaincode.LegCursor:
			assert.Equal(t, 8.0, e.Quality, "reviewed workers get their score")
		case captaincode.LegGrok:
			assert.Zero(t, e.Quality, "nobody assessed the non-terminal stage")
		}
	}
}

// Grading is not availability: a failed review still delivers the work.
func TestReviewFailureFallsBackToLabeledOutputs(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "critique from " + string(leg), DurationMs: 10}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{}, fmt.Errorf("director unavailable")
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/codex red-team it + /claude red-team it"))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	answer := answerOf(t, rec)
	assert.Contains(t, answer, "review unavailable")
	assert.Contains(t, answer, "critique from codex")
	assert.Contains(t, answer, "critique from claude")
}

// A stage that loses every worker aborts, but the completed stages are still
// reviewed - partial work is never discarded.
func TestEmptyStageAbortsButReviewsWhatRan(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if leg == captaincode.LegCursor {
			return leg, captaincode.Result{}, fmt.Errorf("boom")
		}
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 10}, nil
	}
	var seen map[string]captaincode.WorkerOutput
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		seen = outputs
		return captaincode.MultiAssessment{Synthesis: "PARTIAL-AGG"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok analyse it > /cursor review it > /codex ship it"))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	answer := answerOf(t, rec)
	assert.Contains(t, answer, "PARTIAL-AGG")
	require.Len(t, seen, 1, "stage 1's output is what survived")
	for _, o := range seen {
		assert.Equal(t, "out grok", o.Text)
	}
}

func TestWorkflowKillSwitch(t *testing.T) {
	t.Setenv("CAPTAIN_WORKFLOW", "0")
	b := teamBrain()
	called := false
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		called = true
		return leg, captaincode.Result{Text: "single leg answer", DurationMs: 10}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok analyse it > /cursor review it"))
	require.Equal(t, 200, rec.Code)
	assert.True(t, called)
	assert.Equal(t, "single leg answer", answerOf(t, rec), "with the switch off it is one ordinary run")
}

// A capped worker that already produced text must still contribute: the paper
// audit lost claude's and codex's entire reviews to the 8m cap and delivered
// nothing (live 2026-07-30).
func TestCappedWorkerOutputIsDeliveredNotDropped(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if leg == captaincode.LegClaude {
			return leg, captaincode.Result{Text: "Finding 1: the threat model omits X.", DurationMs: 10},
				fmt.Errorf("claude -p did not finish within 15m0s: %w", captaincode.ErrWorkerTimeout)
		}
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 10}, nil
	}
	var reviewed map[string]captaincode.WorkerOutput
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		reviewed = outputs
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok draft it > /claude audit it"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	require.Len(t, reviewed, 1, "the capped worker still reaches the review")
	for _, o := range reviewed {
		assert.Contains(t, o.Text, "Finding 1", "its work is preserved")
		assert.Contains(t, o.Text, "cut off at the time limit", "and labelled as partial")
	}
	assert.Contains(t, answerOf(t, rec), "AGG")
}

// A cap with NOTHING produced is still a failure - nothing to salvage.
func TestCappedWorkerWithNoOutputStillFails(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if leg == captaincode.LegClaude {
			return leg, captaincode.Result{}, fmt.Errorf("did not finish: %w", captaincode.ErrWorkerTimeout)
		}
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 10}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		require.Len(t, outputs, 1, "only grok's stage-1 output survived")
		return captaincode.MultiAssessment{Synthesis: "PARTIAL"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok draft it > /claude audit it"))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, answerOf(t, rec), "PARTIAL")
}

// "Not sure what it's been doing" - the step tracker lives in a reasoning block
// the TUI collapses by default, so a run needs a surface that does not depend on
// the thinking mode: /v1/workflow/status (and `captain status`/`watch`).
func TestWorkflowStatusEndpointReportsTheLiveStep(t *testing.T) {
	b := teamBrain()
	started := make(chan struct{})
	release := make(chan struct{})
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if leg == captaincode.LegGrok {
			close(started)
			<-release
		}
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}

	go b.chatCompletions(httptest.NewRecorder(), wfReq(false, "/grok analyse it > /claude review it"))
	<-started

	rec := httptest.NewRecorder()
	b.workflowStatus(rec, httptest.NewRequest(http.MethodGet, "/v1/workflow/status", nil))
	require.Equal(t, 200, rec.Code)
	var st struct {
		Active    bool   `json:"active"`
		Key       string `json:"key"`
		Stages    int    `json:"stages"`
		Checklist string `json:"checklist"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	assert.True(t, st.Active, "a running workflow reports itself active")
	assert.Equal(t, "grok>claude", st.Key)
	assert.Equal(t, 2, st.Stages)
	assert.Contains(t, st.Checklist, "[1/2]")
	assert.Contains(t, st.Checklist, "▸", "the live step is marked running")
	close(release)
}

func TestWorkflowWritesAFullTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "FULL REVIEW BY " + string(leg), DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "SHORT AGGREGATE"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok analyse it > /claude review it"))
	require.Equal(t, 200, rec.Code)

	matches, err := filepath.Glob(filepath.Join(home, ".captaincode", "runs", "*.md"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "one transcript per workflow")
	body, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, "FULL REVIEW BY grok", "the review compresses; the transcript keeps everything")
	assert.Contains(t, text, "FULL REVIEW BY claude")
	assert.Contains(t, text, "SHORT AGGREGATE")
	assert.Contains(t, answerOf(t, rec), matches[0], "the answer points at the transcript")
}

// The fork re-issues a turn's request; each retry used to start a whole new
// workflow, so one turn ran the same 3-worker audit twice and the answer went to
// an abandoned request (live 2026-07-30: three "Thinking" blocks, no answer).
func TestRetriedRequestAttachesInsteadOfRerunning(t *testing.T) {
	old := wfAttachRefresh
	wfAttachRefresh = 10 * time.Millisecond
	defer func() { wfAttachRefresh = old }()

	b := teamBrain()
	var mu sync.Mutex
	calls := 0
	gate := make(chan struct{})
	firstIn := make(chan struct{})
	var once sync.Once
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		once.Do(func() { close(firstIn) })
		<-gate // hold the run open so the retry arrives mid-flight
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "THE-ONE-ANSWER"}, nil
	}

	expr := "/grok analyse the paper > /claude audit it"
	rec1, rec2 := httptest.NewRecorder(), httptest.NewRecorder()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); b.chatCompletions(rec1, wfReq(false, expr)) }()
	<-firstIn

	wg.Add(1)
	go func() { defer wg.Done(); b.chatCompletions(rec2, wfReq(false, expr)) }() // the retry
	time.Sleep(30 * time.Millisecond)
	close(gate)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, calls, "two stages ran ONCE each - the retry must not duplicate the work")
	assert.Contains(t, answerOf(t, rec1), "THE-ONE-ANSWER")
	assert.Contains(t, answerOf(t, rec2), "THE-ONE-ANSWER", "the retry gets the same answer")
}

// A repeat shortly after completion is served from the result, not re-run: the
// user who resends because they saw nothing gets the answer instantly.
func TestRepeatWithinTTLServesTheStoredAnswer(t *testing.T) {
	b := teamBrain()
	calls := 0
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		calls++
		return leg, captaincode.Result{Text: "out", DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "STORED"}, nil
	}
	expr := "/grok a > /claude b"
	rec1 := httptest.NewRecorder()
	b.chatCompletions(rec1, wfReq(false, expr))
	require.Equal(t, 2, calls)

	rec2 := httptest.NewRecorder()
	b.chatCompletions(rec2, wfReq(false, expr))
	assert.Equal(t, 2, calls, "no worker re-ran")
	assert.Contains(t, answerOf(t, rec2), "STORED")
}

// The fork names each session by calling the same model with the user's text.
// That request must never become a team or a workflow: it fired a second
// 3-worker audit alongside the real one (live 2026-07-30).
func TestTitleRequestNeverRunsAWorkflowOrTeam(t *testing.T) {
	b := teamBrain()
	var legs []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		legs = append(legs, string(leg))
		return leg, captaincode.Result{Text: "Paper audit", DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		t.Fatal("a title request must not reach the review")
		return captaincode.MultiAssessment{}, nil
	}
	b.planFn = func(string, captaincode.Class, string, []captaincode.Leg, map[captaincode.Leg]captaincode.LegStats, map[string]captaincode.TeamStat, bool) (captaincode.Plan, error) {
		t.Fatal("a title request must not be planned")
		return captaincode.Plan{}, nil
	}

	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false, "messages": []map[string]string{
		{"role": "system", "content": "You are a title generator. Reply with a short title."},
		{"role": "user", "content": "Have grok, codex and then claude Review the research paper"},
	}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"free"}, legs, "one cheap run, no fan-out")
	assert.Equal(t, "Paper audit", answerOf(t, rec))
}

// R2: a stage with a gate is done only when the gate passes. One bounded retry
// with the gate's output; a second failure delivers the work WITH the failure
// stated - never silently.
func TestStageGatePassesAndFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_CWD", home)

	t.Run("gate failure feeds back once, then retry passes", func(t *testing.T) {
		marker := filepath.Join(home, "fixed")
		var mu sync.Mutex
		var prompts []string
		b := teamBrain()
		b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
			mu.Lock()
			prompts = append(prompts, prompt)
			n := len(prompts)
			mu.Unlock()
			if n == 2 { // the retry "fixes" it
				os.WriteFile(marker, []byte("ok"), 0o644)
			}
			return leg, captaincode.Result{Text: fmt.Sprintf("attempt %d", n), DurationMs: 5}, nil
		}
		b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
			return captaincode.MultiAssessment{Synthesis: "GATED-AGG"}, nil
		}
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, wfReq(false, "/grok fix the tests gate: test -f "+marker+" > /claude review"))
		require.Equal(t, 200, rec.Code, rec.Body.String())

		mu.Lock()
		defer mu.Unlock()
		require.GreaterOrEqual(t, len(prompts), 3, "attempt, gated retry, then stage 2")
		assert.Contains(t, prompts[1], "gate", "the retry sees why the gate failed")
		assert.Contains(t, prompts[1], "attempt 1", "and its own prior output")
		assert.Contains(t, answerOf(t, rec), "GATED-AGG")
	})

	t.Run("gate failing twice is delivered as a failure, not silence", func(t *testing.T) {
		b := teamBrain()
		b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
			return leg, captaincode.Result{Text: "claims it is fixed", DurationMs: 5}, nil
		}
		var reviewed string
		b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
			for _, o := range outputs {
				reviewed = o.Text
			}
			return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
		}
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, wfReq(false, "/grok fix it gate: false"))
		require.Equal(t, 200, rec.Code)
		assert.Contains(t, reviewed, "GATE FAILED", "the review judges the work knowing the gate failed")
	})
}

// A typed workflow must outrank the forced pseudo-model short-circuit: the
// fork sends model=frontier for "/frontier draft > /codex review", but the
// user wrote a topology (2026-08-25) - frontierChat must NOT swallow it.
func TestFrontierLedWorkflowOutranksForcedFrontier(t *testing.T) {
	b := teamBrain()
	var legs []string
	var mu sync.Mutex
	b.frontierFn = func(prompt string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		mu.Lock()
		legs = append(legs, "frontier")
		mu.Unlock()
		return captaincode.Result{Text: "draft out", DurationMs: 10}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		legs = append(legs, string(leg))
		mu.Unlock()
		return leg, captaincode.Result{Text: "review out", DurationMs: 10}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "frontier", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/frontier draft the plan > /codex review it"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"frontier", "codex"}, legs, "both stages must run, in order")
	assert.Contains(t, rec.Body.String(), "AGG")
}

// M5.1: gate check results from the workflow executor must be wired into the
// task's outcome evidence so the acceptance record includes what the gate saw.
func TestWorkflowGateCheckResultsRecordedAsOutcome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_CWD", home)
	marker := filepath.Join(home, "fixed")
	var mu sync.Mutex
	var prompts []string
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		n := len(prompts)
		mu.Unlock()
		if n == 2 {
			os.WriteFile(marker, []byte("ok"), 0o644)
		}
		return leg, captaincode.Result{Text: fmt.Sprintf("attempt %d", n), DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok fix the tests gate: test -f "+marker+" > /claude review"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	b.mu.Lock()
	defer b.mu.Unlock()
	var hasGateCheck bool
	for _, o := range b.ledger.Outcomes {
		for _, c := range o.Checks {
			if c.Source == "gate" {
				hasGateCheck = true
				assert.Contains(t, c.Command, "test -f")
			}
		}
	}
	assert.True(t, hasGateCheck, "gate check results must be recorded in outcome evidence")
}

// M3.5: `captain handoff <task-id> --format json` must output the full brief
// as JSON for machine consumption by M4 hosts.
func TestHandoffFormatJSON(t *testing.T) {
	brief := captaincode.HandoffBrief{
		Version:      captaincode.HandoffVersion,
		TaskID:       "t-json",
		Requirements: "fix the bug",
		State:        captaincode.StateSucceeded,
	}
	body, _ := json.Marshal(brief)
	var decoded captaincode.HandoffBrief
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, "t-json", decoded.TaskID)
	assert.Equal(t, "fix the bug", decoded.Requirements)
	assert.Equal(t, captaincode.StateSucceeded, decoded.State)
}

// A frontier stage refused at the door (the monthly spend limit, 5s, no
// output) reroutes to claude like a solo /frontier turn does, instead of
// aborting the workflow with "every stage failed" (live 2026-09-20: a
// two-stage frontier>cursor request died on stage 1 while the same prompt
// solo was rerouted and answered).
func TestWorkflowFrontierStageReroutesWhenTheTierIsClosed(t *testing.T) {
	b := teamBrain()
	var mu sync.Mutex
	var legs []string
	b.frontierFn = func(prompt string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		mu.Lock()
		legs = append(legs, "frontier")
		mu.Unlock()
		return captaincode.Result{DurationMs: 5000}, &captaincode.RateLimitError{Leg: captaincode.LegFrontier, Tier: "frontier",
			Msg: "You've hit your monthly spend limit. Switch to another model, or manage usage credits at claude.ai/settings/usage?from=cc_cli_limit_message, to continue."}
	}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		legs = append(legs, string(leg))
		mu.Unlock()
		return leg, captaincode.Result{Text: "stage out from " + string(leg), DurationMs: 10}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "frontier", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/frontier design the explorer > /cursor test and deploy it"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"frontier", "claude", "cursor"}, legs, "stage 1 reroutes to claude, stage 2 still runs")
	assert.NotContains(t, rec.Body.String(), "every stage failed")
	assert.Contains(t, rec.Body.String(), "AGG")
	b.mu.Lock()
	_, benched := b.ledger.Cooldowns[captaincode.LegFrontier]
	b.mu.Unlock()
	assert.True(t, benched, "the closed tier is benched so the next stage does not knock again")
}
