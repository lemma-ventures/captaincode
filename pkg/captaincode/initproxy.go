package captaincode

// Wiring the egress proxy into the transports that honour a base URL.
//
//   - opencode providers (the workers): each API provider block's baseURL is
//     rewritten to the proxy path for that provider; xai (OAuth, no block of
//     its own) gets a block with just the baseURL - its auth plugin injects
//     the token whatever the URL. Reversible: with CAPTAIN_REDACT=off the
//     same pass writes the direct URLs back, so a config never points at a
//     proxy that is not listening.
//   - claude -p: ANTHROPIC_BASE_URL in the worker's environment (legs.go),
//     set only while the brain's proxy is up.
//   - Claude Code's PreToolUse hook (`captain redact --hook`) in the user
//     settings: restores placeholders in tool input, refuses secret files.
//
//   - codex exec: a custom model provider (`-c model_provider=captain`,
//     CodexProxyArgs) whose base_url is the proxy's /chatgpt/backend-api/codex
//     and which reuses the ChatGPT login (requires_openai_auth). Neither
//     chatgpt_base_url (side channels only - plugins, MCP, analytics) nor an
//     override of the built-in `openai` provider (refused) routes the model
//     turn; the custom provider does (live 2026-09-13, codex-cli 0.153.4:
//     POST /backend-api/codex/responses crossed the proxy, answer intact).
//
// cursor-agent has --endpoint / CURSOR_API_ENDPOINT, but its wire is
// connect/grpc-web+proto: a text scanner cannot rewrite a protobuf frame, so
// it gets the tool-boundary layer only (and its own sandbox).

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// proxyRoutes: provider block → (direct base URL, proxy path). The direct
// URL is what the block carried before; the proxy path is what it carries
// while redaction is on.
var proxyRoutes = map[string]struct{ direct, path string }{
	"openrouter":  {"https://openrouter.ai/api/v1", "/openrouter/v1"},
	"nim":         {"https://integrate.api.nvidia.com/v1", "/nim/v1"},
	"nvidia":      {"https://integrate.api.nvidia.com/v1", "/nvidia/v1"},
	"opencode":    {"https://opencode.ai/zen/v1", "/opencode/v1"},
	"xai":         {"https://api.x.ai/v1", "/xai/v1"},
	"huggingface": {"https://router.huggingface.co/v1", "/huggingface/v1"},
}

var proxyBaseURL atomic.Value // string: "http://127.0.0.1:14098" while the proxy listens, "" otherwise

// SetProxyBase records the listening proxy (the brain calls it after a
// successful bind) or clears it.
func SetProxyBase(base string) { proxyBaseURL.Store(base) }

// ProxyBase is the listening proxy's origin, or "" when there is none.
func ProxyBase() string {
	if v, ok := proxyBaseURL.Load().(string); ok {
		return v
	}
	return ""
}

// ClaudeProxyEnv is the environment claude -p gets so its API calls cross
// the proxy: empty when the proxy is not up or CAPTAIN_PROXY_CLAUDE=0.
func ClaudeProxyEnv() []string {
	base := ProxyBase()
	if base == "" || os.Getenv("CAPTAIN_PROXY_CLAUDE") == "0" {
		return nil
	}
	return []string{"ANTHROPIC_BASE_URL=" + base + "/anthropic"}
}

// CodexProxyArgs is what codex exec gets so its model turn crosses the
// proxy: a custom provider on the proxy's chatgpt route that reuses the
// ChatGPT login (auth_mode chatgpt) or the API key (anything else). Empty
// when the proxy is not up or CAPTAIN_PROXY_CODEX=0.
func CodexProxyArgs() []string {
	base := ProxyBase()
	if base == "" || os.Getenv("CAPTAIN_PROXY_CODEX") == "0" {
		return nil
	}
	args := []string{
		"-c", `model_provider="captain"`,
		"-c", `model_providers.captain.name="OpenAI via captain"`,
		"-c", `model_providers.captain.wire_api="responses"`,
	}
	if codexAuthMode() == "chatgpt" {
		return append(args,
			"-c", `model_providers.captain.base_url="`+base+`/chatgpt/backend-api/codex"`,
			"-c", `model_providers.captain.requires_openai_auth=true`)
	}
	return append(args,
		"-c", `model_providers.captain.base_url="`+base+`/openai/v1"`,
		"-c", `model_providers.captain.env_key="OPENAI_API_KEY"`)
}

// codexAuthMode reads ~/.codex/auth.json's auth_mode ("chatgpt" under a
// subscription login, "apikey" otherwise); "" when there is no login.
func codexAuthMode() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = h + "/.codex"
	}
	raw, err := os.ReadFile(home + "/auth.json")
	if err != nil {
		return ""
	}
	var a struct {
		AuthMode string `json:"auth_mode"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return ""
	}
	return a.AuthMode
}

// EnsureProxyRouting points the opencode provider blocks at the proxy (on)
// or back at the providers (off). Returns whether the file changed and what
// was done. cfgPath "" → the default opencode config.
func EnsureProxyRouting(cfgPath, proxyBase string, on bool) (bool, []string, error) {
	if cfgPath == "" {
		cfgPath = OpencodeConfigPath()
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(StripJSONC(raw), &cfg); err != nil {
		return false, nil, fmt.Errorf("%s does not parse (left untouched): %w", cfgPath, err)
	}
	changed, notes := applyProxyRouting(cfg, proxyBase, on)
	if !changed {
		return false, nil, nil
	}
	if err := writeConfig(cfgPath, cfg); err != nil {
		return false, nil, err
	}
	return true, notes, nil
}

func applyProxyRouting(cfg map[string]any, proxyBase string, on bool) (bool, []string) {
	providers, _ := cfg["provider"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
		cfg["provider"] = providers
	}
	changed := false
	var notes []string
	for id, route := range proxyRoutes {
		block, _ := providers[id].(map[string]any)
		if block == nil {
			if !on || id != "xai" {
				continue // only blocks that exist are rerouted; xai is created (OAuth provider with no block)
			}
			block = map[string]any{}
			providers[id] = block
		}
		options, _ := block["options"].(map[string]any)
		if options == nil {
			options = map[string]any{}
			block["options"] = options
		}
		cur, _ := options["baseURL"].(string)
		want := route.direct
		if on {
			want = proxyBase + route.path
		}
		switch {
		case cur == want:
			continue
		case on && (cur == "" || cur == route.direct || isProxyURL(cur)):
			options["baseURL"] = want
			changed = true
			notes = append(notes, id+" → proxy")
		case !on && isProxyURL(cur):
			if id == "xai" && len(options) == 1 && len(block) == 1 {
				delete(providers, id) // the block existed only for the proxy
			} else {
				options["baseURL"] = route.direct
			}
			changed = true
			notes = append(notes, id+" → direct")
		default:
			// A custom gateway the operator configured: left alone either way.
		}
	}
	return changed, notes
}

func isProxyURL(u string) bool {
	return strings.HasPrefix(u, "http://127.0.0.1:") || strings.HasPrefix(u, "http://localhost:")
}

// ── Claude Code hook ─────────────────────────────────────────────────────────

const redactHookCommand = "captain redact --hook"

// gateHookCommand is the action gate at Claude Code's tool boundary
// (gate.go). It is installed beside the redaction hook and not instead of
// it: one restores placeholders and refuses secret FILES, the other screens
// what the ACTION would do. With no decision leg configured the hook returns
// without a call, so installing it costs a process spawn and nothing else.
const gateHookCommand = "captain gate --hook"

// EnsureClaudeRedactHook installs (on) or removes (off) the PreToolUse hook
// in Claude Code's user settings. Idempotent; other hooks are kept.
func EnsureClaudeRedactHook(on bool) (bool, string, error) {
	return ensureClaudePreToolUseHook(on, redactHookCommand, "Read|Grep|Glob|Write|Edit|MultiEdit|Bash", 10,
		"placeholders restored in tool input, secret files refused")
}

// EnsureClaudeGateHook installs (on) or removes (off) the action gate's
// PreToolUse hook. Same settings file, same shape, its own entry.
func EnsureClaudeGateHook(on bool) (bool, string, error) {
	return ensureClaudePreToolUseHook(on, gateHookCommand, "Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch", 5,
		"actions screened on the decision leg; no decision leg configured means no call and no delay")
}

// ensureClaudePreToolUseHook adds or removes one PreToolUse entry.
// Idempotent; other hooks are kept.
func ensureClaudePreToolUseHook(on bool, command, matcher string, timeout int, note string) (bool, string, error) {
	path := ClaudeSettingsPath()
	if path == "" {
		return false, "", nil
	}
	cfg := map[string]any{}
	body, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(StripJSONC(body), &cfg); err != nil {
			return false, "", fmt.Errorf("%s does not parse (left untouched): %w", path, err)
		}
	case !os.IsNotExist(err):
		return false, "", err
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)
	has := -1
	for i, e := range pre {
		if entryRunsCommand(e, command) {
			has = i
			break
		}
	}
	switch {
	case on && has < 0:
		if hooks == nil {
			hooks = map[string]any{}
			cfg["hooks"] = hooks
		}
		pre = append(pre, map[string]any{
			"matcher": matcher,
			"hooks":   []any{map[string]any{"type": "command", "command": command, "timeout": timeout}},
		})
		hooks["PreToolUse"] = pre
	case !on && has >= 0:
		pre = append(pre[:has], pre[has+1:]...)
		if len(pre) == 0 {
			delete(hooks, "PreToolUse")
		} else {
			hooks["PreToolUse"] = pre
		}
		if len(hooks) == 0 {
			delete(cfg, "hooks")
		}
	default:
		return false, "", nil
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return false, "", err
	}
	if on {
		return true, "installed `" + command + "` as a PreToolUse hook in " + path + " (" + note + ")", nil
	}
	return true, "removed `" + command + "` from " + path, nil
}

func entryRunsCommand(e any, cmd string) bool {
	m, _ := e.(map[string]any)
	hs, _ := m["hooks"].([]any)
	for _, h := range hs {
		hm, _ := h.(map[string]any)
		if c, _ := hm["command"].(string); strings.Contains(c, cmd) {
			return true
		}
	}
	return false
}
