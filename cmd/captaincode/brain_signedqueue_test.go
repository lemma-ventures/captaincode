package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func queueSigned(t *testing.T) (*brain, pres.Signed, string) {
	t.Helper()
	b, p, dir := signedInboxFixture(t)
	w := httptest.NewRecorder()
	b.signedInbox(w, p)
	require.Equal(t, 200, w.Code, w.Body.String())
	return b, p, dir
}

// A verified signed prompt is written to the queue and finished when its
// TUI takes it.
func TestSignedQueueKeepsAPromptUntilItsTUITakesIt(t *testing.T) {
	b, p, dir := queueSigned(t)
	pending := sqList("pending")
	require.Len(t, pending, 1)
	assert.Equal(t, p.Text, pending[0].Text)
	assert.Equal(t, dir, pending[0].Dir)
	items := b.inbox.take(dir, "")
	require.Len(t, items, 1)
	assert.Empty(t, sqList("pending"))
	done := sqList("done")
	require.Len(t, done, 1)
	assert.Equal(t, "tui", done[0].Done)
}

// With no TUI open for its folder, the brain runs the prompt itself, as a
// typed turn that finds the signed approval.
func TestSignedQueueRunsAPromptNoTUITook(t *testing.T) {
	b, p, dir := queueSigned(t)
	old := sqHeadlessAfter
	sqHeadlessAfter = 0
	defer func() { sqHeadlessAfter = old }()
	ran := make(chan string, 1)
	b.signedQueueRunFn = func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, dir, r.Header.Get(workspaceHeader))
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(raw, &body))
		_, sent, grant := b.inbox.notePresenceTurn(dir, body.Messages[len(body.Messages)-1].Content)
		assert.False(t, sent, "a typed turn, not a sent one")
		assert.NotNil(t, grant, "the signed approval goes with it")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done it"}}]}`))
		ran <- body.Messages[len(body.Messages)-1].Content
	}
	b.signedQueueStep()
	select {
	case text := <-ran:
		assert.Equal(t, p.Text, text)
	case <-time.After(5 * time.Second):
		t.Fatal("the queued prompt did not run")
	}
	require.Eventually(t, func() bool { return len(sqList("done")) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "headless", sqList("done")[0].Done)
	assert.Empty(t, b.inbox.take(dir, ""), "the TUI does not get it a second time")
}

// A TUI that polled recently gets the prompt; the brain does not run it.
func TestSignedQueueLeavesThePromptToAnOpenTUI(t *testing.T) {
	b, _, dir := queueSigned(t)
	old := sqHeadlessAfter
	sqHeadlessAfter = 0
	defer func() { sqHeadlessAfter = old }()
	b.inbox.mu.Lock()
	b.inbox.polled = map[string]time.Time{dir: time.Now()}
	b.inbox.mu.Unlock()
	b.signedQueueRunFn = func(http.ResponseWriter, *http.Request) { t.Error("an open TUI takes it") }
	b.signedQueueStep()
	assert.Len(t, sqList("pending"), 1)
}

// A restarted brain puts pending signed prompts back; a dropped one is not
// handed out.
func TestSignedQueueSurvivesARestartAndADrop(t *testing.T) {
	_, p, dir := queueSigned(t)
	b2 := teamBrain()
	b2.loadSignedQueue()
	items := b2.inbox.take(dir, "")
	require.Len(t, items, 1, "restored after the restart")
	assert.Equal(t, p.Text, items[0].Text)

	b3, _, dir3 := queueSigned(t)
	id := sqList("pending")[0].ID
	sqFinish(id, "dropped", "test")
	assert.Empty(t, b3.inbox.take(dir3, ""), "a dropped prompt is not handed out")
}

// A prompt the brain runs itself had no lane: "/codex-cli /frontier what do
// you need" went out as model auto and ran on the quality lane's leg
// (2026-10-24). The leading directive picks the model, as a TUI would.
func TestSignedQueueHonoursTheLeadingLane(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := &brain{}
	var model string
	b.signedQueueRunFn = func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		model = body.Model
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}
	dir := t.TempDir()
	for text, want := range map[string]string{
		"/codex-cli /frontier what do you need": "codex-cli",
		"/frontier plan the next step":          "frontier",
		"/quality /team review it":              "team",
		"what do you need":                      "auto",
	} {
		b.runSignedQueued(sqEntry{ID: "in_1", Dir: dir, Text: text}, inboxItem{Text: text})
		assert.Equal(t, want, model, text)
	}
}

// A folder with no TUI keeps its conversation: the next prompt the brain
// runs there starts with the earlier prompts and answers, as a TUI session
// would; another folder's thread is not mixed in.
func TestSignedQueueKeepsAThreadPerFolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := &brain{}
	var got []struct{ Role, Content string }
	n := 0
	b.signedQueueRunFn = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Role, Content string }
		}
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		got = body.Messages
		n++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer ` + string(rune('0'+n)) + `"}}]}`))
	}
	ws3, other := t.TempDir(), t.TempDir()
	b.runSignedQueued(sqEntry{ID: "in_1", Dir: ws3, Text: "what do you need"}, inboxItem{Text: "what do you need"})
	require.Len(t, got, 1)
	b.runSignedQueued(sqEntry{ID: "in_2", Dir: other, Text: "elsewhere"}, inboxItem{Text: "elsewhere"})
	require.Len(t, got, 1, "another folder starts its own thread")
	b.runSignedQueued(sqEntry{ID: "in_3", Dir: ws3, Text: "start with E1-01"}, inboxItem{Text: "start with E1-01"})
	require.Len(t, got, 3)
	assert.Equal(t, "user", got[0].Role)
	assert.Equal(t, "what do you need", got[0].Content)
	assert.Equal(t, "assistant", got[1].Role)
	assert.Equal(t, "answer 1", got[1].Content)
	assert.Equal(t, "start with E1-01", got[2].Content, "the new prompt comes last")
}

// Two signed prompts for one folder with no TUI ran side by side in the same
// checkout: a merge of every branch while another worker had uncommitted
// edits there (2026-10-25). A folder's next prompt now waits for the one
// running there; a control line (/repeat stop, /btw, /interrupt) does not.
func TestSignedQueueRunsOneAtATimePerFolder(t *testing.T) {
	b, _, dir := queueSigned(t)
	old := sqHeadlessAfter
	sqHeadlessAfter = 0
	defer func() { sqHeadlessAfter = old }()
	require.NoError(t, sqWrite("pending", sqEntry{ID: "in_second", Dir: dir, Text: "merge all branches", At: time.Now()}))
	require.NoError(t, sqWrite("pending", sqEntry{ID: "in_stop", Dir: dir, Text: "/repeat stop", At: time.Now()}))
	b.inbox.mu.Lock()
	signed := b.inbox.items[dir][0].Presence
	for _, id := range []string{"in_second", "in_stop"} {
		text := map[string]string{"in_second": "merge all branches", "in_stop": "/repeat stop"}[id]
		b.inbox.items[dir] = append(b.inbox.items[dir], inboxItem{ID: id, Text: text, Presence: signed})
	}
	b.inbox.mu.Unlock()
	release := make(chan struct{})
	started := make(chan string, 4)
	b.signedQueueRunFn = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		text := body.Messages[len(body.Messages)-1].Content
		started <- text
		if text != "/repeat stop" {
			<-release
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	next := func() string {
		select {
		case text := <-started:
			return text
		case <-time.After(2 * time.Second):
			return ""
		}
	}
	b.signedQueueStep()
	got := map[string]bool{next(): true, next(): true}
	assert.True(t, got["implement the change"], "the first prompt runs: %v", got)
	assert.True(t, got["/repeat stop"], "a control line is not held behind the running prompt: %v", got)
	assert.False(t, got["merge all branches"], "the second prompt waits while the first runs: %v", got)
	b.signedQueueStep()
	select {
	case text := <-started:
		t.Fatalf("%q started while the folder's prompt was running", text)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	require.Eventually(t, func() bool { b.signedQueueStep(); return len(sqList("pending")) == 0 }, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, "merge all branches", next(), "the next prompt runs once the first is done")
}
