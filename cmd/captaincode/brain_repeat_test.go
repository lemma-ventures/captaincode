package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRepeat(t *testing.T) {
	n, rest, ok := parseRepeat("/repeat 10 /team audit the deck")
	assert.True(t, ok)
	assert.Equal(t, 10, n)
	assert.Equal(t, "/team audit the deck", rest, "the inner prompt keeps its own directives")

	n, rest, ok = parseRepeat("/repeat check the feed")
	assert.True(t, ok)
	assert.Equal(t, 0, n, "no count = open-ended")
	assert.Equal(t, "check the feed", rest)

	_, rest, ok = parseRepeat("/repeat stop")
	assert.True(t, ok)
	assert.Equal(t, "stop", rest)

	_, _, ok = parseRepeat("please repeat that")
	assert.False(t, ok, "only the leading control word counts")
}

func repeatReq(text string) *http.Request {
	body, _ := json.Marshal(map[string]any{"model": "free", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": text}}})
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
}

// A bounded thread runs exactly N iterations, each a REAL turn (never an
// attach to the previous iteration's cached answer).
func TestRepeatRunsRequestedIterations(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	var runs atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs.Add(1)
		assert.NotContains(t, prompt, "/repeat", "the control word never reaches a worker")
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/repeat 3 tidy the changelog"))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "repeat thread rp_", "the launching turn returns immediately with a thread id")

	require.Eventually(t, func() bool { return runs.Load() == 3 }, 30*time.Second, 100*time.Millisecond,
		"exactly 3 iterations ran (got %d)", runs.Load())
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(3), runs.Load(), "and no more after the target")
}

// The whole point of detaching: /repeat stop must be answerable while a thread
// is running, and must actually end it.
func TestRepeatStopEndsTheThread(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	var runs atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs.Add(1)
		return leg, captaincode.Result{Text: "ok", DurationMs: 5}, nil
	}
	// The launch turn now STREAMS until the loop ends, so an open-ended thread
	// blocks its caller by design - start it off-thread.
	go b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat watch the queue"))
	require.Eventually(t, func() bool { return runs.Load() >= 1 }, 20*time.Second, 50*time.Millisecond)

	stop := httptest.NewRecorder()
	b.chatCompletions(stop, repeatReq("/repeat stop"))
	require.Equal(t, 200, stop.Code)
	assert.Contains(t, stop.Body.String(), "finishing: rp_")

	settled := runs.Load()
	time.Sleep(repeatPause + 500*time.Millisecond)
	assert.LessOrEqual(t, runs.Load(), settled+1, "at most the in-flight iteration completes after stop")

	st := httptest.NewRecorder()
	b.chatCompletions(st, repeatReq("/repeat status"))
	assert.Contains(t, st.Body.String(), "finished")
}

func TestRepeatStatusWithNoThreads(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/repeat status"))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "no repeat threads running")
}

// A consistently failing task must not burn the whole hard cap.
func TestRepeatStopsAfterConsecutiveFailures(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	var runs atomic.Int32
	var mu sync.Mutex
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		runs.Add(1)
		return leg, captaincode.Result{}, assertErrRepeat("worker exploded")
	}
	go b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat 50 do the impossible")) // blocks until it gives up

	require.Eventually(t, func() bool {
		st := httptest.NewRecorder()
		b.chatCompletions(st, repeatReq("/repeat status"))
		return strings.Contains(st.Body.String(), "finished")
	}, 40*time.Second, 250*time.Millisecond, "thread gives up rather than running all 50")
	assert.LessOrEqual(t, runs.Load(), int32(repeatMaxFails), "stopped at the consecutive-failure bound")
}

type assertErrRepeat string

func (e assertErrRepeat) Error() string { return string(e) }

// Live 2026-08-31: "/repeat /team /quality continue…" ran as a SOLO claude
// turn for 24 minutes and completed zero iterations - the "/team" prefix is
// parsed by the fork, and a detached iteration never passes through it.
func TestModelForTask(t *testing.T) {
	assert.Equal(t, "team", modelForTask("/team /quality do the thing", "claude"))
	assert.Equal(t, "team", modelForTask("/quality /team do the thing", "claude"), "preference first still resolves the model")
	assert.Equal(t, "frontier", modelForTask("/frontier think hard", "free"))
	assert.Equal(t, "grok", modelForTask("/grok summarize", "claude"), "forced legs too")
	assert.Equal(t, "claude", modelForTask("just a normal prompt", "claude"), "no directive → unchanged")
	assert.Equal(t, "free", modelForTask("/quality polish this", "free"), "a bare preference does not change the model")
	assert.Equal(t, "free", modelForTask("/notaleg do it", "free"), "unknown prefix is left to the normal path")
}

func TestRepeatDispatchesTeamWhenTaskSaysTeam(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	var planned atomic.Int32
	var gotPrefer string
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		planned.Add(1)
		gotPrefer = prefer
		return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "r",
			Workers: []captaincode.Worker{{Leg: captaincode.LegClaude, Brief: "a"}, {Leg: captaincode.LegCursor, Brief: "b"}}}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "out", DurationMs: 5}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/repeat 1 /team /quality continue the implementation"))
	require.Equal(t, 200, rec.Code)
	require.Eventually(t, func() bool { return planned.Load() == 1 }, 20*time.Second, 100*time.Millisecond,
		"the iteration must run the TEAM path, not a solo turn")
	assert.Equal(t, "quality", gotPrefer, "/quality inside the repeated task still binds the menu")
}

// Supervision: a detached round cannot stream into the turn that launched it,
// so what each round DID must still reach the TUI - announced once on the
// progress channel, and readable in full with /repeat show (2026-08-31).
func TestRepeatRoundsAreVisibleInTheTUI(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	var n atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		i := n.Add(1)
		return leg, captaincode.Result{Text: fmt.Sprintf("round %d did the work", i), DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/repeat 2 tidy the changelog"))
	require.Equal(t, 200, rec.Code)
	require.Eventually(t, func() bool { return n.Load() == 2 }, 30*time.Second, 100*time.Millisecond)

	// Announced once, with what the round produced.
	notice := b.repeatNotice(defaultWorkspace().Dir)
	assert.Contains(t, notice, "round 1")
	assert.Contains(t, notice, "did the work", "the notice says what happened, not just that it happened")
	assert.Empty(t, b.repeatNotice(defaultWorkspace().Dir), "each round is announced exactly once")

	// And readable in full afterwards.
	show := httptest.NewRecorder()
	b.chatCompletions(show, repeatReq("/repeat show"))
	body := show.Body.String()
	assert.Contains(t, body, "round 1")
	assert.Contains(t, body, "round 2")
	assert.Contains(t, body, "did the work")
}

// Live 2026-08-31: `/repeat stop` cancelled the round in flight - a 25-minute
// claude round died mid-work and the thread ended 0/∞ done. Stop must be
// graceful; `/repeat abort` is the hard version.
func TestRepeatStopLetsTheRunningRoundFinish(t *testing.T) {
	testRepeatGracefulEnd(t, "/repeat stop")
}

// /repeat finish is the graceful end under the name of what it does.
func TestRepeatFinishLetsTheRunningRoundFinish(t *testing.T) {
	testRepeatGracefulEnd(t, "/repeat finish")
}

func testRepeatGracefulEnd(t *testing.T, ctl string) {
	t.Helper()
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	release := make(chan struct{})
	var finished atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		<-release // hold round 1 open while we stop the thread
		finished.Add(1)
		return leg, captaincode.Result{Text: "expensive work completed", DurationMs: 10}, nil
	}
	go b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat expensive job")) // open-ended: blocks
	time.Sleep(300 * time.Millisecond)                                               // let round 1 enter the worker

	stop := httptest.NewRecorder()
	b.chatCompletions(stop, repeatReq(ctl))
	assert.Contains(t, stop.Body.String(), "left to finish")

	close(release) // the in-flight round must still complete
	require.Eventually(t, func() bool { return finished.Load() >= 1 }, 15*time.Second, 50*time.Millisecond,
		"the round in flight must not be cancelled by a stop")

	require.Eventually(t, func() bool {
		s := httptest.NewRecorder()
		b.chatCompletions(s, repeatReq("/repeat status"))
		return strings.Contains(s.Body.String(), "finished")
	}, 15*time.Second, 100*time.Millisecond)

	show := httptest.NewRecorder()
	b.chatCompletions(show, repeatReq("/repeat show"))
	assert.Contains(t, show.Body.String(), "expensive work completed", "its work is kept, not thrown away")
	assert.Equal(t, int32(1), finished.Load(), "and no further round started")
}

// Rounds must reach the TUI while the loop runs. Notices only fire when the
// user types, which is useless for 15-minute rounds (live 2026-08-31: five
// rounds done, one visible). /repeat watch keeps one turn open and streams
// each round as it lands.
func TestRepeatWatchStreamsRoundsLive(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	gate := make(chan struct{})
	var n atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		i := n.Add(1)
		if i == 2 {
			<-gate // hold round 2 so the watch must stream it LATER, not up front
		}
		return leg, captaincode.Result{Text: fmt.Sprintf("round %d output", i), DurationMs: 5}, nil
	}
	go b.chatCompletions(httptest.NewRecorder(), repeatReq("/repeat 2 do the job")) // launch streams; run it off-thread
	require.Eventually(t, func() bool { return n.Load() >= 1 }, 15*time.Second, 50*time.Millisecond)

	var emitted []string
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.repeatWatch(context.Background(), func(s string) {
			mu.Lock()
			emitted = append(emitted, s)
			mu.Unlock()
		}, nil, defaultWorkspace().Dir, "")
	}()

	// Round 1 shows up without the watcher having to wait for the loop to end.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(emitted, ""), "round 1 output")
	}, 15*time.Second, 100*time.Millisecond, "round 1 must stream before the thread finishes")

	close(gate) // let round 2 complete
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("watch did not end when the thread finished")
	}
	all := strings.Join(emitted, "")
	assert.Contains(t, all, "round 2 output", "later rounds stream as they land")
	assert.Contains(t, all, "finished", "the watch closes itself when the loop ends")
}

func TestRepeatControlWordsDoNotSwallowTasks(t *testing.T) {
	for _, ctl := range []string{"status", "stop", "finish", "wrapup", "abort", "show rp_abc123", "watch last", "stop all", "finish last"} {
		_, _, ok := repeatControl(ctl)
		assert.True(t, ok, "%q is a command", ctl)
	}
	for _, task := range []string{"watch the queue", "show me the diff", "stop the flaky test",
		"status page needs a rewrite", "abort the migration and explain why", "finish the refactor"} {
		_, _, ok := repeatControl(task)
		assert.False(t, ok, "%q is a TASK, not a command", task)
	}
}

// Rounds return walls of prose; a supervisor needs what the round DID, not
// 3k characters to scroll (2026-08-31).
func TestRoundsCarryASummary(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return "Edited rules.py and added 3 tests." }
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: strings.Repeat("verbose worker prose. ", 200), DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/repeat 1 do the job"))
	require.Equal(t, 200, rec.Code)
	require.Eventually(t, func() bool {
		s := httptest.NewRecorder()
		b.chatCompletions(s, repeatReq("/repeat status"))
		return strings.Contains(s.Body.String(), "finished")
	}, 20*time.Second, 100*time.Millisecond)

	// status digest, notice and show all lead with the summary
	st := httptest.NewRecorder()
	b.chatCompletions(st, repeatReq("/repeat status"))
	assert.Contains(t, st.Body.String(), "Edited rules.py")

	assert.Contains(t, b.repeatNotice(defaultWorkspace().Dir), "Edited rules.py", "the notice says what happened")

	show := httptest.NewRecorder()
	b.chatCompletions(show, repeatReq("/repeat show"))
	body := show.Body.String()
	assert.Contains(t, body, "**did:** Edited rules.py")
	assert.Contains(t, body, "verbose worker prose", "the full text is still there when wanted")
}

// A summarizer failure must never block the loop.
func TestRoundSummaryFallsBackToFirstLine(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text } // no network in tests
	assert.Equal(t, "(no output)", b.roundSummary("t", "   "))
	b.roundSummaryFn = func(text string) string { return firstLine(text, "") }
	assert.Equal(t, "first meaningful line", b.roundSummary("t", "\n\nfirst meaningful line\nrest"))
}

// Launching a loop must SHOW it. Requiring "/repeat watch" as a second command
// meant a user who started a loop watched an empty screen for 15 rounds
// (2026-09-01).
func TestRepeatLaunchStreamsRoundsWithoutASecondCommand(t *testing.T) {
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return "did: " + firstLine(text, "") }
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "changed the config", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/repeat 2 do the job")) // the ONLY command typed
	require.Equal(t, 200, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "repeat thread rp_", "the ack is still there")
	assert.Contains(t, body, "round 1", "and the rounds arrive in the SAME turn")
	assert.Contains(t, body, "round 2")
	assert.Contains(t, body, "did: changed the config", "each round says what it did")
	assert.Contains(t, body, "finished", "the turn closes when the loop ends")
}

// While a round runs, the watcher sees the worker's activity (tool starts,
// heartbeats) on the progress channel - not a silent wait it cannot
// interrupt, since the TUI queues typed input behind a streaming turn
// ("/repeat show ... QUEUED, while I want to see what the worker is doing",
// 2026-09-12).
func TestRepeatWatchShowsTheRunningRoundsActivity(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_LOGS", "0")
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if onStatus != nil {
			onStatus("⚙ shell cargo test -p arc")
		}
		time.Sleep(2500 * time.Millisecond) // the round is "working" while the watcher polls
		return leg, captaincode.Result{Text: "round done", DurationMs: 2500}, nil
	}
	var mu sync.Mutex
	var statuses []string
	rec := &captureWriter{onStatus: func(s string) {
		mu.Lock()
		statuses = append(statuses, s)
		mu.Unlock()
	}}
	body, _ := json.Marshal(oaiChatReq{Model: "free", Stream: true,
		Messages: []oaiMessage{{Role: "user", Content: jsonString("/repeat 1 tidy the changelog")}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	b.chatCompletions(rec, req) // the launching turn watches until the single round finishes
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(statuses, "\n")
	assert.Contains(t, joined, "round 1 · ⚙ shell cargo test -p arc", "the running round's tool activity reaches the watcher")
	text, errText := rec.answer()
	assert.Empty(t, errText)
	assert.Contains(t, text, "round 1")
}

// A round that defers ("no files were touched … await the runner … scheduled
// wakeup", 2026-09-12) is a wasted hour: a headless round cannot come back
// later. It is marked, and the next round's task says so and demands the step.
func TestRepeatDeferredRoundIsNamedAndTheNextRoundIsTold(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_LOGS", "0")
	assert.True(t, roundDeferred("No files were touched and no code changes were made. The round deferred implementation to await the runner's fixed-point phase and scheduled wakeup."))
	assert.False(t, roundDeferred("Implemented the NTT product; 40 tests pass. Files: decider.rs, merkle.rs"))

	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	var prompts []string
	var mu sync.Mutex
	var n atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()
		if n.Add(1) == 1 {
			return leg, captaincode.Result{Text: "No files were touched. Deferred to await the runner's fixed-point phase and scheduled wakeup.", DurationMs: 5}, nil
		}
		return leg, captaincode.Result{Text: "Wrote the reduction algebra in-circuit; 12 tests pass.", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, repeatReq("/repeat 2 address bounded recursion"))
	require.Equal(t, 200, rec.Code)
	require.Eventually(t, func() bool { return n.Load() == 2 }, 30*time.Second, 100*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[0], "fresh, headless run", "every round carries the round contract")
	assert.Contains(t, prompts[1], "Round 1 of this task returned no work", "the next round is told what was deferred")
	assert.Contains(t, prompts[1], "Do the step it deferred, now.")
	assert.NotContains(t, prompts[0], "returned no work")
	assert.Contains(t, b.repeatStatus(defaultWorkspace().Dir), "○", "a deferred round is marked, not counted as work")
}

// A control word typed while a watch streams sits QUEUED behind it; sent
// out of band by the plugin, its answer is printed by the watching turn
// (2026-09-13: "/repeat show is still queued... cannot show then").
func TestRepeatControlOutOfBandReachesTheWatcher(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	b.repeatState()["rp_live"] = &repeatThread{id: "rp_live", dir: dir, task: "poll the feed", target: 3, started: time.Now(), cancel: cancel,
		rounds: []roundRecord{{n: 1, at: time.Now(), dur: time.Second, summary: "checked the feed"}}, done: 1}

	var out strings.Builder
	var mu sync.Mutex
	emit := func(s string) { mu.Lock(); out.WriteString(s); mu.Unlock() }
	watching := make(chan struct{})
	go func() { b.repeatWatch(ctx, emit, nil, dir, "rp_live"); close(watching) }()
	time.Sleep(50 * time.Millisecond)

	body, _ := json.Marshal(map[string]string{"word": "show"})
	rec := httptest.NewRecorder()
	b.repeatCtlHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/repeat/ctl?cwd="+url.QueryEscape(dir), bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"watched":true`, "the caller learns a watcher will print it")
	assert.Contains(t, rec.Body.String(), "checked the feed", "…and gets the answer itself (the toast)")

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return strings.Contains(out.String(), "checked the feed") }, 3*time.Second, 50*time.Millisecond,
		"the watching turn prints the answer in place")
	cancel()
	<-watching

	rec = httptest.NewRecorder()
	b.repeatCtlHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/repeat/ctl?cwd="+url.QueryEscape(dir), bytes.NewReader([]byte(`{"word":"dance"}`))))
	assert.Equal(t, 400, rec.Code, "only the control words")
}

// A control word is never a task: "/repeat 3 /frontier <task>" sent as
// model=auto must start the loop, not be routed whole - the director once
// planned a team for it and the turn ran as that team, once, with no loop
// (live 2026-09-15). The director is not even consulted.
func TestRepeatOnAutoIsNotRoutedAsATask(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	b.roundSummaryFn = func(text string) string { return text }
	directorCalls := 0
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		directorCalls++
		return captaincode.Plan{Class: captaincode.ClassHigh, Workers: []captaincode.Worker{{Leg: captaincode.LegGLM, Brief: task}, {Leg: captaincode.LegGrok, Brief: task}}}, nil
	}
	var frontierRuns atomic.Int32
	b.frontierFn = func(prompt string, onDelta, onStatus func(string)) (captaincode.Result, error) {
		frontierRuns.Add(1)
		return captaincode.Result{Text: "fixed", DurationMs: 5}, nil
	}
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Errorf("a plain worker ran (%s) - the round should be the forced frontier", leg)
		return leg, captaincode.Result{Text: "x"}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "auto", "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "/repeat 2 /frontier harden the balance equations"}}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "repeat thread rp_", "the loop started: %s", rec.Body.String())
	assert.Equal(t, 0, directorCalls, "a control word never reaches the director")
	require.Eventually(t, func() bool { return frontierRuns.Load() == 2 }, 30*time.Second, 100*time.Millisecond, "two frontier rounds (got %d)", frontierRuns.Load())
}
