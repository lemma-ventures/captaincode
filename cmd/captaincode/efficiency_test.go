package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/require"
)

func TestLocalSelectionNeverFallsBackToCloud(t *testing.T) {
	t.Setenv("CAPTAIN_LOCAL_TASKS", "classify,title,learn")
	t.Setenv("CAPTAIN_LOCAL_URL", "https://remote.invalid")
	t.Setenv("CAPTAIN_LOCAL_MODEL", "test")
	b := teamBrain()
	b.classifyLLMFn = func(string) (captaincode.Class, captaincode.Domain, error) {
		t.Fatal("cloud classifier called")
		return "", "", nil
	}
	b.runWorkerFn = func(captaincode.Leg, string, func(string), func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Fatal("worker called")
		return "", captaincode.Result{}, nil
	}
	_, _, _, e := b.classifyTier1("ambiguous task", captaincode.DomainGeneral, nil)
	require.Error(t, e)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"system","content":"You are a title generator"},{"role":"user","content":"Name this session"}]}`))
	out := httptest.NewRecorder()
	b.chatCompletions(out, req)
	require.Equal(t, 400, out.Code)
	require.Equal(t, captaincode.LegLocal, b.distillLeg())
	// No local runtime exists in this test; the invalid origin fails before I/O.
	_, e = captaincode.RunLocalSmall(context.Background(), "learn", "facts")
	require.Error(t, e)
}
func TestLeanMCPEnforcesAdvertisedTools(t *testing.T) {
	t.Setenv("CAPTAIN_EUCLID_TOOL_PROFILE", "lean")
	_, err := euclidToolCall("note", map[string]any{"kind": "memory", "text": "do not write"}, t.TempDir())
	require.ErrorContains(t, err, "unavailable")
	var out strings.Builder
	serveEuclidMCP(strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n"), &out, func() string { return t.TempDir() })
	require.Contains(t, out.String(), `"code_search"`)
	require.NotContains(t, out.String(), `"note"`)
}
func TestWindowPromptStableMarkerAndBound(t *testing.T) {
	first := strings.Repeat("A", 8000) + strings.Repeat("B", 8000)
	a := windowPrompt(first, 2000)
	b := windowPrompt(first+"added turn", 2000)
	require.LessOrEqual(t, len(a), 2000)
	require.LessOrEqual(t, len(b), 2000)
	require.Equal(t, a[:470], b[:470], "marker must not change when history grows")
	require.LessOrEqual(t, len(windowPrompt(first, 20)), 20)
}
func TestUsageBlockUsesMeasuredBuckets(t *testing.T) {
	r := captaincode.Result{Tokens: 135, TokenUsage: captaincode.ClaudeTokenUsage(10, 5, 100, 20)}
	u := usageBlock(captaincode.LegClaude, r)
	require.Equal(t, 130, u["prompt_tokens"])
	require.Equal(t, 5, u["completion_tokens"])
	require.Equal(t, "claude:run", u["captain_usage_split"])
	r.TokenUsage = nil
	u = usageBlock(captaincode.LegClaude, r)
	require.Equal(t, "estimated_3_to_1", u["captain_usage_split"])
}

func TestUsageFailureEventKeepsReportedBuckets(t *testing.T) {
	b := teamBrain()
	res := captaincode.Result{Tokens: 135, TokenUsage: captaincode.ClaudeTokenUsage(10, 5, 100, 20)}
	b.onWorkerError(captaincode.LegClaude, fmt.Errorf("fixture failed"), res)
	require.Equal(t, 100, *b.ledger.Events[len(b.ledger.Events)-1].TokenUsage.CacheRead)
}

func TestCheaperPolicyStampedOnlyForAutomaticDirectorAnswer(t *testing.T) {
	t.Setenv("CAPTAIN_DIRECTOR_PICK", "1")
	for _, tc := range []struct {
		name, prefer, want string
		fail               bool
	}{
		{name: "automatic", want: "cheap-capable-v1"},
		{name: "preference", prefer: "speed"},
		{name: "failed", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := teamBrain()
			noDirector(t, b)
			b.pickFn = func(string, captaincode.Class, string, []captaincode.Scored, captaincode.Leg) (captaincode.WorkerPick, error) {
				if tc.fail {
					return captaincode.WorkerPick{}, fmt.Errorf("fixture failure")
				}
				return captaincode.WorkerPick{Leg: captaincode.LegGLM, Class: captaincode.ClassHigh}, nil
			}
			task := "audit the security of the auth proxy across the codebase"
			routeBody(t, b, task, map[string]any{"prefer": tc.prefer})
			decision := decisionAfterRoute(t, b, task)
			require.Equal(t, tc.want, decision.TiePolicy)
			require.Equal(t, tc.prefer, decision.Preference)
		})
	}
}
