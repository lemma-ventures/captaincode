package main

import (
	"net"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A brain that cannot take the proxy port must not run without it: every
// opencode call then went to a dead port (2026-10-03). It waits for a second
// brain to let go of the port, and fails when nobody does.
func TestTheProxyWaitsForItsPortThenRefusesToRunWithout(t *testing.T) {
	prev := proxyListenWait
	proxyListenWait = 600 * time.Millisecond
	defer func() { proxyListenWait = prev }()
	defer captaincode.SetProxyBase("")

	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Setenv("CAPTAIN_PROXY_ADDR", held.Addr().String())
	t.Setenv("CAPTAIN_REDACT", "on")

	start := time.Now()
	err = startProxy()
	require.Error(t, err, "a port nobody frees is an error, not a silent brain without a proxy")
	assert.GreaterOrEqual(t, time.Since(start), 500*time.Millisecond, "it retried first")

	go func() { time.Sleep(200 * time.Millisecond); held.Close() }()
	require.NoError(t, startProxy(), "a port freed while it waits is taken")
	assert.Equal(t, "http://"+held.Addr().String(), captaincode.ProxyBase())
}
