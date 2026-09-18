package main

import (
	"context"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A retried solo turn must attach, not start a second run: a rerouted 8-minute
// job answered a request nobody was listening to any more (live 2026-07-30).
func TestRetriedSoloTurnAttaches(t *testing.T) {
	b := teamBrain()
	var mu sync.Mutex
	runs := 0
	gate := make(chan struct{})
	first := make(chan struct{})
	var once sync.Once
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		once.Do(func() { close(first) })
		<-gate
		return leg, captaincode.Result{Text: "THE ANSWER", DurationMs: 5}, nil
	}

	rec1, rec2 := httptest.NewRecorder(), httptest.NewRecorder()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); b.chatCompletions(rec1, wfReq(false, "condense this to 200 words")) }()
	<-first
	wg.Add(1)
	go func() { defer wg.Done(); b.chatCompletions(rec2, wfReq(false, "condense this to 200 words")) }()
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, runs, "the retry attached instead of re-running")
	assert.Contains(t, answerOf(t, rec1), "THE ANSWER")
	assert.Contains(t, answerOf(t, rec2), "THE ANSWER")
}

func TestSoloRepeatWithinTTLIsServedFromTheResult(t *testing.T) {
	b := teamBrain()
	runs := 0
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs++
		return leg, captaincode.Result{Text: "CACHED", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "same prompt"))
	require.Equal(t, 1, runs)
	rec2 := httptest.NewRecorder()
	b.chatCompletions(rec2, wfReq(false, "same prompt"))
	assert.Equal(t, 1, runs, "no second run within the window")
	assert.Contains(t, answerOf(t, rec2), "CACHED")
}

// A different prompt is different work.
func TestDifferentPromptRunsAgain(t *testing.T) {
	b := teamBrain()
	runs := 0
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		runs++
		return leg, captaincode.Result{Text: "x", DurationMs: 5}, nil
	}
	b.chatCompletions(httptest.NewRecorder(), wfReq(false, "first prompt"))
	b.chatCompletions(httptest.NewRecorder(), wfReq(false, "second prompt"))
	assert.Equal(t, 2, runs)
}

// A resend after a FAILURE is "try again", never a replay of the failure:
// the user fixed the Cursor login and got the cached "Authentication
// required" four times in three minutes (2026-09-13). In-flight runs still
// attach; a finished success is still served within the TTL.
func TestResendAfterAFailureRunsAgain(t *testing.T) {
	b := teamBrain()
	var runs atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		if runs.Add(1) == 1 {
			return leg, captaincode.Result{}, assertErrRepeat("authentication required") // not reroutable: the turn fails
		}
		return leg, captaincode.Result{Text: "second try answers"}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "will fail once"))
	require.Equal(t, 502, rec.Code, rec.Body.String())

	rec2 := httptest.NewRecorder()
	b.chatCompletions(rec2, wfReq(false, "will fail once"))
	assert.Equal(t, 200, rec2.Code, "the resend runs again instead of replaying the failure")
	assert.Contains(t, rec2.Body.String(), "second try answers")
	assert.Equal(t, int32(2), runs.Load())
}

// "I never got the answer from codex" (live 2026-08-01): a 14-minute
// stall→reroute chain outlived the TUI request; codex's answer went to a dead
// connection, and the brain restart wiped the in-memory dedupe. The history
// record is the durable copy - an identical resend must serve it.
func TestAbandonedTurnIsMarkedInHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := teamBrain()
	ctx, cancel := context.WithCancel(context.Background())
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		cancel() // the client goes away while the worker runs
		return leg, captaincode.Result{Text: "the answer nobody received", DurationMs: 10}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "address the improvements").WithContext(ctx))

	got, ok := findRun("last")
	require.True(t, ok)
	assert.True(t, got.Abandoned, "a client-gone turn is marked so a resend can recover it")
	assert.Equal(t, "the answer nobody received", got.Output)
}

func TestResendRecoversTheAbandonedAnswerAcrossRestarts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	recordRunHistory(runRecord{Kind: "solo", Legs: []string{"codex"}, Task: "address the improvements",
		Output: "RECOVERED ANSWER", DurationMs: 355000, Abandoned: true})

	b := teamBrain() // fresh brain: the in-memory dedupe map is empty (restart)
	ran := false
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = true
		return leg, captaincode.Result{Text: "fresh run", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "address the improvements"))
	require.Equal(t, 200, rec.Code)
	assert.False(t, ran, "the preserved answer is served, not re-run")
	out := answerOf(t, rec)
	assert.Contains(t, out, "RECOVERED ANSWER")
	assert.Contains(t, out, "recovered", "the marker says where this came from")
}

func TestDeliveredAnswersAreNeverReplayedFromHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	recordRunHistory(runRecord{Kind: "solo", Legs: []string{"grok"}, Task: "same task again",
		Output: "OLD DELIVERED ANSWER"}) // Abandoned=false: the user saw it
	b := teamBrain()
	ran := false
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = true
		return leg, captaincode.Result{Text: "fresh", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "same task again"))
	assert.True(t, ran, "asking again after a delivered answer means: run it again")
}

func TestStaleAbandonedAnswersExpire(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	recordRunHistory(runRecord{Kind: "solo", Task: "old thing", Output: "ANCIENT",
		Abandoned: true, At: time.Now().Add(-time.Hour)})
	b := teamBrain()
	ran := false
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = true
		return leg, captaincode.Result{Text: "fresh", DurationMs: 5}, nil
	}
	b.chatCompletions(httptest.NewRecorder(), wfReq(false, "old thing"))
	assert.True(t, ran, "an hour-old orphan is history, not an answer to today's prompt")
}
