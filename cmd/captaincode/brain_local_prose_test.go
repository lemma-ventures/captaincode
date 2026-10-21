package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	srv := localProseServer(t, "{\"message\":{\"content\":\"1. listed a.go\"},\"done\":true,\"done_reason\":\"stop\"}\n", 200)
	defer srv.Close()
	t.Setenv("CAPTAIN_COMPACT_LOCAL", "gpt-oss:20b")
	t.Setenv("CAPTAIN_LOCAL_URL", srv.URL)
	out, err := summarizeSpan("the span", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "1. listed a.go", out)
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
