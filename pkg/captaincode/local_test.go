package captaincode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type localRoundTrip func(*http.Request) (*http.Response, error)

func (f localRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func localFixture(t *testing.T, fail bool) (LocalConfig, *int) {
	t.Helper()
	calls := 0
	c := LocalConfig{Endpoint: "http://127.0.0.1:11434", Model: "test-model"}
	c.client = &http.Client{Transport: localRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		switch r.URL.Path {
		case "/api/tags":
			body = `{"models":[{"name":"test-model","digest":"sha256:fixture"}]}`
		case "/api/show":
		case "/v1/chat/completions":
			var req struct {
				Messages []struct{ Content string }
				Tools    json.RawMessage
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Empty(t, req.Tools)
			s := req.Messages[1].Content
			answer := ""
			for _, p := range localProbes {
				if p.input == s {
					switch p.name {
					case "typo":
						answer = `{"class":"trivial","domain":"code","abstain":false}`
					case "security":
						answer = `{"class":"high","domain":"code","abstain":false}`
					case "routing_abstention":
						answer = `{"abstain":true}`
					case "title":
						answer = "Fix retry timeout"
					case "commit":
						answer = "Add retry timeout"
					case "retrieval_abstention":
						answer = "CANNOT_TELL"
					case "digest_facts":
						answer = "4 tests passed and 1 retry test failed."
					case "learn_update":
						answer = `{"summary":"Updated retry state","edits":[{"file":"BRAIN.md","mode":"replace_section","anchor":"## State","text":"Retries are enabled with at most 3 attempts.","why":"The journal records the change."}]}`
					case "learn":
						answer = `{"summary":"","edits":[]}`
					}
				}
			}
			calls++
			if fail && calls == 1 {
				answer = "invented"
			}
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": answer}}}, "usage": map[string]any{"prompt_tokens": 30, "completion_tokens": 5, "prompt_tokens_details": map[string]int{"cached_tokens": 20}}})
			body = "data: " + string(chunk) + "\n\ndata: [DONE]\n\n"
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	return c, &calls
}
func TestLocalEndpointBoundary(t *testing.T) {
	for _, u := range []string{"http://example.com", "http://localhost:11434", "http://127.0.0.1@evil.test", "http://127.0.0.1/foo", "http://127.0.0.1?proxy=x", "https://127.0.0.1"} {
		require.Error(t, (LocalConfig{Endpoint: u, Model: "x"}).Validate(), u)
	}
	for _, u := range []string{"http://127.0.0.1:11434", "http://[::1]:11434"} {
		require.NoError(t, (LocalConfig{Endpoint: u, Model: "x"}).Validate())
	}
	require.Error(t, localClient().CheckRedirect(nil, nil))
	require.Nil(t, localClient().Transport.(*http.Transport).Proxy)
}
func TestLocalQualificationThreeCompleteRounds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			c, n := localFixture(t, fail)
			q, e := QualifyLocal(context.Background(), c)
			require.NoError(t, e)
			require.Equal(t, 3*len(localProbes), *n)
			require.Equal(t, !fail, q.Passed)
			require.Equal(t, !fail, q.valid(c, q.Digest, time.Now()))
			require.False(t, q.valid(c, "changed", time.Now()))
			require.False(t, q.valid(c, q.Digest, q.At.Add(31*24*time.Hour)))
			stored, e := ReadLocalQualification()
			require.NoError(t, e)
			require.Equal(t, q.Passed, stored.Passed)
			st, e := os.Stat(LocalQualificationPath())
			require.NoError(t, e)
			require.Equal(t, os.FileMode(0600), st.Mode().Perm())
		})
	}
}
func TestLocalNoCodingAndNoUnqualifiedDispatch(t *testing.T) {
	require.False(t, ServesTasks(LegLocal))
	require.False(t, AutoRoutes(LegLocal))
	require.False(t, DirectorCapable(LegLocal))
	_, e := NewDispatcher(1).Run(LegLocal, "edit files")
	require.Error(t, e)
	_, e = (Workspace{}).RunWorkerStreamHooks(LegLocal, "edit files", 1, nil, nil)
	require.Error(t, e)
	c, n := localFixture(t, false)
	_, e = c.complete(context.Background(), "coding", "edit files")
	require.Error(t, e)
	require.Zero(t, *n)
	q := LocalQualification{Passed: true}
	require.False(t, q.valid(c, "fixture", time.Now()))
	_, e = c.complete(context.Background(), "title", strings.Repeat("x", localMaxInput+1))
	require.Error(t, e)
	require.Zero(t, *n)
}
func TestLocalUsageAndAbstention(t *testing.T) {
	c, _ := localFixture(t, false)
	r, e := c.complete(context.Background(), "digest", localProbes[5].input)
	require.NoError(t, e)
	require.Equal(t, "CANNOT_TELL", r.Text)
	require.Equal(t, 35, r.Tokens)
	require.Equal(t, 10, *r.TokenUsage.Input)
	require.Equal(t, 20, *r.TokenUsage.CacheRead)
	require.NotNil(t, r.TTFTMs)
	require.False(t, localClassIs(`{"class":"medium"}`, "", true))
	require.True(t, localClassIs(`{"abstain":true}`, "", true))
}

func TestLocalRejectsRemoteAndChangedModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name, show string
		change     bool
	}{
		{name: "remote", show: `{"remote_host":"https://example.invalid","remote_model":"remote"}`},
		{name: "changed", show: `{}`, change: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := localFixture(t, false)
			base := c.client.Transport
			tagCalls := 0
			c.client.Transport = localRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/show" {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.show)), Header: make(http.Header)}, nil
				}
				if r.URL.Path == "/api/tags" {
					tagCalls++
					if tc.change && tagCalls > 1 {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"test-model","digest":"changed"}]}`)), Header: make(http.Header)}, nil
					}
				}
				return base.RoundTrip(r)
			})
			q, err := QualifyLocal(context.Background(), c)
			if tc.name == "remote" {
				require.Error(t, err)
				require.Zero(t, *calls)
			} else {
				require.NoError(t, err)
			}
			require.False(t, q.Passed)
			stored, err := ReadLocalQualification()
			require.NoError(t, err)
			require.False(t, stored.Passed)
		})
	}
}

func TestLocalRequalificationRevokesPassOnUnavailableRuntime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, _ := localFixture(t, false)
	q, err := QualifyLocal(context.Background(), c)
	require.NoError(t, err)
	require.True(t, q.Passed)
	c.client.Transport = localRoundTrip(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("unavailable") })
	_, err = QualifyLocal(context.Background(), c)
	require.Error(t, err)
	q, err = ReadLocalQualification()
	require.NoError(t, err)
	require.False(t, q.Passed)
}

func TestLocalRejectsBrokenStreams(t *testing.T) {
	for _, body := range []string{
		"data: {bad}\n\ndata: [DONE]\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n",
		"data: [DONE]\n\n",
	} {
		c, _ := localFixture(t, false)
		c.client.Transport = localRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		_, err := c.complete(context.Background(), "title", "fixture")
		require.Error(t, err)
	}
}

func TestLocalRegistryCannotPromoteHelper(t *testing.T) {
	require.Error(t, validateSpec(LegSpec{ID: LegLocal, Transport: TransportOpencode, Provider: "fixture", Model: "fixture"}))
	require.Error(t, validateSpec(LegSpec{ID: "another", Transport: TransportLocal}))
	_, err := ParseWorkflow("/local edit files > /claude review")
	require.Error(t, err)
}
