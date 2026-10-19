package captaincode

// Opt-in third-party integrations for Claude and OpenRouter workers.
//
// All three are off by default: each is gated by an environment variable the
// operator sets deliberately. The behaviour is intentionally narrow so a
// regression in any of them cannot reach a real provider without the operator
// having opted in.
//
//   - CAPTAIN_CLAUDE_ADVISOR: a second, stronger Claude model the worker can
//     consult during a task. Anthropic shipped this on Claude Code 2.1.260 as
//     `--advisor <model>`. Only applied to the cheap tier (Sonnet), where an
//     Opus advisor adds judgement without doubling the worker's own cost.
//     Frontier already runs Opus, so the advisor offers nothing there. The
//     name is validated against an allowlist so a typo cannot name a private
//     model id into a flag the worker will pass upstream.
//
//   - CAPTAIN_SERENA_MCP: Serena is an LSP-backed code-intelligence MCP
//     (https://github.com/oraios/serena, GPL-3.0). Captain invokes it as a
//     separate MCP process, alongside Euclid's. Three settings are forced
//     because Serena's defaults phone home or open a web dashboard:
//     SERENA_USAGE_REPORTING=false, --open-web-dashboard False, and a hop
//     limit at the brain (failure:36590ea033536e13 capped generic MCP
//     launchers at 4 child spawns to stop a recursive MCP→MCP→MCP loop).
//
//   - CAPTAIN_HEADROOM_OPENROUTER_BASE: Headroom is a transparent proxy that
//     compresses what an agent reads before it reaches the model
//     (https://github.com/headroomlabs-ai/headroom). When this env var names a
//     listening Headroom URL, captain's own egress proxy forwards OpenRouter
//     traffic through it. HEADROOM_OFFLINE=1 must be set by the operator to
//     block Headroom's telemetry and outbound calls - captain refuses to
//     enable the route without it.

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
)

// claudeAdvisorAllowlist is the set of names CAPTAIN_CLAUDE_ADVISOR accepts.
// Anthropic's docs name opus, fable and opus-5+ at release; nothing else has
// a published advisor endpoint. Empty strings, mixed case, and unknown names
// are rejected so a typo cannot pass a private model id to the worker.
var claudeAdvisorAllowlist = map[string]bool{
	"opus":    true,
	"fable":   true,
	"opus-5":  true,
	"opus-5+": true,
}

// claudeAdvisorArg returns the claude -p flag that opts this run into an
// advisor consultation, or nil when the feature is off, the tier forbids it,
// or the name is not on the allowlist.
//
// The flag goes onto a cheap-tier run only. Frontier already uses Opus, so
// asking Claude Code to consult a model of equal or lesser capability adds
// latency for no gain. The flag is not added to the director's claude -p
// call (runClaudeTimeout with dir "").
func claudeAdvisorArg(frontier bool, effort Effort) []string {
	if frontier {
		return nil
	}
	v := strings.TrimSpace(os.Getenv("CAPTAIN_CLAUDE_ADVISOR"))
	if v == "" {
		return nil
	}
	if !claudeAdvisorAllowlist[strings.ToLower(v)] {
		return nil
	}
	if TierOf(effort) != TierCheap {
		return nil
	}
	return []string{"--advisor", v}
}

// serenaMCPHopLimit is the cap on how many times a generic MCP server may
// spawn a child process from inside another worker. A recursive loop
// (Captain → Euclid → Serena → Euclid) blew the supervisor budget on
// 2026-10-04; the brain tracks spawn depth and refuses a fourth hop.
const serenaMCPHopLimit = 4

// serenaMCPEnabled reports whether the operator asked for the Serena MCP
// alongside Euclid. The brain never installs it without CAPTAIN_SERENA_MCP=1.
func serenaMCPEnabled() bool { return os.Getenv("CAPTAIN_SERENA_MCP") == "1" }

// serenaHopDepth reads CAPTAIN_SERENA_HOP_DEPTH, the spawn count this MCP
// process inherits from its parent. 0 when unset: the launcher is itself the
// first hop. The brain caps the value at serenaMCPHopLimit on the way in.
func serenaHopDepth() int {
	v := strings.TrimSpace(os.Getenv("CAPTAIN_SERENA_HOP_DEPTH"))
	if v == "" {
		return 0
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > serenaMCPHopLimit {
			return serenaMCPHopLimit
		}
	}
	return n
}

// serenaMCPServer is the spec handed to claude -p, codex exec, and the
// opencode provider block. The launcher is `serena start-mcp-server` with
// the language servers the brain already downloaded; telemetry and the web
// dashboard are forced off in the child's environment so a Serena upgrade
// cannot silently change the policy.
func serenaMCPServer(dir string) (command string, args []string, env map[string]string, ok bool) {
	if !serenaMCPEnabled() || dir == "" {
		return "", nil, nil, false
	}
	env = map[string]string{
		"CAPTAIN_CWD":            dir,
		"SERENA_USAGE_REPORTING": "false",
		"SERENA_DASHBOARD_OPEN":  "False",
		"SERENA_HOP_DEPTH":       strconv.Itoa(serenaHopDepth()),
	}
	for _, key := range []string{
		"SERENA_LANGUAGES", "SERENA_PROJECT_ROOT", "SERENA_CONTEXT",
	} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	return "serena", []string{"start-mcp-server", "--open-web-dashboard", "False"}, env, true
}

// SerenaClaudeMCP is the `--mcp-config` argument for claude -p, a JSON
// string with the serena server; nil when Serena is off or no workspace.
// Distinct from Euclid's entry so a failing Serena does not take the
// Euclid server down with it.
func SerenaClaudeMCP(dir string) []string {
	command, args, env, ok := serenaMCPServer(dir)
	if !ok {
		return nil
	}
	spec := map[string]any{"mcpServers": map[string]any{"serena": map[string]any{
		"command": command, "args": args, "env": env,
	}}}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil
	}
	return []string{"--mcp-config", string(b)}
}

// SerenaCodexMCP is the `-c mcp_servers.serena.*` overrides for codex
// exec; nil when Serena is off.
func SerenaCodexMCP(dir string) []string {
	command, args, env, ok := serenaMCPServer(dir)
	if !ok {
		return nil
	}
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, strconv.Quote(a))
	}
	var envParts []string
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		envParts = append(envParts, k+"="+strconv.Quote(env[k]))
	}
	return []string{
		"-c", "mcp_servers.serena.command=" + strconv.Quote(command),
		"-c", "mcp_servers.serena.args=[" + strings.Join(quoted, ",") + "]",
		"-c", "mcp_servers.serena.env={" + strings.Join(envParts, ",") + "}",
	}
}

// headroomOpenRouterBase returns the URL the brain's proxy should forward
// OpenRouter requests through, or "" when the operator has not opted in.
// HEADROOM_OFFLINE=1 must be set in the same environment, otherwise the
// function refuses the route: Headroom's defaults phone home and there is
// no second line of defence.
func headroomOpenRouterBase() string {
	base := strings.TrimSpace(os.Getenv("CAPTAIN_HEADROOM_OPENROUTER_BASE"))
	if base == "" {
		return ""
	}
	if os.Getenv("HEADROOM_OFFLINE") != "1" {
		return ""
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return ""
	}
	return strings.TrimRight(base, "/")
}

// HeadroomOpenRouterUpstream is the URL the proxy forwards /openrouter/* to
// when the operator opted in, or the direct OpenRouter origin otherwise.
// One function so the wire-up never forks on a missing config.
func HeadroomOpenRouterUpstream(direct string) string {
	if via := headroomOpenRouterBase(); via != "" {
		return via
	}
	return direct
}
