package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyRoutingIsReversibleAndLeavesGatewaysAlone(t *testing.T) {
	cfg := map[string]any{"provider": map[string]any{
		"openrouter":  map[string]any{"options": map[string]any{"apiKey": "{env:K}", "baseURL": "https://openrouter.ai/api/v1"}},
		"huggingface": map[string]any{"options": map[string]any{"apiKey": "{env:HF_TOKEN}", "baseURL": "https://router.huggingface.co/v1"}},
		"nim":         map[string]any{"options": map[string]any{"baseURL": "https://corp-gateway.example/nim/v1"}},
		"captain":     map[string]any{"options": map[string]any{"baseURL": "http://127.0.0.1:14097/v1"}},
	}}
	changed, notes := applyProxyRouting(cfg, "http://127.0.0.1:14098", true)
	require.True(t, changed)
	assert.ElementsMatch(t, []string{"openrouter → proxy", "huggingface → proxy", "xai → proxy"}, notes)
	p := cfg["provider"].(map[string]any)
	assert.Equal(t, "http://127.0.0.1:14098/openrouter/v1", p["openrouter"].(map[string]any)["options"].(map[string]any)["baseURL"])
	assert.Equal(t, "http://127.0.0.1:14098/huggingface/v1", p["huggingface"].(map[string]any)["options"].(map[string]any)["baseURL"])
	assert.Equal(t, "http://127.0.0.1:14098/xai/v1", p["xai"].(map[string]any)["options"].(map[string]any)["baseURL"], "xai gets a block of its own")
	assert.Equal(t, "https://corp-gateway.example/nim/v1", p["nim"].(map[string]any)["options"].(map[string]any)["baseURL"], "a custom gateway is not ours to touch")
	assert.Equal(t, "http://127.0.0.1:14097/v1", p["captain"].(map[string]any)["options"].(map[string]any)["baseURL"], "the brain is not a provider to reroute")
	changed, _ = applyProxyRouting(cfg, "http://127.0.0.1:14098", true)
	assert.False(t, changed, "idempotent")

	changed, notes = applyProxyRouting(cfg, "http://127.0.0.1:14098", false)
	require.True(t, changed)
	assert.ElementsMatch(t, []string{"openrouter → direct", "huggingface → direct", "xai → direct"}, notes)
	assert.Equal(t, "https://openrouter.ai/api/v1", p["openrouter"].(map[string]any)["options"].(map[string]any)["baseURL"])
	assert.Equal(t, "https://router.huggingface.co/v1", p["huggingface"].(map[string]any)["options"].(map[string]any)["baseURL"])
	_, hasXai := p["xai"]
	assert.False(t, hasXai, "the block that existed only for the proxy is gone")
}

func TestClaudeRedactHookInstallAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	path := filepath.Join(home, ".claude", "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"model":"opus","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo hi"}]}]}}`), 0o644))

	changed, note, err := EnsureClaudeRedactHook(true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Contains(t, note, "installed")
	changed, _, err = EnsureClaudeRedactHook(true)
	require.NoError(t, err)
	assert.False(t, changed, "idempotent")

	var cfg map[string]any
	require.NoError(t, json.Unmarshal(mustRead(t, path), &cfg))
	pre := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)
	assert.Len(t, pre, 2, "the user's own hook is kept")
	assert.Equal(t, "opus", cfg["model"])

	changed, _, err = EnsureClaudeRedactHook(false)
	require.NoError(t, err)
	assert.True(t, changed)
	require.NoError(t, json.Unmarshal(mustRead(t, path), &cfg))
	pre = cfg["hooks"].(map[string]any)["PreToolUse"].([]any)
	assert.Len(t, pre, 1)
	assert.True(t, entryRunsCommand(pre[0], "echo hi"))
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return b
}

func TestClaudeProxyEnvFollowsTheListeningProxy(t *testing.T) {
	SetProxyBase("")
	assert.Empty(t, ClaudeProxyEnv())
	SetProxyBase("http://127.0.0.1:14098")
	defer SetProxyBase("")
	assert.Equal(t, []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:14098/anthropic"}, ClaudeProxyEnv())
	t.Setenv("CAPTAIN_PROXY_CLAUDE", "0")
	assert.Empty(t, ClaudeProxyEnv())
}

func TestCodexProxyArgsFollowTheListeningProxyAndTheLogin(t *testing.T) {
	SetProxyBase("")
	assert.Empty(t, CodexProxyArgs())
	SetProxyBase("http://127.0.0.1:14098")
	defer SetProxyBase("")

	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeAuth := func(mode string) {
		require.NoError(t, os.WriteFile(home+"/auth.json", []byte(`{"auth_mode":"`+mode+`"}`), 0o600))
	}

	writeAuth("chatgpt")
	joined := strings.Join(CodexProxyArgs(), " ")
	assert.Contains(t, joined, `-c model_provider="captain"`, "the built-in openai provider cannot be overridden; a custom one routes the turn")
	assert.Contains(t, joined, `base_url="http://127.0.0.1:14098/chatgpt/backend-api/codex"`)
	assert.Contains(t, joined, `requires_openai_auth=true`, "the ChatGPT login is reused")
	assert.NotContains(t, joined, "env_key")

	writeAuth("apikey")
	joined = strings.Join(CodexProxyArgs(), " ")
	assert.Contains(t, joined, `base_url="http://127.0.0.1:14098/openai/v1"`)
	assert.Contains(t, joined, `env_key="OPENAI_API_KEY"`)

	t.Setenv("CAPTAIN_PROXY_CODEX", "0")
	assert.Empty(t, CodexProxyArgs())
}
