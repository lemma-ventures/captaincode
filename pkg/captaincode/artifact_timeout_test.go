package captaincode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The deadline must kill the whole process group. `sh -c "cargo test"` leaves
// cargo and every rustc holding the output pipe, so CombinedOutput kept
// reading long after the shell was killed: a 5-minute budget took 22 minutes
// to return, and the caller then read "failed" (live 2026-09-21, lemma).
func TestATimedOutCheckIsKilledWithItsChildrenAndReportsNoVerdict(t *testing.T) {
	outsideEvidenceRun(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "captain-test-marker"), []byte("x"), 0o644))
	old := testCommandByFile
	// A child that outlives the shell and keeps the pipe open - the shape of
	// cargo under sh -c.
	testCommandByFile = []struct {
		file    string
		command string
	}{{"captain-test-marker", "sleep 30 & sleep 30"}}
	defer func() { testCommandByFile = old }()
	t.Setenv("CAPTAIN_VERIFY_TIMEOUT", "1s")
	assert.Equal(t, time.Second, TestEvidenceTimeout())

	start := time.Now()
	ce, err := CaptureTestEvidence(context.Background(), dir)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotNil(t, ce)
	assert.Less(t, elapsed, 15*time.Second, "the budget must be real: the group is killed, not only the shell")
	assert.True(t, ce.TimedOut, "a killed command produced no verdict")
	assert.False(t, ce.Passed)
	assert.Contains(t, ce.Output, "no verdict")
}

// A check that finishes inside the budget is an ordinary verdict, either way.
func TestACheckThatFinishesIsNotMarkedTimedOut(t *testing.T) {
	outsideEvidenceRun(t)
	old := testCommandByFile
	defer func() { testCommandByFile = old }()
	t.Setenv("CAPTAIN_VERIFY_TIMEOUT", "30s")
	for _, tc := range []struct {
		cmd    string
		passed bool
	}{{"true", true}, {"echo nope; exit 3", false}} {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "captain-test-marker"), []byte("x"), 0o644))
		testCommandByFile = []struct {
			file    string
			command string
		}{{"captain-test-marker", tc.cmd}}
		ce, err := CaptureTestEvidence(context.Background(), dir)
		require.NoError(t, err)
		require.NotNil(t, ce)
		assert.Equal(t, tc.passed, ce.Passed, tc.cmd)
		assert.False(t, ce.TimedOut, tc.cmd)
		assert.Greater(t, ce.Duration, time.Duration(0))
	}
}

// The default budget stands when the env names nothing usable.
func TestVerifyTimeoutDefaults(t *testing.T) {
	t.Setenv("CAPTAIN_VERIFY_TIMEOUT", "")
	assert.Equal(t, 5*time.Minute, TestEvidenceTimeout())
	t.Setenv("CAPTAIN_VERIFY_TIMEOUT", "nonsense")
	assert.Equal(t, 5*time.Minute, TestEvidenceTimeout())
	t.Setenv("CAPTAIN_VERIFY_TIMEOUT", "90s")
	assert.Equal(t, 90*time.Second, TestEvidenceTimeout())
}
