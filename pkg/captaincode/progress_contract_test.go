package captaincode

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The progress contract (2026-09-12): a worker is cut only when it is QUIET -
// silent past the window its state allows. A running tool is not quiet (a
// build, a test suite, a CI watch print nothing for minutes), and there is no
// wall-clock ceiling by default: a productive 31-minute codex-cli run was cut
// mid-task with "partial output", and `cargo test --release` was aborted at
// the 4-minute stall window with "User aborted the command".

func TestProgressQuietDependsOnToolState(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT", "50ms")
	t.Setenv("CAPTAIN_WORKER_CLI_TOOL_TIMEOUT", "1h")
	p := &progress{}
	p.touch()
	assert.False(t, p.quiet(), "just touched")
	time.Sleep(80 * time.Millisecond)
	assert.True(t, p.quiet(), "silent past the idle window with no tool running")

	p.toolStart()
	time.Sleep(80 * time.Millisecond)
	assert.False(t, p.quiet(), "silent WITH a tool in flight is work, not a stall")
	p.toolEnd()
	time.Sleep(80 * time.Millisecond)
	assert.True(t, p.quiet(), "the tool finished and nothing followed")
}

func TestProgressCtxNoCeilingByDefault(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT", "50ms")
	t.Setenv("CAPTAIN_WORKER_CLI_TOOL_TIMEOUT", "1h")
	t.Setenv("CAPTAIN_WORKER_CEILING", "")
	assert.Equal(t, time.Duration(0), workerCeiling(), "no absolute stop unless an operator sets one")

	ctx, cancel, p, capped := progressCtx(40*time.Millisecond, workerCeiling())
	defer cancel()
	p.toolStart() // a long tool: past base, still not quiet
	select {
	case <-ctx.Done():
		t.Fatal("a run with a tool in flight must not be cut past its base cap")
	case <-time.After(250 * time.Millisecond):
	}
	assert.False(t, capped.Load())
	p.toolEnd()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("quiet past base with no tool running must end the run")
	}
	assert.True(t, capped.Load())
}

func TestProgressCtxCeilingStillCutsWhenSet(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT", "1h")
	ctx, cancel, p, capped := progressCtx(20*time.Millisecond, 120*time.Millisecond)
	defer cancel()
	p.toolStart()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an explicit ceiling is an absolute stop")
	}
	assert.True(t, capped.Load())
}

// A provider rejecting the leg's key is a provider fault the task must route
// around - kimi's NIM 403 ended the turn instead (live 2026-09-12).
func TestCredentialRejectionReroutesAndNamesTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_1/message" {
			w.Write([]byte(`{"info":{"error":{"name":"APIError","data":{"message":"Forbidden: {\"status\":403,\"title\":\"Forbidden\",\"detail\":\"Authorization failed\"}","statusCode":403,"isRetryable":false,"metadata":{"url":"https://integrate.api.nvidia.com/v1/chat/completions"}}},"tokens":{"total":0}},"parts":[]}`))
			return
		}
		w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer srv.Close()

	d := &OpencodeDispatcher{BaseURL: srv.URL, SessionID: "ses_1", Client: srv.Client(), Spawn: false}
	_, err := d.Run(LegKimi, "task")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrProviderAuth), "a 403 is a rejected key")
	assert.True(t, errors.Is(err, ErrProviderDown), "…and a provider fault, so the brain reroutes")
	assert.NotErrorIs(t, err, ErrRateLimited)
	assert.Contains(t, err.Error(), "check the provider key")
	assert.True(t, harnessFault(err.Error()), "the operator's key, not the model: off the reliability stats")
}

// …and it outlives the STALL window too, as long as the tool is still
// running: `cargo test --release` (5-minute tool timeout) was aborted at the
// 4-minute stall window. opencode bounds the tool; toolRunTimeout is the
// only cap while it runs.
func TestRunningToolOutlivesTheStallWindow(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_IDLE_TIMEOUT", "100ms")
	t.Setenv("CAPTAIN_WORKER_TOOL_TIMEOUT", "10s")
	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		w.Write([]byte("data: {\"type\":\"message.part.updated\",\"properties\":{\"sessionID\":\"ses_cargo\",\"part\":{\"id\":\"t1\",\"type\":\"tool\",\"tool\":\"bash\",\"state\":{\"status\":\"running\",\"title\":\"cargo test --release\"}}}}\n\n"))
		fl.Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("/session/ses_cargo/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // the REST cross-check sees the same running tool every time
			w.Write([]byte(`[{"parts":[{"type":"tool","tool":"bash","state":{"status":"running"}}]}]`))
			return
		}
		time.Sleep(1200 * time.Millisecond) // well past the stall window below
		w.Write([]byte(`{"info":{"tokens":{"total":3}},"parts":[{"type":"text","text":"test result: ok"}]}`))
	})
	mux.HandleFunc("/session/ses_cargo/abort", func(w http.ResponseWriter, r *http.Request) {
		t.Error("a running tool must never be aborted by the stall watchdog")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_cargo", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      20 * time.Second,
		StallTimeout: 300 * time.Millisecond,
	}
	res, err := d.Run(LegFree, "task")
	require.NoError(t, err)
	assert.Equal(t, "test result: ok", res.Text)
}

// A tool "running" past what opencode itself allows is wedged, not slow: the
// tool window still ends it.
func TestWedgedToolStillStallsAtTheToolWindow(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_IDLE_TIMEOUT", "100ms")
	t.Setenv("CAPTAIN_WORKER_TOOL_TIMEOUT", "400ms")
	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		w.Write([]byte("data: {\"type\":\"message.part.updated\",\"properties\":{\"sessionID\":\"ses_wedge\",\"part\":{\"id\":\"t1\",\"type\":\"tool\",\"tool\":\"bash\",\"state\":{\"status\":\"running\",\"title\":\"hung\"}}}}\n\n"))
		fl.Flush()
		<-r.Context().Done()
	})
	var aborted atomic.Bool
	wedgedSession(mux, "ses_wedge", &aborted)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_wedge", Client: srv.Client(), Spawn: false,
		OnDelta:      func(string) {},
		Timeout:      20 * time.Second,
		StallTimeout: 200 * time.Millisecond,
	}
	start := time.Now()
	_, err := d.Run(LegFree, "task")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerStalled))
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.True(t, aborted.Load())
}

// The serve's /event bus only carries its own instance's sessions; a worker
// pinned to another folder was invisible on it. The dispatcher reads
// /global/event (every folder, payload-wrapped) and falls back to /event.
func TestDispatcherReadsTheGlobalEventBus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/global/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		w.Write([]byte(`data: {"payload":{"type":"server.connected","properties":{}}}` + "\n\n"))
		w.Write([]byte(`data: {"directory":"/src/arc","project":"p1","payload":{"type":"message.part.updated","properties":{"sessionID":"ses_arc","part":{"id":"t1","type":"tool","tool":"bash","state":{"status":"running","title":"cargo test"}}}}}` + "\n\n"))
		w.Write([]byte(`data: {"directory":"/src/arc","project":"p1","payload":{"type":"message.part.delta","properties":{"sessionID":"ses_arc","partID":"p2","field":"text","delta":"hello from arc"}}}` + "\n\n"))
		fl.Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		t.Error("with a global bus available the instance bus must not be used")
	})
	mux.HandleFunc("/session/ses_arc/message", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte(`{"info":{"tokens":{"total":3}},"parts":[{"type":"text","text":"hello from arc"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var deltas, statuses []string
	d := &OpencodeDispatcher{
		BaseURL: srv.URL, SessionID: "ses_arc", Client: srv.Client(), Spawn: false,
		OnDelta:      func(s string) { deltas = append(deltas, s) },
		OnStatus:     func(s string) { statuses = append(statuses, s) },
		Timeout:      20 * time.Second,
		StallTimeout: 10 * time.Second,
	}
	res, err := d.Run(LegFree, "task")
	require.NoError(t, err)
	assert.Equal(t, "hello from arc", res.Text)
	assert.True(t, res.Streamed, "the bus connected")
	assert.Contains(t, strings.Join(deltas, ""), "hello from arc", "deltas of a session in another folder arrive")
	assert.NotEmpty(t, statuses, "…and so do its tool statuses")
}

// A reasoning CLI leg is silent while it thinks: past the base cap a codex-cli
// run was cut 91s after its last tool call, mid-thought (2026-09-12). The CLI
// legs' idle window is long by default; the opencode dispatcher keeps its own.
func TestCLILegsHaveLongQuietWindowsByDefault(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT", "")
	t.Setenv("CAPTAIN_WORKER_CLI_TOOL_TIMEOUT", "")
	t.Setenv("CAPTAIN_WORKER_IDLE_TIMEOUT", "50ms")
	assert.GreaterOrEqual(t, cliIdleTimeout(), 20*time.Minute, "minutes of invisible reasoning are not a stall")
	assert.GreaterOrEqual(t, cliToolTimeout(), time.Hour, "a CLI's tool is bounded by the CLI, not by opencode's 10m bash cap")
	p := &progress{}
	p.touch()
	time.Sleep(80 * time.Millisecond)
	assert.False(t, p.quiet(), "the opencode idle window does not apply to a CLI leg")
}
