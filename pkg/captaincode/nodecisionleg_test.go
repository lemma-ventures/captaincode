package captaincode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The contract for everyone without a decision leg: the gate, the supervisor
// and the per-backend calibration are features they do not have, not features
// that happen to them. Nothing calls out, nothing waits, nothing installs.
//
// These are one test file rather than a line in each feature's own because it
// is one promise, and a promise that is easy to break one file at a time.

func noDecisionLeg(t *testing.T) {
	t.Helper()
	t.Setenv(SystemOneKeyEnv, "")
	t.Setenv(SystemOneURLEnv, "")
	t.Setenv("CAPTAIN_SRC", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	require.Nil(t, SystemOneFromEnv(), "the fixture itself must leave no decision leg configured")
}

func TestWithoutADecisionLegTheGateNeverCallsEvenAskedToEnforce(t *testing.T) {
	noDecisionLeg(t)
	t.Setenv(GateModeEnv, "enforce")
	t.Setenv(GateBarEnv, "0.1")
	v := ScreenAction(context.Background(), SystemOneFromEnv(),
		GateAction{Tool: "bash", Command: "rm -rf / --no-preserve-root"})
	assert.True(t, v.Allow)
	assert.False(t, v.Screened)
	assert.Nil(t, v.Shadow)
	assert.Empty(t, v.Reason)
}

func TestWithoutADecisionLegTheSupervisorAsksNothing(t *testing.T) {
	noDecisionLeg(t)
	sh, res, err := SuperviseWithJev(context.Background(), SystemOneFromEnv(),
		SuperviseSnapshot{Leg: LegClaude, Elapsed: time.Hour, Quiet: time.Hour})
	assert.NoError(t, err)
	assert.Nil(t, sh)
	assert.Zero(t, res.Tokens)
}

func TestWithoutADecisionLegTriageFallsBackAndSaysNothingWasAsked(t *testing.T) {
	noDecisionLeg(t)
	// The heuristic tier still answers, which is the whole fallback.
	h := TriageTask("fix the typo in the README")
	assert.NotEmpty(t, h.Class)
	assert.NotEmpty(t, h.Why)
	// And the calibration report says what is missing rather than showing zeros.
	out := FormatShadowCalibration(nil, 0.9, 20)
	assert.Contains(t, out, "no shadow decisions yet")
	assert.Contains(t, out, "keyless CAPTAIN_SYSTEMONE_URL")
}

// The gate's PreToolUse hook is what a worker's every bash call would pay
// for. It must be removable, and removing it must leave the rest of the
// user's Claude settings alone.
func TestGateHookInstallsAndRemovesWithoutDisturbingOtherHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"my-own-hook"}]}]}}`), 0o644))

	changed, note, err := EnsureClaudeGateHook(true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Contains(t, note, gateHookCommand)
	assert.Contains(t, readFileStr(t, path), gateHookCommand)

	again, _, err := EnsureClaudeGateHook(true)
	require.NoError(t, err)
	assert.False(t, again, "installing twice is a no-op")

	changed, _, err = EnsureClaudeGateHook(false)
	require.NoError(t, err)
	assert.True(t, changed)
	body := readFileStr(t, path)
	assert.NotContains(t, body, gateHookCommand)
	assert.Contains(t, body, "my-own-hook", "somebody else's hook survives")
}

// The redaction hook and the gate hook are separate entries: one restores
// placeholders and refuses secret FILES, the other screens what the ACTION
// would do. Installing one must not remove the other.
func TestTheRedactAndGateHooksCoexist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	path := filepath.Join(home, ".claude", "settings.json")

	_, _, err := EnsureClaudeRedactHook(true)
	require.NoError(t, err)
	_, _, err = EnsureClaudeGateHook(true)
	require.NoError(t, err)

	var cfg struct {
		Hooks struct {
			PreToolUse []json.RawMessage `json:"PreToolUse"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal([]byte(readFileStr(t, path)), &cfg))
	assert.Len(t, cfg.Hooks.PreToolUse, 2)
	body := readFileStr(t, path)
	assert.Contains(t, body, redactHookCommand)
	assert.Contains(t, body, gateHookCommand)
}

func readFileStr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}
