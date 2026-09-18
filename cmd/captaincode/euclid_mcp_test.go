package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"resources/list"}`,
	}
	var out bytes.Buffer
	calls := 0
	serveEuclidMCP(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, func() string { calls++; return home })
	assert.Equal(t, 5, calls, "cwd is resolved per tool call - the server follows project switches")

	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		replies = append(replies, m)
	}
	require.Len(t, replies, 8, "one reply per request, none for the notification")
	assert.Equal(t, mcpProtocolVersion, replies[0]["result"].(map[string]any)["protocolVersion"])
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	names := []string{}
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	assert.ElementsMatch(t, []string{"search", "ask", "recall", "note", "read_register", "recent_runs", "status"}, names)
	text := func(i int) string {
		return replies[i]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	assert.Contains(t, text(2), "[main] WISDOM.md")
	assert.Contains(t, text(2), "atomically")
	assert.Contains(t, text(3), "wire value routing")
	assert.Contains(t, text(3), "pkg/value.go")
	assert.Contains(t, text(4), "[main] wisdom")
	assert.Contains(t, text(5), "write")
	assert.Equal(t, true, replies[6]["result"].(map[string]any)["isError"], "unknown tool → tool error, not a protocol error")
	assert.NotNil(t, replies[7]["error"], "unknown method → JSON-RPC error")
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
