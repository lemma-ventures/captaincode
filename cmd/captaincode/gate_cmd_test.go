package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `captain gate`. The property that matters for everyone who does not have a
// decision leg: the gate is not a thing that happens to them.

func TestGateStatusSaysNothingIsScreenedWithoutADecisionLeg(t *testing.T) {
	t.Setenv(captaincode.SystemOneKeyEnv, "")
	t.Setenv(captaincode.SystemOneURLEnv, "")
	t.Setenv("CAPTAIN_SRC", t.TempDir())
	t.Chdir(t.TempDir())
	out := captureStdout(t, func() { cmdGate([]string{"--status"}) })
	assert.Contains(t, out, "decision leg: none")
	assert.Contains(t, out, "nothing is screened")
	assert.Contains(t, out, "no tool call waits on one")
}

// The hook contract: silence allows. A gate that cannot answer must never be
// the reason a worker's tool call fails.
func TestGateHookAllowsSilentlyWithoutADecisionLeg(t *testing.T) {
	t.Setenv(captaincode.SystemOneKeyEnv, "")
	t.Setenv(captaincode.SystemOneURLEnv, "")
	t.Setenv("CAPTAIN_SRC", t.TempDir())
	t.Chdir(t.TempDir())
	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"rm -rf /"},"cwd":"/repo"}`)
	out := captureStdout(t, gateHook)
	assert.Empty(t, strings.TrimSpace(out), "no decision leg: no verdict, no deny, no output")
}

func TestGateHookAllowsSilentlyOnAnUnrecognisedPayload(t *testing.T) {
	withStdin(t, `not json at all`)
	out := captureStdout(t, gateHook)
	assert.Empty(t, strings.TrimSpace(out))
}

func TestGateReportSaysWhatItHasWhenNothingIsRecorded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := captureStdout(t, func() { gateReport(0.9, 20) })
	assert.Contains(t, out, "no screenings recorded")
	assert.Contains(t, out, "keyless CAPTAIN_SYSTEMONE_URL")
}

// A gate noul predicts something about an action; it is not a second opinion
// on a decision captain made beside it. The report has to say so, or the
// first person to read it will take an empty agreement column for a bar.
func TestGateReportRefusesToReadUncomparedRowsAsABar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".captaincode"), 0o700))
	captaincode.AppendGateLog(captaincode.ShadowRecord{
		Point: captaincode.PointGate, Points: captaincode.GatePoints, Task: "bash rm -rf x",
		Shadow: captaincode.Shadow{Leg: captaincode.LegJev, Backend: "typesafe", Model: "jev-1.13.0",
			Answers: map[string]captaincode.ShadowAnswer{
				captaincode.PointGateDestructive: {Choice: "true", Confidence: 0.95},
			}},
	})
	out := captureStdout(t, func() { gateReport(0.9, 20) })
	assert.Contains(t, out, "1 screening(s)")
	assert.Contains(t, out, "is a PREDICTION about an action")
	assert.Contains(t, out, "not an agreement rate, and not a bar")
}

func TestFirstStringPrefersTheFirstPresentKey(t *testing.T) {
	m := map[string]any{"command": "", "url": "https://example.com", "n": 3}
	assert.Equal(t, "https://example.com", firstString(m, "command", "url"))
	assert.Empty(t, firstString(m, "n", "missing"))
}

// What the worker's environment carries into a screening (pkg gate.go
// GateWorkerEnv), so a PreToolUse hook can judge against the assignment.
func TestGateActionPicksUpTheWorkersAssignmentFromTheEnvironment(t *testing.T) {
	t.Setenv(captaincode.GateLegEnv, "claude")
	t.Setenv(captaincode.GateTaskEnv, "fix the failing test in pool_test.go")
	t.Setenv(captaincode.GateTaskIDEnv, "task-42")
	a := gateActionFromEnv(captaincode.GateAction{Tool: "bash", Command: "go test ./..."})
	assert.Equal(t, captaincode.Leg("claude"), a.Leg)
	assert.Equal(t, "fix the failing test in pool_test.go", a.Task)
	assert.Equal(t, "task-42", a.TaskID)
}

func withStdin(t *testing.T, s string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	require.NoError(t, err)
	_, err = f.WriteString(s)
	require.NoError(t, err)
	_, err = f.Seek(0, 0)
	require.NoError(t, err)
	prev := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = prev; f.Close() })
}
