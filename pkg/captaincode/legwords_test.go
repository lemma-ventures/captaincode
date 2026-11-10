package captaincode

import (
	"errors"
	"strings"
	"testing"
)

func TestRouteWordsNameTheRoute(t *testing.T) {
	want := map[string]Leg{
		"claude-cli":          LegClaude,
		"cursor-cli":          LegCursor,
		"luna-cli":            LegLuna,
		"grok-opencode":       LegGrok,
		"grok-max-opencode":   LegGrokMax,
		"deepseek-openrouter": LegDeepSeek,
	}
	words := RouteWords()
	for w, l := range want {
		if !KnownLeg(l) {
			continue // disabled by an overlay on this machine
		}
		if got, ok := words[w]; !ok || got != l {
			t.Errorf("RouteWords()[%q] = %q, %v; want %q", w, got, ok, l)
		}
	}
	// A leg already named for its route has no second word.
	for _, l := range []Leg{LegCodexCLI, LegGrokCLI} {
		if w := RouteWord(l); w != "" {
			t.Errorf("RouteWord(%s) = %q, want none", l, w)
		}
	}
	if w := RouteWord(LegCarbon); w != "carbon-openrouter" {
		t.Errorf("RouteWord(carbon) = %q, want carbon-openrouter", w)
	}
	// A decision leg takes no task, so no word forces it.
	if w := RouteWord(LegJev); w != "" {
		t.Errorf("RouteWord(jev) = %q", w)
	}
}

func TestResolveLegWord(t *testing.T) {
	for in, want := range map[string]Leg{
		"codex":              LegCodex,
		"grok-cli":           LegGrokCLI,
		"carbon":             LegCarbon,
		"/carbon-openrouter": LegCarbon,
		"codex-cli":          LegCodexCLI,
	} {
		if got, ok := ResolveLegWord(in); !ok || got != want {
			t.Errorf("ResolveLegWord(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// codex runs through the Codex CLI (2026-11-10): codex-cli names the
	// codex-cli leg, and the old opencode and OpenRouter words are gone.
	for _, gone := range []string{"codex-opencode", "codex-openrouter"} {
		if _, ok := ResolveLegWord(gone); ok {
			t.Errorf("%s still resolves", gone)
		}
	}
	if _, ok := ResolveLegWord("codex-nope"); ok {
		t.Error("an unknown word resolved")
	}
}

func TestCanonicalLegWords(t *testing.T) {
	for in, want := range map[string]string{
		"/grok-max-opencode fix it":                         "/grok-max fix it",
		"/claude-cli: review":                               "/claude: review",
		"/grok-opencode fix A + /deepseek-openrouter fix B": "/grok fix A + /deepseek fix B",
		"/grok-opencode review X > /claude-cli":             "/grok review X > /claude",
		"/team /kimi-openrouter /claude-cli audit":          "/team /kimi /claude audit",
		"/grok-cli fix it":                                  "/grok-cli fix it", // a leg id stays
		"read /Users/me/codex-opencode/main.go":             "read /Users/me/codex-opencode/main.go",
		"/codex-opencoder fix":                              "/codex-opencoder fix",
	} {
		if got := CanonicalLegWords(in); got != want {
			t.Errorf("CanonicalLegWords(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNamedOnlyLegsStayOffTheRouter(t *testing.T) {
	for _, l := range []Leg{LegGrokCLI} {
		if !KnownLeg(l) {
			t.Fatalf("%s is not registered", l)
		}
		if AutoRoutes(l) {
			t.Errorf("%s auto-routes; it runs only when named", l)
		}
		for _, r := range Rungs {
			if r == l {
				t.Errorf("%s is a rung", l)
			}
		}
		if !ServesTasks(l) {
			t.Errorf("%s takes no task", l)
		}
	}
}

func TestSlashCommandsListRouteWords(t *testing.T) {
	all := strings.Join(SlashCommands(), " ")
	for _, w := range []string{"grok-cli", "luna-cli", "grok-opencode", "claude-cli"} {
		if !strings.Contains(" "+all+" ", " "+w+" ") {
			t.Errorf("SlashCommands lacks %q", w)
		}
	}
}

func TestGrokCLIArgs(t *testing.T) {
	t.Setenv("CAPTAIN_GROK_CLI_MODEL", "")
	t.Setenv("CAPTAIN_GROK_CLI_EFFORT", "")
	args := strings.Join(grokCLICmdArgs("/tmp/repo", "fix it"), " ")
	for _, want := range []string{"-p fix it", "--output-format streaming-json", "-m grok-4.7", "--cwd /tmp/repo", "--always-approve"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "--reasoning-effort") {
		t.Errorf("effort passed unasked: %q", args)
	}
	t.Setenv("CAPTAIN_GROK_CLI_MODEL", "grok-4.7-build-fast")
	t.Setenv("CAPTAIN_GROK_CLI_EFFORT", "high")
	args = strings.Join(grokCLICmdArgs("", "x"), " ")
	if !strings.Contains(args, "-m grok-4.7-build-fast") || !strings.Contains(args, "--reasoning-effort high") {
		t.Errorf("env pins ignored: %q", args)
	}
}

func TestClassifyGrokCLIFailure(t *testing.T) {
	err := classifyGrokCLIFailure("Not signed in. To authenticate without a browser, run:\n  grok login --device-code")
	if !strings.Contains(err.Error(), "grok login") || !errors.Is(err, ErrProviderDown) {
		t.Errorf("a dead login is not provider-down: %v", err)
	}
}

func TestGrokToolStatusNamesTheShell(t *testing.T) {
	ev := grokEvent{ToolName: "run_terminal_command", Title: "run_terminal_command", RawInput: []byte(`{"command":"ls"}`)}
	if got := grokToolStatus(ev); !strings.Contains(got, "shell") || !strings.Contains(got, "ls") {
		t.Errorf("grokToolStatus = %q, want a shell line", got)
	}
}

// Every `grok -p` run turns off analytics, trace uploads and crash reports,
// over anything the inherited environment says: exec uses a name's last value.
func TestGrokCLIEnvTurnsOffTelemetry(t *testing.T) {
	env := grokCLIEnv([]string{"GROK_TELEMETRY_ENABLED=1", "GROK_TELEMETRY_TRACE_UPLOAD=1", "HOME=/h"}, "fix the parser", "")
	last := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		last[k] = v
	}
	for k, v := range map[string]string{
		"GROK_TELEMETRY_ENABLED": "0", "DISABLE_TELEMETRY": "1",
		"GROK_TELEMETRY_TRACE_UPLOAD": "0", "DISABLE_ERROR_REPORTING": "1",
		"GROK_DISABLE_AUTOUPDATER": "1", "HOME": "/h",
	} {
		if last[k] != v {
			t.Errorf("%s = %q, want %q", k, last[k], v)
		}
	}
}
