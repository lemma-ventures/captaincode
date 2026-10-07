package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useReplyBook(t *testing.T) {
	t.Helper()
	saved := replies
	replies = &replyBook{path: filepath.Join(t.TempDir(), "reply-to.json")}
	t.Cleanup(func() { replies = saved })
}

func postInbox(b *brain, cwd string, body map[string]string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	b.inboxHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/inbox?cwd="+url.QueryEscape(cwd), bytes.NewReader(data)))
	return rec
}

func takeInbox(b *brain, dir, session string) []inboxItem {
	rec := httptest.NewRecorder()
	b.inboxHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/inbox?cwd="+url.QueryEscape(dir)+"&session="+url.QueryEscape(session), nil))
	var got struct{ Items []inboxItem }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return got.Items
}

// A worker that moved into another repository still replies to the TUI it
// came from, and the token outlives a brain restart.
func TestReplyTokenPointsAtTheTurnsOwnSessionAndSurvivesARestart(t *testing.T) {
	useReplyBook(t)
	tui, other := t.TempDir(), t.TempDir()
	ws := captaincode.Workspace{Dir: tui, Origin: tui, Session: "ses_a"}.At(other) // followTask moved the worker
	id := replyTo(ws, "frontier")
	require.True(t, strings.HasPrefix(id, "rt_"))

	restarted := &replyBook{path: replies.path}
	got, ok := restarted.resolve(id)
	require.True(t, ok, "read back from disk")
	assert.Equal(t, tui, got.Dir, "the TUI's folder, not the repository the worker moved to")
	assert.Equal(t, "ses_a", got.Session)
	_, ok = restarted.resolve("rt_unknown")
	assert.False(t, ok)

	assert.Contains(t, callbackContract(ws, "frontier"), "captain send --reply rt_", "workers are told to reply, not to name a folder")
}

func TestReplyReachesItsSessionWithItsOrigin(t *testing.T) {
	useReplyBook(t)
	b := teamBrain()
	tui, elsewhere := t.TempDir(), t.TempDir()
	id := replies.mint(replyTarget{Dir: tui, Session: "ses_a", Leg: "frontier"})

	// Sent from anywhere, it lands in the TUI's folder.
	rec := postInbox(b, elsewhere, map[string]string{"text": "the Sean build failed: the voice tool takes only input", "from": "frontier", "reply": id, "sender_dir": elsewhere})
	require.Equal(t, 200, rec.Code, rec.Body.String())

	assert.Empty(t, takeInbox(b, tui, "ses_b"), "another session in the same folder waits")
	items := takeInbox(b, tui, "ses_a")
	require.Len(t, items, 1)
	assert.Contains(t, items[0].Origin, "frontier turn of")
	assert.Contains(t, items[0].Origin, filepath.Base(tui))
	assert.Contains(t, items[0].Text, "- sent by frontier, for the frontier turn of", "the transcript says who sent it")

	rec = postInbox(b, elsewhere, map[string]string{"text": "late", "reply": "rt_gone"})
	assert.Equal(t, 404, rec.Code, "an unknown token is refused, not dropped somewhere")
}

func TestReplyForAClosedSessionIsNotStranded(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	b.inbox.push(dir, "report", "", "frontier", "ses_closed", "frontier")
	assert.Empty(t, b.inbox.take(dir, "ses_open"))
	b.inbox.mu.Lock()
	b.inbox.items[dir][0].At = time.Now().Add(-inboxSessionWait - time.Second)
	b.inbox.mu.Unlock()
	assert.Len(t, b.inbox.take(dir, "ses_open"), 1, "after the wait, any session in the folder takes it")
}

// Captain to captain: sending to another folder on purpose still works, and
// says which folder it came from.
func TestSendToAnotherFolderSaysWhereItCameFrom(t *testing.T) {
	b := teamBrain()
	target, sender := t.TempDir(), t.TempDir()
	rec := postInbox(b, target, map[string]string{"text": "the shared schema changed; rebase on it", "from": "claude", "sender_dir": sender})
	require.Equal(t, 200, rec.Code, rec.Body.String())
	items := takeInbox(b, target, "")
	require.Len(t, items, 1)
	assert.Equal(t, "claude in "+filepath.Base(sender)+" (another folder, unverified)", items[0].Origin, "only a reply token proves the sender")

	rec = postInbox(b, target, map[string]string{"text": "same folder", "from": "claude", "sender_dir": target})
	require.Equal(t, 200, rec.Code)
	items = takeInbox(b, target, "")
	require.Len(t, items, 1)
	assert.Equal(t, "claude", items[0].Origin, "from the same folder, the sender is enough")
}

func TestSessionSeenNamesTheTurnsSession(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	rec := httptest.NewRecorder()
	b.sessionSeenHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/session/seen?cwd="+url.QueryEscape(dir), strings.NewReader(`{"session":"ses_42"}`)))
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, "ses_42", sessions.of(dir))
	assert.Equal(t, "", sessions.of(t.TempDir()))
}
