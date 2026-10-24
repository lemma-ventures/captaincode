package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
			`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"/Gits/ash/x.go now"}}`, ``,
			`event: content_block_stop`, `data: {"type":"content_block_stop","index":0}`, ``,
			`data: [DONE]`,
		} {
			_, _ = w.Write([]byte(l + "\n"))
		}
	}))
	defer up.Close()
	proxyUpstreams["test"] = up.URL
	defer delete(proxyUpstreams, "test")

	body := `{"messages":[{"role":"user","content":"fix /home/jdoe/Gits/ash/x.go; key sk-ant-api03-abcdefghijklmnopqrstuvwxyz"}]}`
	req := httptest.NewRequest(http.MethodPost, "/test/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer real-provider-key")
	rec := httptest.NewRecorder()
	proxyHandler(rec, req)
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, seen, "/home/captain/Gits/ash/x.go", "identity rewritten on the wire")
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
	assert.Equal(t, `see /home/jdoe/Gits/ash/x.go now`, text.String(), "the split stand-in is restored across events: %s", out)
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

func TestProxyAddsTheLegsOpenRouterHosts(t *testing.T) {
	t.Setenv("CAPTAIN_GLM_MODEL", "")
	t.Setenv("CAPTAIN_GLM_PROVIDER", "")
	t.Setenv("CAPTAIN_CHEAP_TIER", "")
	path := filepath.Join(t.TempDir(), "legs.json")
	data, err := json.Marshal(map[string]any{"legs": []map[string]any{{
		"id": "glm", "provider": "openrouter", "model": "z-ai/glm-5.3",
		"hosts": map[string]any{"quality": map[string]any{"order": []string{"z-ai"}}},
	}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	_, err = captaincode.LoadRegistry(path)
	t.Cleanup(func() { captaincode.LoadRegistry(filepath.Join(t.TempDir(), "none.json")) })
	require.NoError(t, err)

	out, ok := routeOpenRouterHosts([]byte(`{"model":"z-ai/glm-5.3","messages":[]}`))
	require.True(t, ok)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(out, &obj))
	assert.Equal(t, map[string]any{"order": []any{"z-ai"}}, obj["provider"])

	_, ok = routeOpenRouterHosts([]byte(`{"model":"z-ai/glm-5.3","provider":{"order":["cerebras"]}}`))
	assert.False(t, ok, "a request that already names its hosts (a /deterministic pin) is left alone")
	_, ok = routeOpenRouterHosts([]byte(`{"model":"minimax/minimax-m3"}`))
	assert.False(t, ok)
}

// Codex sends its reasoning back as an opaque base64url blob that only the
// provider can decrypt. One blob held "-sk-" and 20 more characters, the key
// rule replaced it, and the round failed with invalid_encrypted_content
// (2026-10-24). The blob is passed through; a key next to it is still masked.
func TestProxyLeavesEncryptedReasoningIntact(t *testing.T) {
	t.Setenv("CAPTAIN_REDACT", "on")
	key := "s" + "k-" + strings.Repeat("Q7x", 9)
	blob := "gAAAAB" + strings.Repeat("aZ09_", 12) + "-" + key + "_tail=="
	body := `{"input":[{"type":"reasoning","encrypted_content":"` + blob + `"},{"type":"message","content":"use ` + key + `"}]}`
	out, rep := proxyRedactBody(body)
	assert.Contains(t, out, `"encrypted_content":"`+blob+`"`, "the provider's blob arrives byte for byte")
	assert.NotContains(t, strings.Replace(out, blob, "", 1), key, "a key in readable text is still masked")
	assert.Equal(t, 1, rep.Secrets)
}

// Codex runs shell commands as a custom tool call whose command is in
// "input". That field was not restored, so the commands kept the stand-in
// home: `cat /Users/rpellerin/.codex/skills/…/SKILL.md` found no skill and
// `cd /Users/rpellerin/Gits/…` failed (2026-10-24).
func TestProxyRestoresIdentityInCodexToolInput(t *testing.T) {
	t.Setenv("CAPTAIN_REDACT", "on")
	captaincode.SetIdentityRulesForTest(map[string]string{"/home/jdoe": "/home/captain", "jdoe": "rpellerin"})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`event: response.custom_tool_call_input.delta`, `data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","delta":"cat /home/captain/.codex/skills/rigor/SKILL.md"}`, ``,
			`event: response.output_item.done`, `data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"exec","status":"completed","input":"cat /home/captain/.codex/skills/rigor/SKILL.md"}}`, ``,
		} {
			_, _ = w.Write([]byte(l + "\n"))
		}
	}))
	defer up.Close()
	proxyUpstreams["test"] = up.URL
	defer delete(proxyUpstreams, "test")
	req := httptest.NewRequest(http.MethodPost, "/test/responses", strings.NewReader(`{"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxyHandler(rec, req)
	out := rec.Body.String()
	assert.NotContains(t, out, "/home/captain", out)
	assert.Equal(t, 2, strings.Count(out, "cat /home/jdoe/.codex/skills/rigor/SKILL.md"), out)
}
