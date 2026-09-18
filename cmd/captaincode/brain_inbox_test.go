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
