package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Programs (CWL level 2) - spec: docs/WORKFLOW_LANGUAGE.md §11.

func fastProgram(t *testing.T) {
	t.Helper()
	old := programPause
	programPause = 10 * time.Millisecond
	t.Cleanup(func() { programPause = old })
}

// typeProgram sends text as a typed turn and waits for the program it starts
// to finish: the launching turn watches it, so it returns when it ends.
func typeProgram(t *testing.T, b *brain, text string) (string, *repeatThread) {
	t.Helper()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { b.chatCompletions(rec, repeatReq(text)); close(done) }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatalf("the program never finished: %q", text)
	}
	b.rmu.Lock()
	defer b.rmu.Unlock()
	var th *repeatThread
	for _, x := range b.repeatState() {
		if x.program != nil && x.task == text {
			th = x
		}
	}
	return rec.Body.String(), th
}

func twoWorkerPlan(_ string, _ captaincode.Class, _ string, _ []captaincode.Leg, _ map[captaincode.Leg]captaincode.LegStats, _ map[string]captaincode.TeamStat, _ bool) (captaincode.Plan, error) {
	return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "r",
		Workers: []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "a"}, {Leg: captaincode.LegCursor, Brief: "b"}}}, nil
}

// A chain hands each step the end of the step before: a /team answer reaches
// the leg that implements it, without routing syntax.
func TestProgramChainsATeamIntoALeg(t *testing.T) {
	fastProgram(t)
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return promptPeek(text) }
	var planned atomic.Int32
	b.planFn = func(task string, c captaincode.Class, prefer string, open []captaincode.Leg, st map[captaincode.Leg]captaincode.LegStats, tm map[string]captaincode.TeamStat, fan bool) (captaincode.Plan, error) {
		planned.Add(1)
		return twoWorkerPlan(task, c, prefer, open, st, tm, fan)
	}
	var mu sync.Mutex
	codexPrompt := ""
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		if leg == captaincode.LegCodex {
			codexPrompt = prompt
			return leg, captaincode.Result{Text: "IMPLEMENTED the endpoint", DurationMs: 5}, nil
		}
		return leg, captaincode.Result{Text: "notes from " + string(leg), DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "TEAM-FINDINGS: use REST"}, nil
	}

	body, th := typeProgram(t, b, "/team research the API > /codex implement it")
	require.NotNil(t, th, "a program thread ran")
	assert.Equal(t, int32(1), planned.Load(), "step 1 ran the team path")
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, codexPrompt, "TEAM-FINDINGS: use REST", "step 2 reads the end of step 1's answer")
	assert.Contains(t, codexPrompt, "implement it")
	assert.Contains(t, codexPrompt, "one turn of a program", "every turn carries the program contract")
	assert.NotContains(t, codexPrompt, "/team research", "no routing syntax reaches a worker")
	assert.NotContains(t, codexPrompt, "/codex implement")

	assert.Contains(t, body, "program "+th.id+" started")
	assert.Contains(t, body, "1. /team research the API", "the launching turn shows how the program was read")
	assert.Contains(t, body, "turn 2 · step 2/2", "turns are named by where they sit")
	assert.Contains(t, body, "IMPLEMENTED the endpoint", "the last turn's answer closes the program")
	assert.Equal(t, "done", th.stopReason)
}

// The handoff is a conversation turn, so the FIRST stage of a workflow step
// reads it - not only the stage the step's text ends on.
func TestProgramHandoffReachesTheFirstStageOfAWorkflowStep(t *testing.T) {
	fastProgram(t)
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return promptPeek(text) }
	b.planFn = twoWorkerPlan
	var calls atomic.Int32
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		if calls.Add(1) == 1 {
			return captaincode.MultiAssessment{Synthesis: "TEAM-SYNTH: three endpoints"}, nil
		}
		return captaincode.MultiAssessment{Synthesis: "WF-REVIEW"}, nil
	}
	var mu sync.Mutex
	grokPrompt := ""
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		if leg == captaincode.LegGrok {
			grokPrompt = prompt
		}
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 5}, nil
	}
	_, th := typeProgram(t, b, "/team plan the API > /grok draft it > /claude review the draft")
	require.NotNil(t, th)
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, grokPrompt, "TEAM-SYNTH: three endpoints", "stage 1 of the workflow step reads the team's answer")
	assert.Equal(t, "done", th.stopReason)
}

// until: runs before every round; exit 0 ends the loop as a success. The
// round reads why the check still fails.
func TestProgramLoopEndsWhenUntilPasses(t *testing.T) {
	fastProgram(t)
	marker := filepath.Join(t.TempDir(), "green")
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	var runs atomic.Int32
	var mu sync.Mutex
	var prompts []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		n := runs.Add(1)
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()
		if n == 2 {
			require.NoError(t, os.WriteFile(marker, []byte("ok"), 0o644))
		}
		return leg, captaincode.Result{Text: fmt.Sprintf("fix %d", n), DurationMs: 5}, nil
	}
	_, th := typeProgram(t, b, "/repeat 5 /codex fix the failing test until: test -f "+marker)
	require.NotNil(t, th)
	assert.Equal(t, int32(2), runs.Load(), "the check passed before round 3")
	assert.Contains(t, th.stopReason, "passed after 2 round(s)")
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, prompts[0], "exit check `test -f", "a round reads why the check still fails")
	assert.Contains(t, prompts[0], "This part repeats", "a round knows it repeats")
}

func TestProgramUntilAlreadyPassingRunsNothing(t *testing.T) {
	fastProgram(t)
	b := teamBrain()
	var runs atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs.Add(1)
		return leg, captaincode.Result{Text: "x", DurationMs: 5}, nil
	}
	_, th := typeProgram(t, b, "/repeat 3 /codex fix it until: true")
	require.NotNil(t, th)
	assert.Zero(t, runs.Load(), "a check that already passes costs no turn")
	assert.Contains(t, th.stopReason, "passed after 0 round(s)")
}

// A loop that ends without its check passing has failed, and || runs the
// fallback, which reads what failed.
func TestProgramFallbackRunsWhenTheLoopFails(t *testing.T) {
	fastProgram(t)
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	var mu sync.Mutex
	codex, claudePrompt := 0, ""
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		if leg == captaincode.LegClaude {
			claudePrompt = prompt
			return leg, captaincode.Result{Text: "the fixture is missing", DurationMs: 5}, nil
		}
		codex++
		return leg, captaincode.Result{Text: fmt.Sprintf("try %d", codex), DurationMs: 5}, nil
	}
	body, th := typeProgram(t, b, "/repeat 2 /codex fix it until: false || /claude explain why the tests still fail")
	require.NotNil(t, th)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, codex, "both rounds ran")
	assert.Contains(t, claudePrompt, "This attempt failed", "the fallback reads that the loop failed")
	assert.Contains(t, claudePrompt, "`false` still fails")
	assert.Equal(t, "done", th.stopReason, "the fallback succeeded, so the program did")
	assert.Contains(t, body, "the fixture is missing")
}

// A workflow answers 200 with a review even when a gate still fails; the
// program counts that turn as failed, so || runs.
func TestProgramFallbackRunsWhenAWorkflowGateFails(t *testing.T) {
	fastProgram(t)
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "REVIEWED"}, nil
	}
	var mu sync.Mutex
	diagnosis := ""
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(prompt, "diagnose it") {
			diagnosis = prompt
		}
		return leg, captaincode.Result{Text: "claims it is fixed", DurationMs: 5}, nil
	}
	_, th := typeProgram(t, b, "/codex fix it gate: false || /claude diagnose it")
	require.NotNil(t, th)
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, diagnosis, "the fallback ran")
	assert.Contains(t, diagnosis, "gate `false`", "it reads which gate failed")
}

// A gate on a /team (or lane) turn is the program runner's: one repair, then
// the check again. The solo path used to read it as prose.
func TestProgramGatesATeamTurn(t *testing.T) {
	fastProgram(t)
	marker := filepath.Join(t.TempDir(), "done")
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return promptPeek(text) }
	var mu sync.Mutex
	var tasks []string
	b.planFn = func(task string, c captaincode.Class, prefer string, open []captaincode.Leg, st map[captaincode.Leg]captaincode.LegStats, tm map[string]captaincode.TeamStat, fan bool) (captaincode.Plan, error) {
		mu.Lock()
		tasks = append(tasks, task)
		mu.Unlock()
		return twoWorkerPlan(task, c, prefer, open, st, tm, fan)
	}
	repairSeen := ""
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if strings.Contains(prompt, "FAILED with") {
			mu.Lock()
			repairSeen = prompt
			mu.Unlock()
			_ = os.WriteFile(marker, []byte("ok"), 0o644)
		}
		return leg, captaincode.Result{Text: "worked on it", DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "TEAM-AGG"}, nil
	}
	_, th := typeProgram(t, b, "/team implement the parser gate: test -f "+marker)
	require.NotNil(t, th)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, tasks, 2, "the team ran, the check failed, one repair ran")
	assert.Contains(t, repairSeen, "completion check `test -f", "the repair's workers read the failed check")
	assert.Equal(t, "done", th.stopReason, "the check passed after the repair")
}

func TestProgramStopsAtTheTurnBudget(t *testing.T) {
	fastProgram(t)
	t.Setenv("CAPTAIN_REPEAT_MAX", "3")
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	var runs atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		n := runs.Add(1)
		return leg, captaincode.Result{Text: fmt.Sprintf("step %d", n), DurationMs: 5}, nil
	}
	_, th := typeProgram(t, b, "/repeat /codex keep improving it until: false")
	require.NotNil(t, th)
	assert.Equal(t, int32(3), runs.Load(), "one budget for the whole program")
	assert.Contains(t, th.stopReason, "turn budget of 3 turns")
}

// /repeat finish ends a program after the turn in flight: the next step
// never starts.
func TestProgramFinishStopsAfterTheCurrentTurn(t *testing.T) {
	fastProgram(t)
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	b.planFn = twoWorkerPlan
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	release := make(chan struct{})
	inTeam := make(chan struct{}, 4)
	var codex atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if leg == captaincode.LegCodex {
			codex.Add(1)
		} else {
			inTeam <- struct{}{}
			<-release
		}
		return leg, captaincode.Result{Text: "out", DurationMs: 5}, nil
	}
	text := "/team research it > /codex implement it"
	done := make(chan struct{})
	go func() { b.chatCompletions(httptest.NewRecorder(), repeatReq(text)); close(done) }()
	<-inTeam

	stop := httptest.NewRecorder()
	b.chatCompletions(stop, repeatReq("/repeat finish"))
	assert.Contains(t, stop.Body.String(), "finishing: rp_")
	close(release)
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the program did not end after /repeat finish")
	}
	assert.Zero(t, codex.Load(), "the step after the turn in flight never started")
	b.rmu.Lock()
	defer b.rmu.Unlock()
	for _, th := range b.repeatState() {
		if th.program != nil {
			assert.Equal(t, "stopped by the user", th.stopReason)
		}
	}
}

func TestProgramSyntaxErrorIsRefused(t *testing.T) {
	b := teamBrain()
	var runs atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs.Add(1)
		return leg, captaincode.Result{Text: "x", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("(/codex fix the parser > /team review it"))
	assert.Equal(t, 400, rec.Code)
	assert.Contains(t, rec.Body.String(), "program syntax")
	assert.Contains(t, rec.Body.String(), "never closed")
	assert.Zero(t, runs.Load(), "a program that does not parse runs nothing")
}

// A program's own turns are plain turns. A turn whose text is a program -
// an answer that quotes one, handed to the next step - never starts another.
func TestProgramTurnsNeverStartPrograms(t *testing.T) {
	b := teamBrain()
	text := "/team research it > /codex implement it"
	turn := oaiChatReq{Model: "auto", internal: true, outcome: &turnOutcome{}, ws: defaultWorkspace()}
	assert.False(t, b.handleProgram(context.Background(), httptest.NewRecorder(), turn, text), "a program's turn")
	round := oaiChatReq{Model: "auto", internal: true, ws: defaultWorkspace()}
	assert.False(t, b.handleProgram(context.Background(), httptest.NewRecorder(), round, text), "a /repeat round")
}

// A prompt typed while another turn streams arrives queued, and internal: it
// is still the user's, so it may start a program - without holding the queue.
func TestQueuedProgramStarts(t *testing.T) {
	fastProgram(t)
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	b.planFn = twoWorkerPlan
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	var codex atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if leg == captaincode.LegCodex {
			codex.Add(1)
		}
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 5}, nil
	}
	text := "/team research it > /codex implement it"
	req := oaiChatReq{Model: "auto", internal: true, ws: defaultWorkspace(),
		Messages: []oaiMessage{{Role: "user", Content: jsonString(text)}}}
	ctx := context.WithValue(context.Background(), queuedKey{}, true)
	rec := httptest.NewRecorder()
	require.True(t, b.handleProgram(ctx, rec, req, text), "the queued program starts")
	assert.Contains(t, rec.Body.String(), "started", "the queue moves on: the turn does not watch")
	require.Eventually(t, func() bool { return codex.Load() == 1 }, 30*time.Second, 50*time.Millisecond, "both steps ran")
}

// Only a typed prompt starts a loop: captain send refuses one.
func TestInboxRefusesLoopsAndPrograms(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	post := func(text string) int {
		body, _ := json.Marshal(map[string]string{"text": text})
		rec := httptest.NewRecorder()
		b.inboxHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/inbox?cwd="+dir, bytes.NewReader(body)))
		return rec.Code
	}
	assert.Equal(t, 400, post("/repeat 5 /codex fix it"))
	assert.Equal(t, 400, post("/oss /repeat 5 fix it"))
	assert.Equal(t, 400, post("/team research it > /codex implement it"))
	assert.Equal(t, 400, post("(/codex fix it) || /claude explain"))
	assert.Equal(t, 200, post("/repeat status"), "a control word starts nothing")
	assert.Equal(t, 200, post("the GPU is free - start the benchmark"))
	assert.Equal(t, 200, post("/grok draft it > /claude review it"), "a workflow is one turn")
}

// Regression: round 2 of a /repeat over a workflow was served round 1's
// stored answer, so two rounds ran two workers, not four.
func TestRepeatOverAWorkflowRunsEveryRound(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	var runs atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs.Add(1)
		return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 5}, nil
	}
	var reviews atomic.Int32
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		n := reviews.Add(1)
		return captaincode.MultiAssessment{Synthesis: fmt.Sprintf("review %d", n)}, nil
	}
	b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat 2 /grok draft it > /claude review it"))
	assert.Equal(t, int32(4), runs.Load(), "2 rounds × 2 stages")
}

// Regression: the round contract rode into the gate command, so the gate
// failed in every round and bought a repair.
func TestRepeatGateKeepsItsCommand(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	var runs atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs.Add(1)
		return leg, captaincode.Result{Text: "did it", DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat 1 /codex fix the parser gate: true"))
	assert.Equal(t, int32(1), runs.Load(), "`true` passes: no repair")
}

func TestWfParsePreviewsWithoutRunning(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Fatal("/wf parse must not run a worker")
		return leg, captaincode.Result{}, nil
	}
	b.planFn = func(string, captaincode.Class, string, []captaincode.Leg, map[captaincode.Leg]captaincode.LegStats, map[string]captaincode.TeamStat, bool) (captaincode.Plan, error) {
		t.Fatal("/wf parse must not call the director")
		return captaincode.Plan{}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/wf parse /team research it > (/repeat 2 /codex fix it until: go test ./...)"))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "up to 3 turns")
	assert.Contains(t, body, "1. /team research it")
	assert.Contains(t, body, "repeat 2 times, until `go test ./...` passes:")
	assert.Contains(t, body, "Nothing ran")

	bad := httptest.NewRecorder()
	b.chatCompletions(bad, repeatReq("/wf parse /team research it > /fontier fix it"))
	assert.Contains(t, bad.Body.String(), "unknown command /fontier")
}

// The TUI sends /wf turns as model=auto. The route's named-sequence check
// read the legs named in "/wf parse /team … > /codex … > /claude …" as a
// pipeline and ran it as a workflow (found on a side-port brain, 2026-10-05).
func TestWfControlWordsOnAutoNeverRunAWorkflow(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Errorf("a /wf control word ran a worker (%s)", leg)
		return leg, captaincode.Result{Text: "x", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, autoReq("/wf parse /team research the API > (/repeat 4 /codex implement the next item gate: go test ./...) > /claude review the diff"))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Nothing ran")

	var compiled atomic.Int32
	b.compileFn = func(intent, convo string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats) (captaincode.CompiledWorkflow, error) {
		compiled.Add(1)
		return captaincode.CompiledWorkflow{}, fmt.Errorf("stub: no director in tests")
	}
	compile := httptest.NewRecorder()
	b.chatCompletions(compile, autoReq("/wf grok analyses the queue file, then cursor reviews the analysis"))
	assert.Equal(t, int32(1), compiled.Load(), "an English plan that names legs in order is compiled and previewed, not run")
}
