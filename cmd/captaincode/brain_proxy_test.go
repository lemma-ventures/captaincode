package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyRedactsRequestAndRestoresStreamedIdentity(t *testing.T) {
	t.Setenv("CAPTAIN_REDACT", "on")
	captaincode.SetIdentityRulesForTest(map[string]string{"/home/jdoe": "/home/captain", "jdoe": "captain-user"})
	var seen string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		seen = string(b)
		assert.Equal(t, "Bearer real-provider-key", r.Header.Get("Authorization"), "the provider's own credential passes through")
		w.Header().Set("Content-Type", "text/event-stream")
		// A stand-in split across two events, then a structural event.
		for _, l := range []string{
			`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"see /home"}}`, ``,
			`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"/captain"}}`, ``,
			`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"/Gits/arc/x.go now"}}`, ``,
			`event: content_block_stop`, `data: {"type":"content_block_stop","index":0}`, ``,
			`data: [DONE]`,
		} {
			_, _ = w.Write([]byte(l + "\n"))
		}
	}))
	defer up.Close()
	proxyUpstreams["test"] = up.URL
	defer delete(proxyUpstreams, "test")

	body := `{"messages":[{"role":"user","content":"fix /home/jdoe/Gits/arc/x.go; key sk-ant-api03-abcdefghijklmnopqrstuvwxyz"}]}`
	req := httptest.NewRequest(http.MethodPost, "/test/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer real-provider-key")
	rec := httptest.NewRecorder()
	proxyHandler(rec, req)
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, seen, "/home/captain/Gits/arc/x.go", "identity rewritten on the wire")
	assert.Contains(t, seen, "[[secret:anthropic:", "the secret never reached the upstream")
	assert.NotContains(t, seen, "jdoe")
	out := rec.Body.String()
	var text strings.Builder
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "data: {") {
			var ev struct {
				Delta struct{ Text string } `json:"delta"`
			}
			require.NoError(t, json.Unmarshal([]byte(l[6:]), &ev), l)
			text.WriteString(ev.Delta.Text)
		}
	}
	assert.Equal(t, `see /home/jdoe/Gits/arc/x.go now`, text.String(), "the split stand-in is restored across events: %s", out)
	assert.NotContains(t, out, "/home/captain")
	assert.Contains(t, out, "content_block_stop")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(out), "data: [DONE]"), "stream shape kept: %s", out)
	// The event:/blank lines still precede their data lines.
	assert.Less(t, strings.Index(out, "event: content_block_delta"), strings.Index(out, "data: {"))
}

// chatgpt.com's codex stream arrives without a Content-Type (live
// 2026-09-13); the request's Accept says what it is, and identity is still
// restored in it.
func TestProxyRestoresAnUntypedStreamTheClientAskedFor(t *testing.T) {
	t.Setenv("CAPTAIN_REDACT", "on")
	captaincode.SetIdentityRulesForTest(map[string]string{"/home/jdoe": "/home/captain", "jdoe": "captain-user"})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil // no sniffing: an untyped body
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"edit /home/captain/x.go\"}\n\n"))
	}))
	defer up.Close()
	proxyUpstreams["test"] = up.URL
	defer delete(proxyUpstreams, "test")

	req := httptest.NewRequest(http.MethodPost, "/test/backend-api/codex/responses", strings.NewReader(`{"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	proxyHandler(rec, req)
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "/home/jdoe/x.go", "restored although the upstream named no content type: %s", rec.Body.String())
}
