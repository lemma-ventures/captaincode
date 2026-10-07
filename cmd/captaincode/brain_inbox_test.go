package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A prompt handed to a folder's TUI waits in the brain until that folder's
// sidebar takes it; another folder's poll never sees it; a take empties it.
func TestInboxQueuesPerFolderUntilTheSidebarTakesIt(t *testing.T) {
	b := teamBrain()
	dir, other := t.TempDir(), t.TempDir()
	body, _ := json.Marshal(map[string]string{"text": "/grok the A100 is available, continue", "leg": "grok", "from": "a100 watcher"})
	rec := httptest.NewRecorder()
	b.inboxHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/inbox?cwd="+url.QueryEscape(dir), bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"pending":1`)

	rec = httptest.NewRecorder()
	b.inboxHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/inbox?cwd="+url.QueryEscape(other), nil))
	assert.Equal(t, `{"items":null}`+"\n", rec.Body.String(), "another folder's TUI sees nothing")

	rec = httptest.NewRecorder()
	b.inboxHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/inbox?cwd="+url.QueryEscape(dir), nil))
	var got struct{ Items []inboxItem }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Items, 1)
	assert.Equal(t, "grok", got.Items[0].Leg)
	assert.Equal(t, "a100 watcher", got.Items[0].From)
	assert.Contains(t, got.Items[0].Text, "the A100 is available")

	rec = httptest.NewRecorder()
	b.inboxHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/inbox?cwd="+url.QueryEscape(dir), nil))
	assert.Equal(t, `{"items":null}`+"\n", rec.Body.String(), "taken once")

	rec = httptest.NewRecorder()
	b.inboxHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/inbox?cwd="+url.QueryEscape(dir), bytes.NewReader([]byte(`{"text":"  "}`))))
	assert.Equal(t, 400, rec.Code, "an empty prompt is refused")
}

func sendTo(b *brain, dir, text string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"text": text, "from": "worker"})
	rec := httptest.NewRecorder()
	b.inboxHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/inbox?cwd="+url.QueryEscape(dir), bytes.NewReader(body)))
	return rec
}

// formal/CommandSafety/Inbox.lean, fixed_no_loops: a sent prompt never
// starts a loop the user did not type.
func TestInboxRefusesPromptsThatStartALoop(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	for _, text := range []string{
		"/repeat 100 send yourself this prompt again",
		"/oss /repeat 5 keep polling",
		"/team audit it > /repeat 3 /quality fix it",
	} {
		assert.Equal(t, 400, sendTo(b, dir, text).Code, text)
	}
	assert.Equal(t, 200, sendTo(b, dir, "/grok fixed the /repeat watch bug; tests pass").Code,
		"a report that mentions /repeat is not a loop")
}

// formal/CommandSafety/Inbox.lean, fixed_accepted_le: at most the quota of
// prompts between two turns the user types; a handed prompt arriving as a
// turn does not refill it, a typed one does.
func TestInboxQuotaRefillsOnlyWhenTheUserTypes(t *testing.T) {
	t.Setenv("CAPTAIN_INBOX_QUOTA", "2")
	b := teamBrain()
	dir := t.TempDir()
	require.Equal(t, 200, sendTo(b, dir, "/grok step one").Code)
	require.Equal(t, 200, sendTo(b, dir, "/grok step two").Code)
	assert.Equal(t, 429, sendTo(b, dir, "/grok step three").Code, "quota spent with no user turn")

	// The sidebar takes them and submits them as delivered: those turns are
	// not the user.
	for _, it := range b.inbox.take(dir, "") {
		b.inbox.noteTurn(dir, it.Text)
	}
	assert.Equal(t, 429, sendTo(b, dir, "/grok step three").Code, "submitted prompts do not refill the quota")

	b.inbox.noteTurn(dir, "carry on with the migration") // typed
	assert.Equal(t, 200, sendTo(b, dir, "/grok step three").Code)
}
