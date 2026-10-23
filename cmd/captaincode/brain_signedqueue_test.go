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
		_, sent, grant := b.inbox.notePresenceTurn(dir, body.Messages[0].Content)
		assert.False(t, sent, "a typed turn, not a sent one")
		assert.NotNil(t, grant, "the signed approval goes with it")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done it"}}]}`))
		ran <- body.Messages[0].Content
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
