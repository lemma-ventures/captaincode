package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The supervisor watch in the brain. Its first duty is to be nothing at all
// when there is no decision leg: one nil check per worker run, no goroutine,
// no ticker, and a status callback that is handed back unchanged.

func TestSuperviseWatchIsNilAndTransparentWithoutADecisionLeg(t *testing.T) {
	b := &brain{}
	w := b.superviseStart(captaincode.Workspace{Dir: t.TempDir()}, captaincode.LegClaude, "do the thing")
	require.Nil(t, w)

	called := 0
	inner := func(string) { called++ }
	assert.NotNil(t, w.wrap(inner))
	w.wrap(inner)("reading main.go")
	assert.Equal(t, 1, called, "the status callback is passed straight through")

	assert.NotPanics(t, func() {
		w.status("x")
		w.close(captaincode.Result{}, nil)
	}, "a nil watch is a working watch that does nothing")

	assert.Nil(t, w.wrap(nil), "and it does not invent a callback where there was none")
}

func TestSuperviseWatchKeepsTheActionTailAndTeesTheCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	b := &brain{jev: &captaincode.SystemOneClient{BaseURL: srv.URL, APIKey: "k"}}
	t.Setenv(captaincode.SuperviseEveryEnv, "1h") // no sample fires during the test

	w := b.superviseStart(captaincode.Workspace{Dir: t.TempDir()}, captaincode.LegClaude, "fix the failing test")
	require.NotNil(t, w)
	t.Cleanup(func() { w.close(captaincode.Result{}, nil) })

	seen := ""
	w.wrap(func(s string) { seen = s })("bash cargo build --release")
	assert.Equal(t, "bash cargo build --release", seen, "the TUI still sees every status")

	snap := w.snapshot()
	assert.Equal(t, captaincode.LegClaude, snap.Leg)
	assert.Contains(t, snap.Recent, "bash cargo build --release")
	assert.Less(t, snap.Quiet, time.Second, "a status just arrived, so the worker is not quiet")
}

// The drift question is asked against the repository's own guidance, or not
// asked at all.
func TestRepoGuidanceReadsAgentsMdAndIsAbsentWithoutOne(t *testing.T) {
	dir := t.TempDir()
	assert.Empty(t, repoGuidance(dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("run go test ./... before finishing"), 0o644))
	assert.Contains(t, repoGuidance(dir), "run go test ./...")
}
