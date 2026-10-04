package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func TestCmdEuclidSearchAndStats(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "retrieval.jsonl")
	t.Setenv("CAPTAIN_RETRIEVAL_LOG", logPath)

	// Seed one event
	captaincode.LogRetrievalEvent(captaincode.RetrievalEvent{
		Tool:           "code_search",
		Query:          "testQuery",
		Lane:           "code",
		DurationMS:     3.2,
		HitsCount:      2,
		TokensReturned: 80,
		TokensAvoided:  2400,
		Safe:           true,
		CodeIntelUsed:  true,
	})

	// Test stats output
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmdEuclidStats([]string{"--days", "7"})

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()

	assert.Contains(t, out, "Euclid & CodeIntel Retrieval Efficiency")
	assert.Contains(t, out, "1 total")
	assert.Contains(t, out, "100.0% safe containment")
	assert.Contains(t, out, "100.0% CodeIntel AST coverage")
}

func TestCmdEuclidCodeSearchInvocation(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "retrieval.jsonl")
	t.Setenv("CAPTAIN_RETRIEVAL_LOG", logPath)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Use a real engine process. An unavailable engine now returns an error.
	require.NoError(t, exec.Command("git", "init", "-q", tmp).Run())
	bin := filepath.Join(tmp, ".euclid", "bin")
	require.NoError(t, os.MkdirAll(bin, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "search.py"), []byte("print('FooBarSymbol: src/foo.go')"), 0600))
	t.Setenv("CAPTAIN_CWD", tmp)
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", "")
	cmdEuclidCode([]string{"FooBarSymbol"})

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()

	assert.NotEmpty(t, out)

	// Verify an event was logged
	events, err := captaincode.ReadRetrievalEvents(logPath, time.Time{})
	require.NoError(t, err)
	assert.True(t, len(events) >= 1)
	assert.Equal(t, "code", events[len(events)-1].Lane)
}
