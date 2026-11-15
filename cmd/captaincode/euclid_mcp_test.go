package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The MCP server: newline-delimited JSON-RPC, initialize → tools/list →
// tools/call; multi-root by asking for the cwd on every call; read-only.
func TestEuclidMCPServer(t *testing.T) {
	home := euclidTestHome(t)
	root := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(root, "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "WISDOM.md"), []byte("# WISDOM\n\nDeploy the brain atomically with mv.\n"), 0o644))
	_, err = captaincode.JournalRun(home, captaincode.JournalEntry{Kind: "worker", Task: "wire value routing", Leg: "gemini", Outcome: "ok", Files: []string{"pkg/value.go"}})
	require.NoError(t, err)

	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search","arguments":{"query":"deploy atomically"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"recent_runs","arguments":{"limit":5}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read_register","arguments":{"name":"wisdom"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"code_search","arguments":{"query":"deploy"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"file_search","arguments":{"query":"value"}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"resources/list"}`,
	}
	var out bytes.Buffer
	calls := 0
	serveEuclidMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, func() string { calls++; return home })
	assert.Equal(t, 7, calls, "cwd is resolved per tool call - the server follows project switches")

	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		replies = append(replies, m)
	}
	require.Len(t, replies, 10, "one reply per request, none for the notification")
	assert.Equal(t, mcpProtocolVersion, replies[0]["result"].(map[string]any)["protocolVersion"])
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	names := []string{}
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	assert.ElementsMatch(t, []string{"search", "code_search", "file_search", "ask", "recall", "note", "read_register", "recent_runs", "status"}, names)
	text := func(i int) string {
		return replies[i]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	assert.Equal(t, true, replies[2]["result"].(map[string]any)["isError"])
	assert.Contains(t, text(3), "wire value routing")
	assert.Contains(t, text(3), "pkg/value.go")
	assert.Contains(t, text(4), "[main] wisdom")
	assert.Contains(t, text(5), "write")
	assert.Equal(t, true, replies[6]["result"].(map[string]any)["isError"])
	assert.Equal(t, true, replies[7]["result"].(map[string]any)["isError"])
	assert.Equal(t, true, replies[8]["result"].(map[string]any)["isError"], "unknown tool → tool error, not a protocol error")
	assert.NotNil(t, replies[9]["error"], "unknown method → JSON-RPC error")
}

func TestEuclidMCPExitsWhenFolderRemoved(t *testing.T) {
	life := testMCPLifetime(t)
	life.stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	assertMCPStops(t, life)
}

func TestEuclidMCPExitsWhenParentChanges(t *testing.T) {
	life := testMCPLifetime(t)
	life.parent = 10
	life.ppid = func() int { return 1 }
	assertMCPStops(t, life)
}

func TestEuclidMCPIdleOffByDefault(t *testing.T) {
	t.Setenv("CAPTAIN_EUCLID_MCP_IDLE", "")
	life := newMCPLifetime(t.TempDir())
	assert.Zero(t, life.idle)
	now := time.Now()
	assert.False(t, life.shouldExit(now.Add(365*24*time.Hour), now))
}

func TestEuclidMCPIdleExit(t *testing.T) {
	t.Setenv("CAPTAIN_EUCLID_MCP_IDLE", "5ms")
	life := newMCPLifetime(t.TempDir())
	life.interval = time.Millisecond
	assertMCPStops(t, life)
}

func TestEuclidMCPRequestFinishesBeforeLifecycleExit(t *testing.T) {
	life := testMCPLifetime(t)
	var parent atomic.Int32
	parent.Store(1)
	life.ppid = func() int { return int(parent.Load()) }
	started := make(chan struct{})
	release := make(chan struct{})
	in, writer := io.Pipe()
	defer writer.Close()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		serveEuclidMCP(in, &out, func() string {
			close(started)
			<-release
			return life.dir
		}, life)
		close(done)
	}()
	_, err := writer.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"status\",\"arguments\":{}}}\n"))
	require.NoError(t, err)
	<-started
	parent.Store(2)
	time.Sleep(10 * time.Millisecond)
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("MCP server did not stop after the request")
	}
	assert.Contains(t, out.String(), "No Euclid brain")
}

func testMCPLifetime(t *testing.T) *mcpLifetime {
	t.Helper()
	return &mcpLifetime{
		dir:      t.TempDir(),
		parent:   1,
		interval: time.Millisecond,
		now:      time.Now,
		ppid:     func() int { return 1 },
		stat: func(string) (os.FileInfo, error) {
			return nil, nil
		},
	}
}

func assertMCPStops(t *testing.T, life *mcpLifetime) {
	t.Helper()
	in, writer := io.Pipe()
	defer writer.Close()
	done := make(chan struct{})
	go func() {
		serveEuclidMCP(in, io.Discard, func() string { return life.dir }, life)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("MCP server did not stop")
	}
}

func BenchmarkMCPLifetimeCheck(b *testing.B) {
	life := &mcpLifetime{
		dir:    b.TempDir(),
		parent: 1,
		now:    time.Now,
		ppid:   func() int { return 1 },
		stat:   func(string) (os.FileInfo, error) { return nil, nil },
	}
	last := time.Now()
	for b.Loop() {
		life.shouldExit(last, last)
	}
}

func TestEnsureEuclidMCPRegistersOnce(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(cfg, []byte(`{"provider":{}}`), 0o600))
	changed, err := captaincode.EnsureEuclidMCP(cfg, "/usr/local/bin/captain")
	require.NoError(t, err)
	assert.True(t, changed)
	raw, _ := os.ReadFile(cfg)
	assert.Contains(t, string(raw), `"euclid"`)
	assert.Contains(t, string(raw), `"/usr/local/bin/captain"`)
	assert.Contains(t, string(raw), `"local"`)
	changed, err = captaincode.EnsureEuclidMCP(cfg, "/elsewhere/captain")
	require.NoError(t, err)
	assert.False(t, changed, "an existing entry is never rewritten")
}

func TestEuclidMCPSearchSourceOptions(t *testing.T) {
	for _, name := range []string{"search", "file_search"} {
		for _, args := range []map[string]any{
			{"query": "q", "include_journals": "false"},
			{"query": "q", "scope": "unknown"},
			{"query": "q", "scope": false},
			{"query": "q", "lane": "invalid"},
		} {
			_, err := euclidToolCall(name, args, t.TempDir())
			require.Error(t, err)
		}
		for _, tool := range euclidTools {
			if tool["name"] != name {
				continue
			}
			props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
			require.Equal(t, false, props["include_journals"].(map[string]any)["default"])
			require.Contains(t, props, "scope")
		}
	}
}
