package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func localProseServer(t *testing.T, chat string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"gpt-oss:20b","digest":"sha256:fixture"}]}`)
		case "/api/show":
			fmt.Fprint(w, `{}`)
		case "/api/chat":
			if status != 200 {
				w.WriteHeader(status)
				return
			}
			var req map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Equal(t, false, req["think"])
			_, tools := req["tools"]
			require.False(t, tools)
			fmt.Fprint(w, chat)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSummarizeAndNarrateUseTheLocalTag(t *testing.T) {
	summary := "1) Task state: the parser fix is merged. " + strings.Repeat("2) Decisions: keep the a.go layout. ", 6)
	reply, _ := json.Marshal(map[string]any{"message": map[string]string{"content": summary}, "done": true, "done_reason": "stop"})
	fold := localProseServer(t, string(reply)+"\n", 200)
	defer fold.Close()
	t.Setenv("CAPTAIN_COMPACT_LOCAL", "gpt-oss:20b")
	t.Setenv("CAPTAIN_LOCAL_URL", fold.URL)
	out, err := summarizeSpan("the span", "", nil)
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(summary), out)

	srv := localProseServer(t, "{\"message\":{\"content\":\"1. listed a.go\"},\"done\":true,\"done_reason\":\"stop\"}\n", 200)
	defer srv.Close()
	t.Setenv("CAPTAIN_LOCAL_URL", srv.URL)
	lines, err := (&brain{}).narrateSteps("list files", []captaincode.Step{{
		At: time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC), Tool: "bash", Input: "ls", Output: "a.go",
	}})
	require.NoError(t, err)
	require.Equal(t, []string{"listed a.go"}, lines)
}

func TestLocalProseFailureDoesNotCallAnotherLeg(t *testing.T) {
	srv := localProseServer(t, "", 500)
	defer srv.Close()
	t.Setenv("CAPTAIN_COMPACT_LOCAL", "gpt-oss:20b")
	t.Setenv("CAPTAIN_LOCAL_URL", srv.URL)
	_, err := summarizeSpan("the span", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local")
	_, err = (&brain{}).narrateSteps("list files", []captaincode.Step{{Tool: "bash", Input: "ls"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local")
}

// Euclid learning asks for JSON only, so CAPTAIN_LEARN_LOCAL runs it on the
// long-context local call: no tools, thinking off, the model's text returned.
func TestLearningUsesTheLocalLearnTag(t *testing.T) {
	srv := localProseServer(t, "{\"message\":{\"content\":\"{\\\"summary\\\":\\\"s\\\",\\\"lessons\\\":[]}\"},\"done\":true,\"done_reason\":\"stop\"}\n", 200)
	defer srv.Close()
	t.Setenv("CAPTAIN_LEARN_LOCAL", "gpt-oss:20b")
	t.Setenv("CAPTAIN_LOCAL_URL", srv.URL)
	res, on, err := runLearnLocal("[euclid learn] return JSON only")
	require.True(t, on)
	require.NoError(t, err)
	assert.Equal(t, `{"summary":"s","lessons":[]}`, res.Text)
	assert.Equal(t, captaincode.LegLocal, (&brain{}).distillLeg(), "the run is charged to the local leg")
}

func TestLocalLearningFailureDoesNotCallAnotherLeg(t *testing.T) {
	srv := localProseServer(t, "", 500)
	defer srv.Close()
	t.Setenv("CAPTAIN_LEARN_LOCAL", "gpt-oss:20b")
	t.Setenv("CAPTAIN_LOCAL_URL", srv.URL)
	_, on, err := runLearnLocal("[euclid learn] return JSON only")
	require.True(t, on)
	require.Error(t, err)
	t.Setenv("CAPTAIN_LEARN_LOCAL", "")
	_, on, _ = runLearnLocal("x")
	assert.False(t, on, "unset, learning keeps its distill leg")
}

func TestLocalCompactionWaitsLongerThanAHostedFold(t *testing.T) {
	assert.Equal(t, 2*time.Minute+26*time.Second, localCompactWait(26_000))
	assert.Equal(t, 2*time.Minute+186*time.Second, localCompactWait(186_000))
	assert.Equal(t, 8*time.Minute, localCompactWait(400_000))
}

// A fold that answers with its system line or a few words is not a summary
// (23 Oct: the local model echoed its system line on a 39k-token span). The
// brief comes again after the span, and the call samples rather than decodes
// greedily.
func TestALocalFoldThatIsNotASummaryIsRefused(t *testing.T) {
	var sent map[string]any
	for _, text := range []string{compactLocalSystem, "Done."} {
		reply, _ := json.Marshal(map[string]any{"message": map[string]string{"content": text}, "done": true, "done_reason": "stop"})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/tags":
				fmt.Fprint(w, `{"models":[{"name":"gpt-oss:20b","digest":"sha256:fixture"}]}`)
			case "/api/show":
				fmt.Fprint(w, `{}`)
			case "/api/chat":
				require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
				fmt.Fprint(w, string(reply)+"\n")
			}
		}))
		t.Setenv("CAPTAIN_COMPACT_LOCAL", "gpt-oss:20b")
		t.Setenv("CAPTAIN_LOCAL_URL", srv.URL)
		_, err := summarizeSpan("the span", "", nil)
		srv.Close()
		require.Error(t, err, "%q is not a summary", text)
		assert.Contains(t, err.Error(), "not a summary")
	}
	msgs := sent["messages"].([]any)
	user := msgs[len(msgs)-1].(map[string]any)["content"].(string)
	assert.True(t, strings.HasSuffix(user, "Max ~600 words."), "the brief comes again after the span")
	opts := sent["options"].(map[string]any)
	assert.Equal(t, 0.7, opts["temperature"])
	assert.Equal(t, 0.8, opts["top_p"])
}

func TestLocalLearningDecodesGreedily(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"gpt-oss:20b","digest":"sha256:fixture"}]}`)
		case "/api/show":
			fmt.Fprint(w, `{}`)
		case "/api/chat":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
			fmt.Fprint(w, `{"message":{"content":"{\"summary\":\"s\",\"lessons\":[]}"},"done":true,"done_reason":"stop"}`+"\n")
		}
	}))
	defer srv.Close()
	t.Setenv("CAPTAIN_LEARN_LOCAL", "gpt-oss:20b")
	t.Setenv("CAPTAIN_LOCAL_URL", srv.URL)
	_, on, err := runLearnLocal("[euclid learn] batch")
	require.True(t, on)
	require.NoError(t, err)
	opts := sent["options"].(map[string]any)
	assert.Equal(t, 0.0, opts["temperature"])
	_, sampled := opts["top_p"]
	assert.False(t, sampled, "the same lessons from the same batch")
}
