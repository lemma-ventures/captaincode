package captaincode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeletedSessionIsClassifiedNotSilentlySucceeded reproduces a real
// incident: a persisted ThreadRef pointed at an opencode session that had
// been deleted server-side. Before this fix, the 404 body
// (`{"name":"NotFoundError",...}`) didn't match the expected message-response
// shape, so json.Decode silently succeeded into a zero-value Result - the
// caller saw outcome "ok" with empty text instead of a diagnosable error.
func TestDeletedSessionIsClassifiedNotSilentlySucceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Write([]byte(`{"id":"ses_stale"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/session/ses_stale/message":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"name":"NotFoundError","data":{"message":"Session not found: ses_stale"}}`))
		case r.URL.Path == "/config/providers": // the serve check (EnsureServer)
			w.Write([]byte(`{"providers":[{"id":"opencode"}]}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_stale", Client: srv.Client(), Spawn: false}
	res, err := d.Run(LegFree, "reply with: PONG")

	require.Error(t, err, "a deleted session must surface as an error, never a silent empty-text success")
	assert.ErrorIs(t, err, ErrSessionNotFound)
	assert.Empty(t, res.Text)
}

// TestContextOverflowIsClassified reproduces the 2026-07-18 evening incident:
// a 200k-token TUI conversation replayed through the wrapper exceeded the
// codex worker's context - opencode returned ContextOverflowError ("Session
// too large to compact") on every attempt, the brain mapped it to a generic
// 502, and the fork's SDK blind-retried 8×. The error must surface as a typed,
// non-retryable ErrContextOverflow so the brain can answer 400 with a real
// explanation instead of an endless retry loop.
func TestContextOverflowIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.Write([]byte(`{"info":{"error":{"name":"ContextOverflowError","data":{"message":"Session too large to compact - context exceeds model limit even after stripping media"}},"tokens":{"total":0}},"parts":[]}`))
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	_, err := d.Run(LegCodex, "huge replayed conversation")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextOverflow)
	assert.NotErrorIs(t, err, ErrRateLimited)
}

// TestRateLimitClassificationIgnoresResponseHeaders reproduces the 2026-07-18
// kimi misroute: NVIDIA returned 404 Not Found, but the error JSON embeds the
// upstream x-ratelimit-* RESPONSE HEADERS - and the classifier matched "rate"
// against the whole raw blob, so a missing model got the leg a 30-minute
// rate-limit cooldown. Classification must look at the error name/message
// only, never headers or other metadata.
func TestRateLimitClassificationIgnoresResponseHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.Write([]byte(`{"info":{"error":{"name":"APIError","data":{"message":"Not Found","statusCode":404,"responseHeaders":{"x-ratelimit-limit-requests":"40","x-ratelimit-remaining-requests":"39"}}},"tokens":{"total":0}},"parts":[]}`))
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	_, err := d.Run(LegGLM, "task")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRateLimited, "a 404 with ratelimit response headers is NOT a rate limit")
	assert.Contains(t, err.Error(), "Not Found", "the real upstream cause must survive")
}

// TestExtractResponseHeaders verifies that upstream x-ratelimit-* headers
// embedded in an opencode error JSON are extracted for proactive quota
// telemetry (M2.3), while classification still ignores them (the test above).
func TestExtractResponseHeaders(t *testing.T) {
	t.Run("headers present", func(t *testing.T) {
		raw := json.RawMessage(`{"name":"APIError","data":{"message":"Not Found","statusCode":404,"responseHeaders":{"x-ratelimit-limit-requests":"40","x-ratelimit-remaining-requests":"39","x-ratelimit-reset":"1700000000"}}}`)
		h := extractResponseHeaders(raw)
		require.NotNil(t, h)
		assert.Equal(t, "40", h.Get("X-Ratelimit-Limit-Requests"))
		assert.Equal(t, "39", h.Get("X-Ratelimit-Remaining-Requests"))
		assert.Equal(t, "1700000000", h.Get("X-Ratelimit-Reset"))
	})
	t.Run("no headers in blob", func(t *testing.T) {
		raw := json.RawMessage(`{"name":"APIError","data":{"message":"timeout","statusCode":500}}`)
		h := extractResponseHeaders(raw)
		assert.Nil(t, h)
	})
	t.Run("malformed json", func(t *testing.T) {
		h := extractResponseHeaders(json.RawMessage(`{not json`))
		assert.Nil(t, h)
	})
	t.Run("empty headers map", func(t *testing.T) {
		raw := json.RawMessage(`{"name":"APIError","data":{"message":"ok","responseHeaders":{}}}`)
		h := extractResponseHeaders(raw)
		assert.Nil(t, h)
	})
}

// TestResponseHeadersCarriedOnResult verifies that a non-rate-limit error
// response still carries the upstream response headers on the returned Result,
// so the brain can record proactive quota telemetry (M2.3).
func TestResponseHeadersCarriedOnResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.Write([]byte(`{"info":{"error":{"name":"APIError","data":{"message":"Not Found","statusCode":404,"responseHeaders":{"x-ratelimit-limit-requests":"40","x-ratelimit-remaining-requests":"39"}}},"tokens":{"total":0}},"parts":[]}`))
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	res, err := d.Run(LegGLM, "task")
	require.Error(t, err)
	assert.NotNil(t, res.Headers, "non-rate-limit errors must still carry response headers for quota telemetry")
	assert.Equal(t, "39", res.Headers.Get("X-Ratelimit-Remaining-Requests"))
}

// A REAL rate limit (in the error message itself) must still classify.
func TestRateLimitInMessageStillClassifies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.Write([]byte(`{"info":{"error":{"name":"APIError","data":{"message":"You have hit your usage limit for today","statusCode":429}},"tokens":{"total":0}},"parts":[]}`))
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	_, err := d.Run(LegGLM, "task")
	assert.ErrorIs(t, err, ErrRateLimited)
}

func TestRateLimitedStatusIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	_, err := d.Run(LegFree, "reply with: PONG")
	assert.ErrorIs(t, err, ErrRateLimited)
}

// sseHandler serves the opencode /event bus for tests: an optional initial
// event for sid, then (if tick > 0) a heartbeat event for sid every tick so
// the session looks alive. Blocks until the client disconnects.
func sseHandler(sid string, tick time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		emit := func() {
			fmt.Fprintf(w, "data: {\"type\":\"message.part.updated\",\"properties\":{\"sessionID\":%q,\"part\":{}}}\n\n", sid)
			fl.Flush()
		}
		emit()
		if tick <= 0 {
			<-r.Context().Done()
			return
		}
		tk := time.NewTicker(tick)
		defer tk.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tk.C:
				emit()
			}
		}
	}
}

// abortedBody is what opencode returns for a /message POST whose turn was
// finalized by a server-side abort (observed live 2026-07-18).
const abortedBody = `{"info":{"error":{"name":"MessageAbortedError","data":{"message":"Aborted"}},"tokens":{"total":0}},"parts":[]}`

// wedgedSession registers a session on mux whose /message turn never finishes
// on its own - it only returns (with MessageAbortedError, like real opencode)
// once /abort is called. This is the exact server-side shape of the 2026-07-18
// incident: a tool part stuck "running" forever, POST pending, no events.
func wedgedSession(mux *http.ServeMux, sid string, aborted *atomic.Bool) {
	abortCh := make(chan struct{})
	mux.HandleFunc("/session/"+sid+"/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Watchdog REST cross-check: a wedged session shows STATIC state,
			// so the abort still fires (and the fake answers fast).
			w.Write([]byte(`[{"parts":[{"type":"text","text":"frozen"}]}]`))
			return
		}
		select {
		case <-abortCh:
			w.Write([]byte(abortedBody))
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/session/"+sid+"/abort", func(w http.ResponseWriter, r *http.Request) {
		if aborted != nil {
			aborted.Store(true)
		}
		close(abortCh)
		w.Write([]byte(`true`))
	})
}

// TestStalledWorkerIsAbortedAndClassified reproduces the 2026-07-18 incident:
// the worker's turn wedged server-side (a tool part stuck "running" forever),
// the /message POST never returned, no events flowed - and the whole chain
// (brain wrapper → TUI) froze for 11+ minutes until a MANUAL abort. With a
// stall watchdog, a session with no event-bus activity for StallTimeout must
// be aborted server-side and surface ErrWorkerStalled promptly.
func TestStalledWorkerIsAbortedAndClassified(t *testing.T) {
	var aborted atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/event", sseHandler("ses_wedged", 0)) // one event, then silence: the wedge
	wedgedSession(mux, "ses_wedged", &aborted)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_wedged", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      10 * time.Second,
		StallTimeout: 300 * time.Millisecond,
	}
	done := make(chan error, 1)
	go func() {
		_, err := d.Run(LegFree, "task")
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err, "a wedged turn must not return success")
		assert.ErrorIs(t, err, ErrWorkerStalled)
		assert.NotErrorIs(t, err, ErrRateLimited)
	case <-time.After(5 * time.Second):
		t.Fatal("Run still blocked after 5s - stall watchdog never fired (the incident bug)")
	}
	assert.True(t, aborted.Load(),
		"the stalled server-side generation must be aborted, not left running")
}

// TestStallFallbackWhenServerIgnoresAbort: if the server is too far gone to
// honor even the abort (the POST stays pending), the watchdog's grace-period
// request cancel must still unblock the caller with ErrWorkerStalled - the
// brain can NEVER be left hanging on a dead worker.
func TestStallFallbackWhenServerIgnoresAbort(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/event", sseHandler("ses_dead", 0))
	mux.HandleFunc("/session/ses_dead/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`[]`)) // REST cross-check: static → genuinely dead
			return
		}
		<-r.Context().Done() // ignores the abort entirely
	})
	mux.HandleFunc("/session/ses_dead/abort", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`true`)) // acknowledged but has no effect
	})
	// Deliberately never closed: the dead POST's handler blocks on a request
	// context Go'll never cancel (body fully read → no background read → the
	// server can't see the peer go away), so srv.Close would wait forever.
	// Leaking one test server for the binary's lifetime is the lesser evil.
	srv := httptest.NewServer(mux)

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_dead", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      10 * time.Second,
		StallTimeout: 300 * time.Millisecond, // grace = stall timeout → ~600ms total
	}
	done := make(chan error, 1)
	go func() {
		_, err := d.Run(LegFree, "task")
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkerStalled)
	case <-time.After(5 * time.Second):
		t.Fatal("Run still blocked - the grace-period cancel fallback never fired")
	}
}

// TestActiveWorkerIsNotStalled: a turn whose session keeps emitting events
// (tool updates, deltas) must NOT be killed by the stall watchdog even when
// each individual wait exceeds StallTimeout - activity resets the clock.
func TestActiveWorkerIsNotStalled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/event", sseHandler("ses_busy", 50*time.Millisecond))
	mux.HandleFunc("/session/ses_busy/message", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(900 * time.Millisecond) // 3× the stall timeout, but events keep flowing
		w.Write([]byte(`{"info":{"tokens":{"total":7}},"parts":[{"type":"text","text":"PONG"}]}`))
	})
	mux.HandleFunc("/session/ses_busy/abort", func(w http.ResponseWriter, r *http.Request) {
		t.Error("abort called on a healthy, active worker")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_busy", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      10 * time.Second,
		StallTimeout: 300 * time.Millisecond,
	}
	res, err := d.Run(LegFree, "task")
	require.NoError(t, err, "an active worker must never be classified as stalled")
	assert.Equal(t, "PONG", res.Text)
}

// TestRunWorkerStreamRetriesOnceAfterStall: the self-healing half of the fix.
// When the first attempt stalls, RunWorkerStream must abort it and retry once
// on a FRESH session (the wedged session's context is poison) - exactly the
// manual recovery that fixed the live incident.
func TestRunWorkerStreamRetriesOnceAfterStall(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_STALL_TIMEOUT", "300ms")
	var sessions atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // the umbrella lookup: nothing left by an earlier brain
			w.Write([]byte(`[]`))
			return
		}
		n := sessions.Add(1)
		fmt.Fprintf(w, `{"id":"ses_%d"}`, n)
	})
	mux.HandleFunc("/event", sseHandler("ses_none", 0)) // no events for either session
	// ses_1 is the umbrella; the workers under it are ses_2 and ses_3.
	wedgedSession(mux, "ses_2", nil) // first attempt wedges until aborted
	mux.HandleFunc("/session/ses_3/message", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"info":{"tokens":{"total":3}},"parts":[{"type":"text","text":"RECOVERED"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	port, err := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	require.NoError(t, err)

	done := make(chan struct{})
	var res Result
	var runErr error
	go func() {
		res, runErr = RunWorkerStream(LegFree, "task", port, func(string) {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("RunWorkerStream still blocked - no stall detection or no retry")
	}
	require.NoError(t, runErr, "one stalled attempt must self-heal via retry, not surface an error")
	assert.Equal(t, "RECOVERED", res.Text)
	assert.Equal(t, int32(3), sessions.Load(), "retry must use a fresh session, not the wedged one")
}

func TestUnexpectedStatusIsSurfacedNotSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("boom"))
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	res, err := d.Run(LegFree, "reply with: PONG")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Empty(t, res.Text)
}

// TestProviderDownIsClassified: xAI outage 2026-07-19 - grok-4.5 returned
// "Service temporarily unavailable" in bursts. That's neither a rate limit
// (wrong cooldown length) nor a generic error (no cooldown at all): it needs
// its own class so routing can steer around a down provider briefly.
func TestProviderDownIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.Write([]byte(`{"info":{"error":{"name":"APIError","data":{"message":"unavailable: Service temporarily unavailable. The model did not respond to the request.","statusCode":503}},"tokens":{"total":0}},"parts":[]}`))
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	_, err := d.Run(LegGrok, "task")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderDown)
	assert.NotErrorIs(t, err, ErrRateLimited)
}

// TestWorkerSessionsArePinnedToWorkerDir reproduces the 2026-07-19 wrong-repo
// incident: the user switched projects (brain repinned to the new CAPTAIN_CWD)
// but the long-lived `opencode serve` kept its old cwd, so new worker sessions
// silently explored the PREVIOUS repo. Session creation must pass the caller's
// project via the ?directory= query param - the serve's own cwd is only a
// default, never an answer.
func TestWorkerSessionsArePinnedToWorkerDir(t *testing.T) {
	t.Setenv("CAPTAIN_CWD", "/src/CurrentProject")
	var gotDir atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/session" {
			gotDir.Store(r.URL.Query().Get("directory"))
			w.Write([]byte(`{"id":"ses_pinned"}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, Client: srv.Client(), Spawn: false}
	require.NoError(t, d.ensureSession())
	assert.Equal(t, "/src/CurrentProject", gotDir.Load(),
		"worker session must run in the CALLER's project, not wherever the serve happens to live")
}

// xAI's capacity error uses different wording than its 503s ("currently at
// capacity due to high demand", 2026-07-19) - same class, must classify the same.
func TestCapacityErrorIsProviderDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.Write([]byte(`{"info":{"error":{"name":"APIError","data":{"message":"The model is currently at capacity due to high demand. Please try again in a few minutes, or use a higher service tier for priority processing","statusCode":429}},"tokens":{"total":0}},"parts":[]}`))
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()
	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	_, err := d.Run(LegGrok, "task")
	assert.ErrorIs(t, err, ErrProviderDown, "capacity/high-demand = transient provider outage, short cooldown + reroute")
	assert.NotErrorIs(t, err, ErrRateLimited, "not the caller's quota - must not bench the leg for 30m")
}

// The claude leg runs headless (claude -p): permission prompts can't be
// answered, so un-preapproved tools (WebFetch - live 2026-07-26) get denied
// and the worker is silently handicapped. Workers get full trust by default
// (parity with cursor --trust and the opencode legs); env-tunable.
func TestClaudePermissionArgs(t *testing.T) {
	t.Setenv("CAPTAIN_CLAUDE_PERMISSIONS", "")
	assert.Equal(t, []string{"--dangerously-skip-permissions"}, claudePermissionArgs())
	t.Setenv("CAPTAIN_CLAUDE_PERMISSIONS", "default")
	assert.Empty(t, claudePermissionArgs(), "opt back into interactive-style denials")
	t.Setenv("CAPTAIN_CLAUDE_PERMISSIONS", "acceptEdits")
	assert.Equal(t, []string{"--permission-mode", "acceptEdits"}, claudePermissionArgs())
}

// /frontier: the best model, best version, highest effort - claude -p pinned
// to the frontier model id with a maxed extended-thinking budget.
func TestFrontierCmdConfig(t *testing.T) {
	t.Setenv("CAPTAIN_FRONTIER_MODEL", "")
	t.Setenv("CAPTAIN_FRONTIER_THINKING", "")
	args, env := frontierCmdConfig(EffortMax)
	// The ALIAS, not a pinned version: /frontier must follow the strongest
	// release without a code change each time one ships (2026-09-09).
	assert.Contains(t, strings.Join(args, " "), "--model fable")
	assert.Contains(t, strings.Join(args, " "), "--effort max", "/frontier is the ceiling (2026-09-13)")
	t.Setenv("CAPTAIN_FRONTIER_EFFORT", "xhigh")
	args, _ = frontierCmdConfig(EffortMax)
	assert.Contains(t, strings.Join(args, " "), "--effort xhigh", "a pinned effort wins")
	t.Setenv("CAPTAIN_FRONTIER_EFFORT", "")
	// Effort replaced the raw token budget (2026-09-09); the env var is now an
	// opt-in escape hatch, so the default carries no MAX_THINKING_TOKENS.
	assert.Empty(t, env, "no thinking-token env unless explicitly set")

	t.Setenv("CAPTAIN_FRONTIER_MODEL", "claude-opus-4-8")
	t.Setenv("CAPTAIN_FRONTIER_THINKING", "10000")
	args, env = frontierCmdConfig(EffortMax)
	assert.Contains(t, strings.Join(args, " "), "--model claude-opus-4-8")
	assert.Contains(t, env, "MAX_THINKING_TOKENS=10000")
}

// A worker that produces NOTHING is dead on arrival: waiting the full stall
// window (meant for quiet tool runs) twice cost 8 minutes before a dead xAI
// session was rerouted, on a "condense this to 200 words" task (2026-07-30).
func TestNeverStartedWorkerIsCaughtByTheFirstEventWindow(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_FIRST_EVENT_TIMEOUT", "200ms")
	var aborted atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/event", sseHandler("ses_silent", 0)) // connects, then NOTHING
	wedgedSession(mux, "ses_silent", &aborted)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_silent", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      20 * time.Second,
		StallTimeout: 10 * time.Second, // the normal window must NOT be what fires
	}
	start := time.Now()
	_, err := d.Run(LegFree, "task")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerStalled), "a silent session is a stall: %v", err)
	assert.Less(t, time.Since(start), 5*time.Second,
		"a worker that never emitted anything is diagnosed in the first-event window")
	assert.True(t, aborted.Load(), "and the server-side generation is aborted")
}

// A stream that dies mid-flight with NO tool running is dead, not busy: an xAI
// session emitted a little, went silent, and got the full 4-minute stall window
// before the retry that actually worked (live 2026-07-30, ~6 minutes wasted).
func TestQuietWorkerWithNoToolRunningUsesTheIdleWindow(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_IDLE_TIMEOUT", "200ms")
	var aborted atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		// The model starts generating, then the stream dies. No tool ever runs.
		fmt.Fprintf(w, "data: {\"type\":\"message.part.updated\",\"properties\":{\"sessionID\":\"ses_dead\",\"part\":{\"id\":\"p1\",\"type\":\"text\"}}}\n\n")
		fl.Flush()
		<-r.Context().Done()
	})
	wedgedSession(mux, "ses_dead", &aborted)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_dead", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      20 * time.Second,
		StallTimeout: 10 * time.Second, // the tool-run window must NOT be what fires
	}
	start := time.Now()
	_, err := d.Run(LegFree, "task")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerStalled))
	assert.Less(t, time.Since(start), 5*time.Second, "a dropped stream is caught by the idle window")
	assert.True(t, aborted.Load())
}

// …but a worker that is quiet BECAUSE a tool is running keeps the long window:
// a build or a test suite legitimately emits nothing for minutes.
func TestQuietWorkerWithAToolRunningKeepsTheStallWindow(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_IDLE_TIMEOUT", "200ms")
	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"type\":\"message.part.updated\",\"properties\":{\"sessionID\":\"ses_tool\",\"part\":{\"id\":\"t1\",\"type\":\"tool\",\"tool\":\"bash\",\"state\":{\"status\":\"running\",\"title\":\"npm test\"}}}}\n\n")
		fl.Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("/session/ses_tool/message", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond) // longer than the idle window, shorter than the stall window
		w.Write([]byte(`{"info":{"tokens":{"total":3}},"parts":[{"type":"text","text":"BUILD OK"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_tool", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      20 * time.Second,
		StallTimeout: 10 * time.Second,
	}
	res, err := d.Run(LegFree, "task")
	require.NoError(t, err, "a running tool must not be mistaken for a dead stream")
	assert.Equal(t, "BUILD OK", res.Text)
}

// The workflow and team paths pass onDelta=nil (worker text must not reach the
// answer). That silently disabled the event stream - and with it the stall
// watchdog, tool statuses and partial-output capture - so a dead xAI session sat
// 9 minutes untouched (live 2026-07-31). A stall timeout alone must arm it.
func TestStallDetectionWorksWithoutATextConsumer(t *testing.T) {
	var aborted atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/event", sseHandler("ses_nodelta", 0))
	wedgedSession(mux, "ses_nodelta", &aborted)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_nodelta", Client: srv.Client(), Spawn: false,
		OnDelta:      nil, // exactly how the workflow/team executor calls it
		Timeout:      20 * time.Second,
		StallTimeout: 300 * time.Millisecond,
	}
	start := time.Now()
	_, err := d.Run(LegFree, "task")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerStalled), "the watchdog must arm without a delta consumer: %v", err)
	assert.True(t, aborted.Load(), "and abort the wedged generation")
	assert.Less(t, time.Since(start), 5*time.Second)
}

// If the sensor cannot be connected at all, the run gets a short blind cap and
// is classified as a stall so the retry/reroute path still applies.
func TestBlindRunGetsAShortCapAndIsClassifiedAsAStall(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_BLIND_TIMEOUT", "300ms")
	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // no telemetry available
	})
	release := make(chan struct{})
	mux.HandleFunc("/session/ses_blind/message", func(w http.ResponseWriter, r *http.Request) {
		select { // never answers on its own
		case <-r.Context().Done():
		case <-release:
		}
	})
	mux.HandleFunc("/session/ses_blind/abort", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`true`)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release) // let the stuck handler go before Close waits on it

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_blind", Client: srv.Client(), Spawn: false,
		Timeout:      30 * time.Second,
		StallTimeout: 4 * time.Minute,
	}
	start := time.Now()
	_, err := d.Run(LegFree, "task")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerStalled), "a blind dead end must be retryable/reroutable: %v", err)
	assert.Less(t, time.Since(start), 5*time.Second, "blind runs are capped short, not at the 15m hard cap")
}

// 2026-08-07: the opencode /event bus died silently (server.connected, then
// nothing) while sessions kept WORKING - every "worker stalled" for a week was
// the watchdog killing healthy runs on a blind sensor. Before aborting, the
// watchdog must cross-check real progress over REST; a lying bus is logged,
// not obeyed.
func TestWatchdogCrossChecksRestBeforeAborting(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_FIRST_EVENT_TIMEOUT", "200ms")
	var aborted atomic.Bool
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/event", sseHandler("ses_busdead", 0)) // bus: connect, then silence forever
	done := make(chan struct{})
	mux.HandleFunc("/session/ses_busdead/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // REST progress poll: the session IS advancing
			n := polls.Add(1)
			fmt.Fprintf(w, `[{"info":{"role":"assistant"},"parts":[{"type":"text","text":"progress %d"}]}]`, n)
			return
		}
		select { // the worker "finishes" shortly after several polls
		case <-done:
		case <-time.After(1200 * time.Millisecond):
		}
		w.Write([]byte(`{"info":{"tokens":{"total":3}},"parts":[{"type":"text","text":"REAL ANSWER"}]}`))
	})
	mux.HandleFunc("/session/ses_busdead/abort", func(w http.ResponseWriter, r *http.Request) {
		aborted.Store(true)
		w.Write([]byte(`true`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(done)

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_busdead", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      20 * time.Second,
		StallTimeout: 400 * time.Millisecond,
	}
	res, err := d.Run(LegFree, "task")
	require.NoError(t, err, "a session progressing over REST must NOT be aborted on a silent bus")
	assert.Equal(t, "REAL ANSWER", res.Text)
	assert.False(t, aborted.Load())
	assert.GreaterOrEqual(t, polls.Load(), int32(1), "the watchdog consulted REST")
}

// …but a session with NO progress on either channel is genuinely wedged.
func TestWatchdogStillAbortsWhenRestShowsNoProgress(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_FIRST_EVENT_TIMEOUT", "200ms")
	var aborted atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/event", sseHandler("ses_truestall", 0))
	abortCh := make(chan struct{})
	mux.HandleFunc("/session/ses_truestall/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`[{"parts":[{"type":"text","text":"frozen"}]}]`)) // identical every time
			return
		}
		select {
		case <-abortCh:
			w.Write([]byte(abortedBody))
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/session/ses_truestall/abort", func(w http.ResponseWriter, r *http.Request) {
		aborted.Store(true)
		close(abortCh)
		w.Write([]byte(`true`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_truestall", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      20 * time.Second,
		StallTimeout: 400 * time.Millisecond,
	}
	start := time.Now()
	_, err := d.Run(LegFree, "task")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerStalled))
	assert.True(t, aborted.Load())
	assert.Less(t, time.Since(start), 8*time.Second)
}

// 2026-08-07 afternoon: the REST fingerprint hashed RAW session JSON - which
// carries ticking timestamps - so it "progressed" every poll and the watchdog
// never fired again: a failing stage crawled 45 minutes to the hard cap. The
// fingerprint must track CONTENT (part text), not volatile metadata.
func TestFingerprintIgnoresVolatileMetadata(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_FIRST_EVENT_TIMEOUT", "200ms")
	var aborted atomic.Bool
	var n atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/event", sseHandler("ses_ticker", 0))
	abortCh := make(chan struct{})
	mux.HandleFunc("/session/ses_ticker/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Same CONTENT every poll; only the timestamp ticks.
			fmt.Fprintf(w, `[{"time":{"updated":%d},"parts":[{"type":"text","text":"frozen output"}]}]`, n.Add(1))
			return
		}
		select {
		case <-abortCh:
			w.Write([]byte(abortedBody))
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/session/ses_ticker/abort", func(w http.ResponseWriter, r *http.Request) {
		aborted.Store(true)
		close(abortCh)
		w.Write([]byte(`true`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_ticker", Client: srv.Client(), Spawn: false,
		OnDelta: func(string) {}, Timeout: 20 * time.Second, StallTimeout: 400 * time.Millisecond,
	}
	start := time.Now()
	_, err := d.Run(LegFree, "task")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerStalled), "ticking timestamps are not progress: %v", err)
	assert.True(t, aborted.Load())
	assert.Less(t, time.Since(start), 8*time.Second, "the watchdog must not be neutered by volatile JSON")
}

// The opencode zen free-model catalog ROTATES (live 2026-08-24:
// deepseek-v4-flash-free retired → every free-leg call died with an opaque
// HTTP 500). The default must be a currently-served model and ops must be
// able to repoint it without recompiling when the next rotation hits.
func TestFreeLegModel_DefaultAndEnvOverride(t *testing.T) {
	t.Setenv("CAPTAIN_GLM_MODEL", "")
	t.Setenv("CAPTAIN_GLM_PROVIDER", "")
	LoadRegistry(filepath.Join(t.TempDir(), "none.json"))
	def := legModels[LegFree]
	assert.Equal(t, "opencode", def.Provider)
	assert.Equal(t, "nemotron-3.5-lightning-free", def.Model, "default must track the live zen catalog - rotated twice in a week")
	assert.NotEqual(t, "deepseek-v4-flash-free", def.Model, "retired 2026-08")

	// NIM retired its entire GLM line (HTTP 410, 2026-08-24) - the glm leg's
	// default must not point at a provider that serves no GLM.
	assert.Equal(t, "openrouter", legModels[LegGLM].Provider)
	assert.Equal(t, "z-ai/glm-5.3", legModels[LegGLM].Model, "same model id as NIM served - scorecard continuity")

	t.Setenv("CAPTAIN_FREE_PROVIDER", "nim")
	t.Setenv("CAPTAIN_FREE_MODEL", "some/other-model")
	defer func() { legModels[LegFree] = def }()
	applyLegModelEnv()
	assert.Equal(t, "nim", legModels[LegFree].Provider)
	assert.Equal(t, "some/other-model", legModels[LegFree].Model)
}

// The 2026-08-24 legs. Provider policy: NIM first when the model invokes
// there (free with the NVIDIA key) - kimi-k3 does; OpenRouter (paid) for the
// rest (deepseek/gemini/qwen/glm are absent or dead on NIM, invoke-probed).
func TestOpenRouterLegs(t *testing.T) {
	for leg, want := range map[Leg]struct{ Provider, Model string }{
		LegDeepSeek: {"openrouter", "deepseek/deepseek-v4-pro"},
		LegGemini:   {"openrouter", "google/gemini-3.7-flash"},
		LegKimi:     {"nim", "moonshotai/kimi-k3"},
	} {
		assert.True(t, KnownLeg(leg))
		assert.Equal(t, want.Provider, legModels[leg].Provider)
		assert.Equal(t, want.Model, legModels[leg].Model)
		_, ok := qualityPriorDefaults[leg]
		assert.True(t, ok, "%s needs a cold-start prior or routing treats it as quality 0", leg)
	}
	assert.True(t, defaultVisionLegs()[LegGemini], "gemini is multimodal - vision tasks must be able to route to it")
	assert.False(t, defaultVisionLegs()[LegDeepSeek])
}

// A refusal is not a successful run: the leg is healthy but its POLICY does
// not fit the work, so the task must move to a provider whose does (live
// 2026-09-09 - routine crypto-backend engineering in the user's own repo).
func TestRefusalIsAFailureNotAnAnswer(t *testing.T) {
	refusals := []string{
		"I'm sorry, but I can't help with that.",
		"I can't assist with creating this.",
		"Sorry, I cannot provide that.",
		"I must decline this request.",
		"I'm unable to assist with this task.",
	}
	for _, r := range refusals {
		assert.True(t, IsRefusal(r), "must be a refusal: %q", r)
		_, err := emptyIsFailure(LegCodex, Result{Text: r}, nil)
		assert.ErrorIs(t, err, ErrRefused, "%q", r)
	}

	// Real work must never be mistaken for a refusal, including work that
	// TALKS about refusals or reports an inability to do something specific.
	notRefusals := []string{
		"I can't reproduce the bug locally; here is what I found instead: the config loader …",
		"Done. I refactored the settlement path and added three tests.",
		"The provider refused the request, so I switched to the fixture path and the suite passes.",
		"I'm sorry the previous patch broke the build - fixed in commit abc123, tests green now, and I also " + strings.Repeat("added coverage for the edge cases. ", 40),
	}
	for _, r := range notRefusals {
		assert.False(t, IsRefusal(r), "must NOT be a refusal: %q", r[:60])
	}
	assert.False(t, IsRefusal(""), "empty output is its own error class")
}

// Before the first token, REST only shows our own user message being saved;
// that is not the model working. The first-event window must fire, and the
// caller must not retry on a fresh session (kimi on a dead NIM waited 8
// minutes for a simple prompt, 2026-09-18).
func TestFirstEventStallIsNotMaskedByTheSavedUserMessage(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_FIRST_EVENT_TIMEOUT", "1s")
	mux := http.NewServeMux()
	n := 0
	aborted := make(chan struct{})
	mux.HandleFunc("/session/ses_dead/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			n++ // the user message, "saved" a little more each poll: still no assistant part
			fmt.Fprintf(w, `[{"info":{"role":"user"},"parts":[{"type":"text","text":"the task v%d"}]}]`, n)
			return
		}
		select { // the model never answers; the abort finalizes the turn, as opencode does
		case <-aborted:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"name":"MessageAbortedError","data":{}}`))
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	mux.HandleFunc("/session/ses_dead/abort", func(w http.ResponseWriter, r *http.Request) { close(aborted); w.Write([]byte(`true`)) })
	mux.HandleFunc("/event", sseHandler("ses_dead", 0)) // bus: connect, then silence forever
	mux.HandleFunc("/config/providers", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"providers":[{"id":"nim"}]}`)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_dead", Client: srv.Client(), Spawn: false,
		StallTimeout: 8 * time.Second, Timeout: 20 * time.Second, OnStatus: func(string) {}}
	start := time.Now()
	_, err := d.Run(LegKimi, "the task")
	require.ErrorIs(t, err, ErrWorkerStalled)
	assert.Contains(t, err.Error(), "never started generating")
	assert.Less(t, time.Since(start), 5*time.Second, "the first-event window (1s + poll), not the 8s stall window")
}
