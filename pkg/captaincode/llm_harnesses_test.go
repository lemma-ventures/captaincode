package captaincode

// Off-by-default third-party integrations: Claude advisor, Serena MCP,
// Headroom proxy. Each test sets and unsets its env var under a t.Cleanup so
// parallel tests are isolated, and each test exercises one decision at a time.

import (
	"encoding/json"
	"strings"
	"testing"
)

func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

// ── CAPTAIN_CLAUDE_ADVISOR ──────────────────────────────────────────────────

func TestClaudeAdvisorArgOffByDefault(t *testing.T) {
	t.Setenv("CAPTAIN_CLAUDE_ADVISOR", "")
	if got := claudeAdvisorArg(false, EffortLow); got != nil {
		t.Fatalf("unset env var should return nil, got %v", got)
	}
}

func TestClaudeAdvisorArgCheapTierOnly(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		frontier bool
		effort   Effort
		want     []string
	}{
		{"cheap low", "opus", false, EffortLow, []string{"--advisor", "opus"}},
		{"cheap fable", "fable", false, EffortLow, []string{"--advisor", "fable"}},
		{"frontier refused", "opus", true, EffortMax, nil},
		{"unknown name refused", "gpt-9-secret", false, EffortLow, nil},
		{"empty name refused", "", false, EffortLow, nil},
		{"whitespace trimmed", "  opus  ", false, EffortLow, []string{"--advisor", "opus"}},
		{"case insensitive", "OPUS", false, EffortLow, []string{"--advisor", "OPUS"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAPTAIN_CLAUDE_ADVISOR", tc.env)
			got := claudeAdvisorArg(tc.frontier, tc.effort)
			if !equalStringSlice(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The cheap band is only EffortLow (tiers.go:42). Higher efforts stay on the
// quality tier and never see the advisor - matches the recommendation that
// the advisor adds value when the worker runs Sonnet, not when it already
// runs Opus 5.5 at quality / frontier.
func TestClaudeAdvisorArgQualityAndFrontierEfforts(t *testing.T) {
	t.Setenv("CAPTAIN_CLAUDE_ADVISOR", "opus")
	for _, e := range []Effort{EffortMedium, EffortHigh, EffortXHigh, EffortMax} {
		if got := claudeAdvisorArg(false, e); got != nil {
			t.Fatalf("effort %s should not trigger advisor, got %v", e, got)
		}
	}
}

// ── CAPTAIN_SERENA_MCP ──────────────────────────────────────────────────────

func TestSerenaMCPDisabledByDefault(t *testing.T) {
	t.Setenv("CAPTAIN_SERENA_MCP", "")
	cmd, args, env, ok := serenaMCPServer("/tmp/work")
	if ok || cmd != "" || args != nil || env != nil {
		t.Fatalf("expected disabled serena server, got ok=%v cmd=%q args=%v env=%v", ok, cmd, args, env)
	}
}

func TestSerenaMCPEnabledHasHardenedEnv(t *testing.T) {
	t.Setenv("CAPTAIN_SERENA_MCP", "1")
	t.Setenv("CAPTAIN_CWD", "/tmp/work")
	cmd, args, env, ok := serenaMCPServer("/tmp/work")
	if !ok {
		t.Fatal("expected ok=true when CAPTAIN_SERENA_MCP=1")
	}
	if cmd != "serena" {
		t.Fatalf("expected launcher 'serena', got %q", cmd)
	}
	wantArgs := []string{"start-mcp-server", "--open-web-dashboard", "False"}
	if !equalStringSlice(args, wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
	if env["SERENA_USAGE_REPORTING"] != "false" {
		t.Fatalf("SERENA_USAGE_REPORTING = %q, want false (Serena defaults to on)", env["SERENA_USAGE_REPORTING"])
	}
	if env["SERENA_DASHBOARD_OPEN"] != "False" {
		t.Fatalf("SERENA_DASHBOARD_OPEN = %q, want False (dashboard must stay closed)", env["SERENA_DASHBOARD_OPEN"])
	}
	if env["CAPTAIN_CWD"] != "/tmp/work" {
		t.Fatalf("CAPTAIN_CWD not propagated, got %q", env["CAPTAIN_CWD"])
	}
}

func TestSerenaHopDepthCapsAtFour(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 0},
		{"0", 0},
		{"1", 1},
		{"3", 3},
		{"4", 4},
		{"5", 4}, // capped: failure:36590ea033536e13 forbids a 5th hop
		{"99", 4},
		{"not-a-number", 0},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("CAPTAIN_SERENA_HOP_DEPTH", tc.env)
			if got := serenaHopDepth(); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSerenaClaudeMCPValidJSON(t *testing.T) {
	t.Setenv("CAPTAIN_SERENA_MCP", "1")
	args := SerenaClaudeMCP("/tmp/work")
	if len(args) != 2 || args[0] != "--mcp-config" {
		t.Fatalf("expected --mcp-config <json>, got %v", args)
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(args[1]), &spec); err != nil {
		t.Fatalf("mcp config is not valid JSON: %v", err)
	}
	servers, _ := spec["mcpServers"].(map[string]any)
	serena, _ := servers["serena"].(map[string]any)
	if serena == nil {
		t.Fatal("serena server missing from mcp config")
	}
	if serena["command"] != "serena" {
		t.Fatalf("command = %v, want serena", serena["command"])
	}
	env, _ := serena["env"].(map[string]any)
	if env["SERENA_USAGE_REPORTING"] != "false" {
		t.Fatalf("usage reporting not forced off: %v", env["SERENA_USAGE_REPORTING"])
	}
}

func TestSerenaCodexMCPTOMLShape(t *testing.T) {
	t.Setenv("CAPTAIN_SERENA_MCP", "1")
	args := SerenaCodexMCP("/tmp/work")
	if len(args) != 6 {
		t.Fatalf("expected 6 codex -c args, got %d: %v", len(args), args)
	}
	if args[0] != "-c" || !strings.HasPrefix(args[1], "mcp_servers.serena.command=") {
		t.Fatalf("first pair should name the command: %v", args[:2])
	}
	if !strings.Contains(args[1], `"serena"`) {
		t.Fatalf("command not properly quoted: %s", args[1])
	}
	if !strings.Contains(args[5], "SERENA_USAGE_REPORTING=\"false\"") {
		t.Fatalf("usage reporting not forced off in codex env: %s", args[5])
	}
}

func TestSerenaMCPDisabledReturnsNil(t *testing.T) {
	t.Setenv("CAPTAIN_SERENA_MCP", "")
	if got := SerenaClaudeMCP("/tmp/work"); got != nil {
		t.Fatalf("SerenaClaudeMCP(off) = %v, want nil", got)
	}
	if got := SerenaCodexMCP("/tmp/work"); got != nil {
		t.Fatalf("SerenaCodexMCP(off) = %v, want nil", got)
	}
}

// ── CAPTAIN_HEADROOM_OPENROUTER_BASE ────────────────────────────────────────

func TestHeadroomUpstreamOffByDefault(t *testing.T) {
	t.Setenv("CAPTAIN_HEADROOM_OPENROUTER_BASE", "")
	t.Setenv("HEADROOM_OFFLINE", "1")
	direct := "https://openrouter.ai/api"
	if got := HeadroomOpenRouterUpstream(direct); got != direct {
		t.Fatalf("unset env should pass through: got %q", got)
	}
}

func TestHeadroomUpstreamRequiresOfflineFlag(t *testing.T) {
	t.Setenv("CAPTAIN_HEADROOM_OPENROUTER_BASE", "http://127.0.0.1:8787")
	t.Setenv("HEADROOM_OFFLINE", "") // missing → refuse
	direct := "https://openrouter.ai/api"
	if got := HeadroomOpenRouterUpstream(direct); got != direct {
		t.Fatalf("without HEADROOM_OFFLINE=1 must fall back: got %q", got)
	}
}

func TestHeadroomUpstreamWithOfflineFlag(t *testing.T) {
	t.Setenv("CAPTAIN_HEADROOM_OPENROUTER_BASE", "http://127.0.0.1:8787/")
	t.Setenv("HEADROOM_OFFLINE", "1")
	direct := "https://openrouter.ai/api"
	got := HeadroomOpenRouterUpstream(direct)
	want := "http://127.0.0.1:8787"
	if got != want {
		t.Fatalf("got %q, want %q (trailing slash trimmed)", got, want)
	}
}

func TestHeadroomUpstreamRejectsBadScheme(t *testing.T) {
	t.Setenv("CAPTAIN_HEADROOM_OPENROUTER_BASE", "ftp://example.com")
	t.Setenv("HEADROOM_OFFLINE", "1")
	direct := "https://openrouter.ai/api"
	if got := HeadroomOpenRouterUpstream(direct); got != direct {
		t.Fatalf("non-http(s) base should fall back: got %q", got)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
