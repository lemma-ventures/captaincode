package captaincode

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A leg that cannot run must fail as a TYPED fault, not as a string. Untyped,
// it matches no sentinel: the reroute net does not reroute it, the policy
// layer does not bench it, and the next turn pays the same failure again.
// This is the 2026-09-22 run where thirteen legs failed in ninety seconds and
// the transcript named neither a cause nor a fix.

func TestClassifyOpencodeErrorTypesAMissingKey(t *testing.T) {
	// xAI's wording, verbatim: the message says "API key is missing", never
	// "invalid api key", and the class lives in the error NAME.
	raw := json.RawMessage(`{"name":"ProviderAuthError","data":{"providerID":"xai","message":"xAI API key API key is missing. Pass it using the 'apiKey' parameter or the XAI_API_KEY environment variable."}}`)
	err := classifyOpencodeError("xai", "grok-build-0.1", raw, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderAuth, "a missing key is an auth fault")
	assert.ErrorIs(t, err, ErrProviderDown, "auth wraps provider-down, so the task reroutes")
	assert.Contains(t, err.Error(), "xai/grok-build-0.1", "the leg's provider and model are named")
	assert.Contains(t, err.Error(), "XAI_API_KEY", "the provider's own sentence, in its own casing - it names the env var to set")
	assert.NotContains(t, err.Error(), "apikey' parameter", "classify-safe lowercasing does not leak into what a human reads")
	assert.True(t, harnessFault(err.Error()), "the operator's key, not the model: off the scorecard")
}

func TestClassifyOpencodeErrorTypesAnUnconfiguredProvider(t *testing.T) {
	// What the serve logs behind an opaque 500 when the provider was never
	// authenticated, so its models never entered the registry.
	detail := "ProviderModelNotFoundError: Model not found: openrouter/qwen/qwen3.5-397b-a17b. Did you mean: qwen/qwen3.5-397b-a17b?"
	err := classifyOpencodeError("openrouter", "qwen/qwen3.5-397b-a17b", json.RawMessage(`{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_beac7f17"}}`), detail)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderNotConfigured)
	assert.ErrorIs(t, err, ErrProviderAuth, "an absent key benches like a rejected one")
	assert.Contains(t, err.Error(), "opencode auth login", "the error carries its own fix")
	assert.Contains(t, err.Error(), "openrouter")
}

func TestClassifyOpencodeErrorKeepsCapacityOffTheQuotaWindow(t *testing.T) {
	// Capacity errors ship with HTTP 429: classifying on "429" first would
	// bench a healthy leg for the 30m quota window.
	err := classifyOpencodeError("xai", "grok-4.7", json.RawMessage(`{"name":"APIError","data":{"message":"currently at capacity due to high demand","statusCode":429}}`), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderDown)
	assert.NotErrorIs(t, err, ErrRateLimited)
}

func TestClassifyOpencodeErrorLeavesTheUnknownUnclassified(t *testing.T) {
	// An unknown error is the provider's until proven otherwise - inventing a
	// class for it is how an NVIDIA 404 once became a 30-minute cooldown.
	assert.NoError(t, classifyOpencodeError("nim", "moonshotai/kimi-k3",
		json.RawMessage(`{"name":"SomethingNew","data":{"message":"the model declined politely"}}`), ""))
}

func TestOpencodeErrorRefOnlyTrustsItsOwnShape(t *testing.T) {
	assert.Equal(t, "err_beac7f17", opencodeErrorRef(json.RawMessage(`{"name":"UnknownError","data":{"ref":"err_beac7f17"}}`)))
	assert.Empty(t, opencodeErrorRef(json.RawMessage(`{"data":{"ref":"../../../etc/passwd"}}`)), "a response never steers the log scan")
	assert.Empty(t, opencodeErrorRef(json.RawMessage(`not json`)))
}

func TestOpencodeLogDetailResolvesTheRefTheServeKeptToItself(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	logDir := filepath.Join(dir, "opencode", "log")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	line := `timestamp=2026-09-22T10:54:20.563Z level=ERROR run=6e0ba243 message=failed ref=err_beac7f17 error="ProviderModelNotFoundError: Model not found: openrouter/qwen/qwen3.5-397b-a17b. Did you mean: qwen/qwen3.5-397b-a17b?" cause="ProviderModelNotFoundError: Model not found\n    at <anonymous> (/$bunfs/root/chunk.js:439:94093)"`
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "opencode.log"), []byte("noise\n"+line+"\nmore noise\n"), 0o644))

	got := opencodeLogDetail("err_beac7f17")
	assert.Contains(t, got, "Model not found: openrouter/qwen/qwen3.5-397b-a17b")
	assert.NotContains(t, got, "bunfs", "the JS stack trace helps nobody here")

	old := logDetailBackoff
	logDetailBackoff = nil // no live serve here: nothing is about to be flushed
	t.Cleanup(func() { logDetailBackoff = old })
	assert.Empty(t, opencodeLogDetail("err_00000000"), "a ref with no line yields no detail")
	assert.Empty(t, opencodeLogDetail(""), "no ref, no scan")
}

func TestOpencodeLogDetailIsBestEffort(t *testing.T) {
	old := logDetailBackoff
	logDetailBackoff = nil // the retry is for a live serve's flush, not for a missing file
	t.Cleanup(func() { logDetailBackoff = old })
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "nothing-here"))
	assert.Empty(t, opencodeLogDetail("err_beac7f17"), "no log is not an error")
}

func TestClassifyClaudeFailureNamesTheLogin(t *testing.T) {
	err := classifyClaudeFailure("Not logged in · Please run /login")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderDown, "the task reroutes to another leg")
	assert.Contains(t, err.Error(), "claude /login", "the error carries its own fix")
	assert.True(t, harnessFault(err.Error()), "a dead login is captain's fault, not the model's")
}

func TestClassifyClaudeFailureLeavesRealErrorsAlone(t *testing.T) {
	err := classifyClaudeFailure("the tool call returned nonsense")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrProviderDown)
	assert.Contains(t, err.Error(), "claude error:")
}

func TestEveryAgentCLIClassifiesItsOwnDeadLogin(t *testing.T) {
	// All four agent CLIs keep their own credential store, and an installed
	// binary says nothing about being logged in. Each must report that as a
	// typed fault, or the leg is dispatched again on the next turn.
	assert.True(t, claudeAuthError("Not logged in · Please run /login"))
	assert.True(t, cursorAuthError("Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY"))
	assert.True(t, codexCLIAuthError("stream error: 401 Unauthorized; run `codex login`"))
	assert.True(t, providerAuthError("ProviderAuthError xAI API key API key is missing"))
}

func TestRunRoutesCLILegsByTransport(t *testing.T) {
	// codex-cli died here with `unknown leg "codex-cli"`: Run switched on the
	// leg ID and fell through to the opencode model table, which holds
	// opencode-transport legs only. The leg must never be reported as unknown.
	if _, err := exec.LookPath("codex"); err == nil {
		t.Skip("codex is installed here - dispatching would run a real agent turn")
	}
	d := &OpencodeDispatcher{Dir: t.TempDir(), BaseURL: "http://127.0.0.1:1"}
	_, err := d.Run(LegCodexCLI, "say hello")
	if err != nil {
		assert.NotContains(t, err.Error(), "unknown leg", "a CLI transport is dispatched, not looked up in the model table")
	}
}

func TestRunStillRejectsALegThatDoesNotExist(t *testing.T) {
	d := &OpencodeDispatcher{Dir: t.TempDir(), BaseURL: "http://127.0.0.1:1"}
	_, err := d.Run(Leg("no-such-leg"), "say hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown leg")
	assert.Contains(t, err.Error(), "captain legs", "the error says where the known ones are listed")
}

func TestProviderNotConfiguredWrapsForTheBenchPolicy(t *testing.T) {
	// The bench policy reads these with errors.Is; keep the chain intact.
	require.True(t, errors.Is(ErrProviderNotConfigured, ErrProviderAuth))
	require.True(t, errors.Is(ErrProviderNotConfigured, ErrProviderDown))
}
