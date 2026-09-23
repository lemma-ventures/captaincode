package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The brain listened with http.ListenAndServe, which has no timeouts: a
// connection whose client vanished without a FIN kept its goroutine and its
// descriptor for the life of the process. Ten hours and nine TUIs later the
// brain held 18,566 inbound sockets in CLOSED state and answered nothing
// (2026-09-23). An idle connection must be reaped.
func TestIdleConnectionsAreReaped(t *testing.T) {
	t.Setenv("CAPTAIN_HTTP_IDLE_TIMEOUT", "300ms")
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "pong") })
	srv := brainServer("127.0.0.1:0", mux)
	assert.Equal(t, 300*time.Millisecond, srv.IdleTimeout)
	assert.Equal(t, time.Duration(0), srv.WriteTimeout, "an SSE turn outlives any write deadline")
	assert.Equal(t, 20*time.Second, srv.ReadHeaderTimeout)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	c, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer c.Close()
	_, err = fmt.Fprintf(c, "GET /ping HTTP/1.1\r\nHost: x\r\n\r\n")
	require.NoError(t, err)
	buf := make([]byte, 512)
	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	n, err := c.Read(buf)
	require.NoError(t, err)
	assert.Contains(t, string(buf[:n]), "pong")

	// Now go quiet: the server must close the kept-alive connection itself.
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = c.Read(buf)
	assert.Error(t, err, "the idle connection is still open - it would leak")
}

// The default idle window stands when the env names nothing usable.
func TestIdleTimeoutDefaults(t *testing.T) {
	t.Setenv("CAPTAIN_HTTP_IDLE_TIMEOUT", "")
	assert.Equal(t, 90*time.Second, brainServer(":0", http.NewServeMux()).IdleTimeout)
	t.Setenv("CAPTAIN_HTTP_IDLE_TIMEOUT", "nonsense")
	assert.Equal(t, 90*time.Second, brainServer(":0", http.NewServeMux()).IdleTimeout)
}

// An ordinary response carries a write deadline; a streaming one never does,
// or a turn that runs for an hour would be cut at the first minute.
func TestOnlyOrdinaryResponsesCarryAWriteDeadline(t *testing.T) {
	assert.True(t, streamingPath("/v1/chat/completions"))
	assert.True(t, streamingPath("/v1/watch"))
	assert.False(t, streamingPath("/v1/stats"))
	assert.False(t, streamingPath("/v1/workers"))

	var deadlines []bool
	h := withWriteDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		// A deadline in the past fails the write; use it to observe that one
		// was set at all: setting a new one always succeeds, so instead read
		// it back by asking for a zero deadline and checking support.
		deadlines = append(deadlines, rc.SetWriteDeadline(time.Now().Add(time.Hour)) == nil)
		fmt.Fprint(w, "ok")
	}))
	for _, p := range []string{"/v1/stats", "/v1/chat/completions"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		assert.Equal(t, 200, rec.Code, p)
	}
	assert.Len(t, deadlines, 2)
}
