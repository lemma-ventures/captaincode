package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInboxReviewIsReadOnlyAndFolderScoped(t *testing.T) {
	b := teamBrain()
	dir, other := t.TempDir(), t.TempDir()
	item := inboxItem{ID: "held-one", Text: "Review this message", Dir: dir, At: time.Now()}
	b.inbox.hold(item)
	b.inbox.hold(inboxItem{ID: "held-other", Text: "Another folder", Dir: other, At: time.Now()})
	b.inbox.push(dir, "Deliver normally", "", "", "", "worker")
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		b.inboxHeldHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/inbox/held?cwd="+url.QueryEscape(dir), nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var out struct{ Held []inboxItem }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.Len(t, out.Held, 1)
		assert.Equal(t, item.ID, out.Held[0].ID)
		assert.Equal(t, item.Text, out.Held[0].Text)
	}
	assert.Equal(t, 1, b.inbox.pending(dir))
	assert.Len(t, b.inbox.takeNotify(dir), 1)
	assert.Len(t, b.inbox.heldFor(""), 2)
}

func TestInboxReviewActionsAffectOnlyTheSelectedFolderAndKeepSentTrust(t *testing.T) {
	for _, action := range []string{"release", "drop"} {
		t.Run(action, func(t *testing.T) {
			b := teamBrain()
			dir, other := t.TempDir(), t.TempDir()
			item := inboxItem{ID: "held-one", Text: "Review this message", Dir: dir, At: time.Now(), Origin: "worker", Session: "ses_1"}
			b.inbox.hold(item)
			apply := func(folder string) *httptest.ResponseRecorder {
				body, err := json.Marshal(map[string]string{"id": item.ID, "action": action})
				require.NoError(t, err)
				rec := httptest.NewRecorder()
				b.inboxHeldHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/inbox/held?cwd="+url.QueryEscape(folder), bytes.NewReader(body)))
				return rec
			}
			assert.Equal(t, http.StatusNotFound, apply(other).Code)
			require.Len(t, b.inbox.heldFor(dir), 1)
			require.Equal(t, http.StatusOK, apply(dir).Code)
			assert.Empty(t, b.inbox.heldFor(dir))
			assert.Empty(t, b.inbox.takeNotify(dir))
			assert.Equal(t, http.StatusNotFound, apply(dir).Code)
			items := b.inbox.take(dir, "ses_1")
			if action == "drop" {
				assert.Empty(t, items)
				return
			}
			require.Len(t, items, 1)
			assert.Contains(t, items[0].Text, item.Text)
			by, sent, grant := b.inbox.notePresenceTurn(dir, items[0].Text)
			assert.True(t, sent)
			assert.Contains(t, by, "worker")
			assert.Nil(t, grant)
		})
	}
}
