package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRecord = "Record the user's decision dq-1: pick the cache\n\nQuestion: Which cache?\nThe user chose option a: keep it"

// (a) A decision record queued in the TUI behind other prompts runs before
// them; the prompts keep their own order.
func TestTypedQueueRunsDecisionRecordsFirst(t *testing.T) {
	for _, mode := range []string{"director", "off"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "off" {
				t.Setenv("CAPTAIN_QUEUE_ORDER", "off")
			}
			b := teamBrain()
			b.orderQueueFn = func(items []captaincode.QueueItem) (captaincode.QueueOrder, error) {
				return captaincode.TypedQueueOrder(items), nil // the director keeps the typed order
			}
			var mu sync.Mutex
			var runs []string
			b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
				mu.Lock()
				runs = append(runs, lastUserTurn(prompt))
				mu.Unlock()
				return leg, captaincode.Result{Text: "done", DurationMs: 5}, nil
			}
			msgs := []map[string]string{
				{"role": "user", "content": "/cursor first task"},
				{"role": "assistant", "content": "ok"},
				{"role": "user", "content": "/cursor second task"},
				{"role": "user", "content": testRecord},
				{"role": "user", "content": "/cursor third task"},
				{"role": "user", "content": "Record the user's 2 decisions for repo-a: two more"},
			}
			body, _ := json.Marshal(map[string]any{"model": "cursor", "stream": true, "messages": msgs})
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
			require.Equal(t, 200, rec.Code, rec.Body.String())
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, runs, 4, "%v", runs)
			assert.True(t, strings.HasPrefix(runs[0], "Record the user's decision dq-1"), "the first record runs first: %v", runs)
			assert.True(t, strings.HasPrefix(runs[1], "Record the user's 2 decisions"), "then the second, in typed order: %v", runs)
			assert.Contains(t, runs[2], "second task")
			assert.Contains(t, runs[3], "third task")
		})
	}
}

// (a) The inbox hands a folder's decision records to its TUI before the
// folder's other prompts.
func TestInboxHandsDecisionRecordsFirst(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	b.inbox.push(dir, "run the benchmark", "", "", "", "")
	b.inbox.push(dir, testRecord, "", "", "", "")
	b.inbox.push(dir, "write the summary", "", "", "", "")
	items := b.inbox.take(dir, "")
	require.Len(t, items, 3)
	assert.Equal(t, testRecord, items[0].Text)
	assert.Equal(t, "run the benchmark", items[1].Text)
	assert.Equal(t, "write the summary", items[2].Text)
}

// sqStub records the prompts the signed queue runs headless and holds each
// until released.
type sqStub struct {
	started chan string
	release map[string]chan struct{}
	mu      sync.Mutex
}

func newSQStub(b *brain, hold ...string) *sqStub {
	s := &sqStub{started: make(chan string, 8), release: map[string]chan struct{}{}}
	for _, h := range hold {
		s.release[h] = make(chan struct{})
	}
	b.signedQueueRunFn = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		text := body.Messages[len(body.Messages)-1].Content
		s.started <- text
		s.mu.Lock()
		ch := s.release[text]
		s.mu.Unlock()
		if ch != nil {
			<-ch
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}
	return s
}

func (s *sqStub) next(d time.Duration) string {
	select {
	case text := <-s.started:
		return text
	case <-time.After(d):
		return ""
	}
}

func (s *sqStub) free(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch := s.release[text]; ch != nil {
		close(ch)
		delete(s.release, text)
	}
}

// addSigned queues a signed prompt for dir in the brain's inbox and the
// pending queue, as brain_presence.go does.
func addSigned(t *testing.T, b *brain, g *pres.Grant, dir, id, text string, at time.Time) {
	t.Helper()
	require.NoError(t, sqWrite("pending", sqEntry{ID: id, Dir: dir, Text: text, At: at}))
	b.inbox.mu.Lock()
	defer b.inbox.mu.Unlock()
	if b.inbox.items == nil {
		b.inbox.items = map[string][]inboxItem{}
	}
	b.inbox.items[dir] = append(b.inbox.items[dir], inboxItem{ID: id, Text: text, At: at, Dir: dir, Presence: g})
}

// grantOf is the approval queueSigned's prompt carries.
func grantOf(b *brain, dir string) *pres.Grant {
	b.inbox.mu.Lock()
	defer b.inbox.mu.Unlock()
	return b.inbox.items[dir][0].Presence
}

func recGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
}

// (a) With no TUI open, a folder's signed decision record runs before an
// older signed prompt of the same folder.
func TestSignedQueueRunsAFoldersRecordFirst(t *testing.T) {
	b, _, dir := queueSigned(t)
	old := sqHeadlessAfter
	sqHeadlessAfter = 0
	defer func() { sqHeadlessAfter = old }()
	addSigned(t, b, grantOf(b, dir), dir, "in_record", testRecord, time.Now())
	stub := newSQStub(b, testRecord, "implement the change")
	defer stub.free(testRecord)
	defer stub.free("implement the change")

	b.signedQueueStep()
	assert.Equal(t, testRecord, stub.next(2*time.Second), "the record runs first")
	b.signedQueueStep()
	assert.Equal(t, "", stub.next(300*time.Millisecond), "the older prompt waits for the record")
	stub.free(testRecord)
	require.Eventually(t, func() bool { b.signedQueueStep(); return len(stub.started) > 0 }, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, "implement the change", stub.next(time.Second))
}

// (a) A signed prompt waits while a decision record for its repository is
// still waiting out sqHeadlessAfter: a fresh record goes first.
func TestSignedQueueHoldsAPromptBehindAFreshRecord(t *testing.T) {
	b, _, dir := queueSigned(t)
	old := sqHeadlessAfter
	sqHeadlessAfter = time.Hour
	defer func() { sqHeadlessAfter = old }()
	e := sqList("pending")[0]
	e.At = time.Now().Add(-2 * time.Hour) // the prompt is due
	require.NoError(t, sqWrite("pending", e))
	addSigned(t, b, grantOf(b, dir), dir, "in_record", testRecord, time.Now()) // the record is not
	stub := newSQStub(b)
	b.signedQueueStep()
	assert.Equal(t, "", stub.next(300*time.Millisecond), "the prompt waits for the record ahead of it")
}

// repeatRounds stubs repeat rounds: each one notes its start, then runs
// onRound (if set) and answers with distinct text.
func repeatRounds(b *brain, events *[]string, mu *sync.Mutex, onRound func(n int)) {
	n := 0
	b.roundSummaryFn = func(text string) string { return text }
	b.chatFn = func(w *captureWriter) {
		mu.Lock()
		n++
		k := n
		*events = append(*events, fmt.Sprintf("round %d", k))
		mu.Unlock()
		if onRound != nil {
			onRound(k)
		}
		fmt.Fprint(w, roundWork[(k-1)%len(roundWork)])
	}
}

var roundWork = []string{
	"Fixed the failing auth test: the token clock was compared before refresh, so it expired mid-request.",
	"Migrated the storage callers to the new interface and deleted the compatibility shim.",
	"Found the flaky watcher: fsevents coalesces two writes, and the test asserted on the first.",
}

// (b) A round does not start while a decision record for its folder is
// pending: it waits, says so, and starts once the record has run.
func TestRepeatWaitsForAPendingRecordBeforeTheNextRound(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	defer shortRecordWait(t, time.Minute)()
	var mu sync.Mutex
	var events []string
	repeatRounds(b, &events, &mu, func(n int) {
		if n == 1 { // the user answers while round 1 runs
			require.NoError(t, sqWrite("pending", sqEntry{ID: "in_rec", Dir: dir, Text: testRecord, At: time.Now()}))
		}
	})
	th := &repeatThread{id: "rp_rec", dir: dir, task: "work the backlog", target: 2, started: time.Now()}
	b.rmu.Lock()
	b.repeatState()[th.id] = th
	b.rmu.Unlock()
	done := make(chan struct{})
	go func() {
		b.runRepeat(context.Background(), th, oaiChatReq{Model: "free", ws: captaincode.Workspace{Dir: dir}})
		close(done)
	}()

	require.Eventually(t, func() bool {
		b.rmu.Lock()
		defer b.rmu.Unlock()
		return th.awaitRecord != ""
	}, 5*time.Second, 10*time.Millisecond, "the loop waits for the record")
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	assert.Equal(t, []string{"round 1"}, events, "round 2 has not started")
	mu.Unlock()
	b.rmu.Lock()
	assert.Contains(t, strings.Join(th.live, "\n"), "waiting for the decision record")
	b.rmu.Unlock()
	assert.Contains(t, b.repeatStatus(dir), "waiting for the decision record")

	sqFinish("in_rec", "headless", "ok")
	<-done
	assert.Equal(t, []string{"round 1", "round 2"}, events)
}

// (b) The wait is bounded: a record that never runs delays a loop once, by
// repeatRecordWait, and is not waited for again.
func TestRepeatRecordWaitIsBounded(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	defer shortRecordWait(t, 300*time.Millisecond)()
	require.NoError(t, sqWrite("pending", sqEntry{ID: "in_stuck", Dir: dir, Text: testRecord, At: time.Now()}))
	defer sqFinish("in_stuck", "dropped", "test")
	var mu sync.Mutex
	var events []string
	repeatRounds(b, &events, &mu, nil)
	th := &repeatThread{id: "rp_stuck", dir: dir, task: "work the backlog", target: 2}
	start := time.Now()
	b.runRepeat(context.Background(), th, oaiChatReq{Model: "free", ws: captaincode.Workspace{Dir: dir}})
	assert.Equal(t, []string{"round 1", "round 2"}, events)
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond, "it waited")
	b.amu.Lock()
	waits, gaveUp := 0, 0
	for _, a := range b.acts {
		if strings.Contains(a.Text, "waiting for the decision record") {
			waits++
		}
		if strings.Contains(a.Text, "did not finish within") {
			gaveUp++
		}
	}
	b.amu.Unlock()
	assert.Equal(t, 1, waits, "once: round 2 does not wait for the same record again")
	assert.Equal(t, 1, gaveUp, "and the feed says it went on without it")
}

// (b) A loop stopped while it waits for a record ends without a new round.
func TestRepeatRecordWaitEndsOnStop(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	defer shortRecordWait(t, time.Minute)()
	require.NoError(t, sqWrite("pending", sqEntry{ID: "in_wait", Dir: dir, Text: testRecord, At: time.Now()}))
	defer sqFinish("in_wait", "dropped", "test")
	var mu sync.Mutex
	var events []string
	repeatRounds(b, &events, &mu, nil)
	th := &repeatThread{id: "rp_stop", dir: dir, task: "work the backlog", target: 3}
	done := make(chan struct{})
	go func() {
		b.runRepeat(context.Background(), th, oaiChatReq{Model: "free", ws: captaincode.Workspace{Dir: dir}})
		close(done)
	}()
	require.Eventually(t, func() bool { b.rmu.Lock(); defer b.rmu.Unlock(); return th.awaitRecord != "" }, 5*time.Second, 10*time.Millisecond)
	b.rmu.Lock()
	th.stopped = true
	b.rmu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stopped loop kept waiting")
	}
	assert.Empty(t, events, "no round started")
}

// (b) A loop running in a linked worktree waits for a record signed for the
// main checkout: decisions map a worktree to its repository.
func TestRepeatInAWorktreeWaitsForTheRepositorysRecord(t *testing.T) {
	repo := t.TempDir()
	recGit(t, repo, "init", "--quiet")
	recGit(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init", "--quiet")
	wt := repo + "/.worktrees/loop"
	recGit(t, repo, "worktree", "add", "--quiet", "-b", "loop", wt)
	b := teamBrain()
	b.inbox.push(repo, testRecord, "", "", "", "")
	why, _ := b.recordPending(wt, nil)
	assert.NotEmpty(t, why, "the worktree's loop sees its repository's record")
	why, _ = b.recordPending(t.TempDir(), nil)
	assert.Empty(t, why, "another folder does not")
}

// (c) A signed record that waits for its turn while the folder's loop is
// between rounds runs before the next round: the loop waits for it, and the
// queue no longer holds it behind the unfinished loop.
func TestSignedRecordRunsBeforeTheLoopsNextRound(t *testing.T) {
	b, _, dir := queueSigned(t)
	g := grantOf(b, dir)
	sqFinish(sqList("pending")[0].ID, "dropped", "not this test's prompt")
	b.inbox.take(dir, "")
	b.inbox.mu.Lock()
	b.inbox.polled = nil // no TUI open
	b.inbox.mu.Unlock()
	old := sqHeadlessAfter
	sqHeadlessAfter = 0
	defer func() { sqHeadlessAfter = old }()
	defer shortRecordWait(t, time.Minute)()

	var mu sync.Mutex
	var events []string
	stub := newSQStub(b, testRecord) // the record is held until the test lets it end
	defer stub.free(testRecord)
	repeatRounds(b, &events, &mu, func(n int) {
		if n == 1 {
			addSigned(t, b, g, dir, "in_rec", testRecord, time.Now())
			addSigned(t, b, g, dir, "in_other", "merge all branches", time.Now())
		}
	})
	req := oaiChatReq{ws: captaincode.Workspace{Dir: dir}}
	th := b.startRepeat(req, "work the backlog", 2, nil)
	stop := make(chan struct{})
	go func() { // the queue's ticker
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				b.signedQueueStep()
			}
		}
	}()
	defer close(stop)

	assert.Equal(t, testRecord, stub.next(10*time.Second), "the record runs while the loop waits")
	assert.Equal(t, "", stub.next(300*time.Millisecond), "the ordinary signed prompt does not run beside the record")
	mu.Lock()
	assert.Equal(t, []string{"round 1"}, events, "round 2 waits while the record runs")
	events = append(events, "record")
	mu.Unlock()
	stub.free(testRecord)
	require.Eventually(t, func() bool { b.rmu.Lock(); defer b.rmu.Unlock(); return th.finished }, 15*time.Second, 20*time.Millisecond)
	mu.Lock()
	assert.Equal(t, []string{"round 1", "record", "round 2"}, events)
	mu.Unlock()
	assert.Equal(t, "merge all branches", stub.next(5*time.Second), "an ordinary signed prompt still waits for the loop to finish")
}

// (c) A record handed to the TUI that is watching the loop is queued behind
// the watch; the watch ends so the record can run, and the loop then goes on.
func TestRepeatWatchMakesWayForAHandedRecord(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	defer shortRecordWait(t, time.Minute)()
	oldGrace := recordHandedGrace
	recordHandedGrace = 50 * time.Millisecond
	defer func() { recordHandedGrace = oldGrace }()
	th := &repeatThread{id: "rp_watch", dir: dir, task: "work the backlog", started: time.Now(), awaitRecord: "a decision record"}
	b.rmu.Lock()
	b.repeatState()[th.id] = th
	b.rmu.Unlock()
	b.inbox.push(dir, testRecord, "", "", "", "")
	require.Len(t, b.inbox.take(dir, "sess"), 1) // handed; it sits in opencode's queue behind the watch

	var out strings.Builder
	done := make(chan struct{})
	go func() {
		b.repeatWatch(context.Background(), func(s string) { out.WriteString(s) }, func(string) {}, dir, th.id)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch held the record behind it")
	}
	assert.Contains(t, out.String(), "decision record")
	assert.Contains(t, out.String(), "/repeat watch "+th.id)
}

func shortRecordWait(t *testing.T, d time.Duration) func() {
	t.Helper()
	oldWait, oldPoll := repeatRecordWait, repeatRecordPoll
	repeatRecordWait, repeatRecordPoll = d, 10*time.Millisecond
	return func() { repeatRecordWait, repeatRecordPoll = oldWait, oldPoll }
}

// (b) A record turn counts from the moment it reaches the brain until it
// ends: a TUI's record holds the loop's next round while it runs.
func TestARecordTurnInFlightHoldsTheLoop(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	end := b.beginRecordTurn(dir)
	why, _ := b.recordPending(dir, nil)
	assert.Equal(t, "running", why)
	end()
	why, _ = b.recordPending(dir, nil)
	assert.Empty(t, why)
}

// A loop in a linked worktree gets the repository's answered decisions in its
// round prompt: answers store the main checkout, not the worktree's root.
func TestRepeatDecisionContextInAWorktreeReadsTheRepositorysAnswers(t *testing.T) {
	t.Setenv(captaincode.DecisionsDirEnv, filepath.Join(t.TempDir(), "decisions"))
	repo := t.TempDir()
	recGit(t, repo, "init", "--quiet")
	recGit(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init", "--quiet")
	wt := repo + "/.worktrees/loop"
	recGit(t, repo, "worktree", "add", "--quiet", "-b", "loop", wt)
	main, _, _ := captaincode.DecisionPlace(repo)
	d, err := captaincode.PostDecision(captaincode.OwnerDecision{Repo: main, Title: "Pick", Question: "Which way?",
		Options: []captaincode.DecisionOption{{ID: "a", Label: "Left"}, {ID: "b", Label: "Right"}}})
	require.NoError(t, err)
	_, err = captaincode.AnswerDecision(d.ID, "b", "", "", "test")
	require.NoError(t, err)
	assert.Contains(t, repeatDecisionContext(wt), "Which way?", "the worktree's loop reads the repository's answer")
	assert.Contains(t, repeatDecisionContext(repo), "Which way?")
	assert.Empty(t, repeatDecisionContext(t.TempDir()), "another folder does not")
}
