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

func TestStripJSONC(t *testing.T) {
	src := []byte(`{
  // line comment
  "url": "https://example.com/path", /* block
  comment */
  "slash_in_string": "a // not a comment",
  "list": [1, 2, 3,],
  "obj": {"a": 1,},
}`)
	var out map[string]any
	require.NoError(t, json.Unmarshal(StripJSONC(src), &out))
	assert.Equal(t, "https://example.com/path", out["url"])
	assert.Equal(t, "a // not a comment", out["slash_in_string"])
	assert.Len(t, out["list"], 3)
}

func readCfg(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(StripJSONC(b), &cfg), "config must parse: %s", b)
	return cfg
}

func TestEnsureOpencodeConfig_CreatesFromScratch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	changed, notes, err := EnsureOpencodeConfig(path, true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NotEmpty(t, notes)

	cfg := readCfg(t, path)
	perm, ok := cfg["permission"].(map[string]any)
	require.True(t, ok, "top-level permission block present")
	// No "*" catch-alls: opencode evaluates last-match-wins over the config's
	// key order, so a reordered catch-all would shadow every deny (live
	// 2026-08-13: a trailing "*":"allow" let a worker read .env secrets).
	// The defaults already allow everything; init only overrides the traps.
	assert.NotContains(t, perm, "*")
	assert.Equal(t, "allow", perm["external_directory"], `"ask" wedges a headless worker forever`)
	assert.Equal(t, "deny", perm["question"], "question tool blocks for an answer nobody can give")
	// Action-only schema keys must be plain strings, never objects.
	assert.IsType(t, "", perm["webfetch"])
	assert.IsType(t, "", perm["websearch"])
	assert.IsType(t, "", perm["doom_loop"])
	read, ok := perm["read"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, read, "*", "a catch-all could shadow the denies")
	assert.Equal(t, "deny", read["*.env"], ".env stays protected")
	assert.Equal(t, "deny", read["*.env.*"])
	assert.Equal(t, "allow", read["*.env.example"])

	cmd, ok := cfg["command"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, cmd, "init", "TUI accepts /init only if the command entry exists")
	assert.Contains(t, cmd, "quality")
	// /rename must be a captain command, not opencode's built-in: a custom
	// command with the same name overrides it, sending the template through the
	// plugin, which sets the title from the repo and recent context.
	ren, ok := cmd["rename"].(map[string]any)
	require.True(t, ok, "the /rename control word is registered")
	assert.Equal(t, "/rename $ARGUMENTS", ren["template"])

	prov, ok := cfg["provider"].(map[string]any)
	require.True(t, ok)
	captain, ok := prov["captain"].(map[string]any)
	require.True(t, ok)
	models := captain["models"].(map[string]any)
	for _, m := range []string{"claude", "codex", "grok", "free", "cursor", "glm", "minimax", "team", "frontier"} {
		assert.Contains(t, models, m)
	}
	assert.Equal(t, "captain/free", cfg["small_model"])
}

// existingFixture mirrors the live hand-edited config this feature replaces:
// comments, per-agent "*":"allow" blocks (which merge LAST and defeat the .env
// deny), an object under the Action-only webfetch key (schema break), and
// ask-actions that wedge headless workers.
const existingFixture = `{
  // hand-maintained config
  "$schema": "https://opencode.ai/config.json",
  "small_model": "captain/free",
  "command": {
    "quality": { "description": "route to the strongest model", "template": "/quality $ARGUMENTS" }
  },
  "provider": {
    "captain": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:14097/v1", "apiKey": "captain" },
      "models": { "claude": { "name": "Claude" } }
    },
    "nim": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "https://integrate.api.nvidia.com/v1", "apiKey": "{env:NVIDIA_API_KEY}" },
      "models": { "z-ai/glm-5.2": { "name": "GLM-5.2 (NIM)" } }
    }
  },
  "agent": {
    "title": { "model": "captain/free" },
    "build": { "permission": { "*": "allow", "webfetch": "allow" } },
    "plan": { "permission": { "*": "allow" } }
  },
  "permission": {
    "*": "allow",
    "webfetch": { "*": "allow", "https://github.com/*": "allow" },
    "external_directory": { "*": "ask", "~/Gits/**": "allow" },
    "doom_loop": "ask"
  }
}`

func TestEnsureOpencodeConfig_NormalizesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(path, []byte(existingFixture), 0o644))

	changed, notes, err := EnsureOpencodeConfig(path, true)
	require.NoError(t, err)
	assert.True(t, changed)
	joined := strings.Join(notes, "\n")
	assert.Contains(t, joined, "agent", "notes explain the agent-override removal")

	cfg := readCfg(t, path)

	// Permission surface is owned by init: canonical block, ask-traps gone.
	perm := cfg["permission"].(map[string]any)
	assert.Equal(t, "allow", perm["external_directory"])
	assert.Equal(t, "allow", perm["webfetch"], "Action-only key normalized to a plain string")
	assert.NotEqual(t, "ask", perm["doom_loop"])

	// Agent-level permission overrides removed (they merge after the global
	// block and re-allowed .env reads); the rest of the agent config survives.
	agents := cfg["agent"].(map[string]any)
	build, ok := agents["build"].(map[string]any)
	if ok {
		assert.NotContains(t, build, "permission")
	}
	title := agents["title"].(map[string]any)
	assert.Equal(t, "captain/free", title["model"])

	// Untouched user config survives the rewrite.
	prov := cfg["provider"].(map[string]any)
	assert.Contains(t, prov, "nim")
	assert.Equal(t, "captain/free", cfg["small_model"])

	// /init becomes typable in the TUI.
	cmd := cfg["command"].(map[string]any)
	assert.Contains(t, cmd, "init")
	assert.Contains(t, cmd, "quality")
}

func TestEnsureOpencodeConfig_AddsTheWorkspaceHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(path, []byte(existingFixture), 0o644))
	_, notes, err := EnsureOpencodeConfig(path, true)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(notes, "\n"), "X-Captain-Cwd")

	cfg := readCfg(t, path)
	captain := cfg["provider"].(map[string]any)["captain"].(map[string]any)
	options := captain["options"].(map[string]any)
	assert.Equal(t, "http://127.0.0.1:14097/v1", options["baseURL"], "the connection block is kept")
	headers := options["headers"].(map[string]any)
	assert.Equal(t, "{env:CAPTAIN_CWD}", headers["X-Captain-Cwd"],
		"the TUI names its folder on every call: one brain, many TUIs, each in its own repo")

	// From scratch the header is there from the start.
	fresh := filepath.Join(t.TempDir(), "opencode.jsonc")
	_, _, err = EnsureOpencodeConfig(fresh, true)
	require.NoError(t, err)
	freshOpts := readCfg(t, fresh)["provider"].(map[string]any)["captain"].(map[string]any)["options"].(map[string]any)
	assert.Equal(t, "{env:CAPTAIN_CWD}", freshOpts["headers"].(map[string]any)["X-Captain-Cwd"])
}

func TestEnsureOpencodeConfig_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	_, _, err := EnsureOpencodeConfig(path, true)
	require.NoError(t, err)
	changed, _, err := EnsureOpencodeConfig(path, true)
	require.NoError(t, err)
	assert.False(t, changed, "second run must be a no-op")
}

func TestEnsureOpencodeConfig_CheckModeDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(path, []byte(existingFixture), 0o644))
	before, _ := os.ReadFile(path)

	changed, _, err := EnsureOpencodeConfig(path, false)
	require.NoError(t, err)
	assert.True(t, changed, "check mode still reports what WOULD change")
	after, _ := os.ReadFile(path)
	assert.Equal(t, before, after, "check mode must not write")
}

func TestEnsureOpencodeConfig_BrokenConfigIsNotClobbered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))
	_, _, err := EnsureOpencodeConfig(path, true)
	require.Error(t, err)
	b, _ := os.ReadFile(path)
	assert.Equal(t, "{not json", string(b))
}

// The read rules rely on Go's sorted-key marshal order + opencode's
// last-match-wins evaluation: "*.env.example" (allow) must serialize AFTER
// "*.env.*" (deny), which its filename also matches - and no catch-all may
// appear that could shadow the denies if keys get reordered by a hand edit.
func TestCanonicalReadRuleOrder(t *testing.T) {
	b, err := json.Marshal(canonicalPermission()["read"])
	require.NoError(t, err)
	s := string(b)
	iEnv := strings.Index(s, `"*.env"`)
	iEnvDot := strings.Index(s, `"*.env.*"`)
	iExample := strings.Index(s, `"*.env.example"`)
	require.True(t, iEnv >= 0 && iEnvDot >= 0 && iExample >= 0, s)
	assert.True(t, iEnv < iEnvDot && iEnvDot < iExample,
		"the .env.example re-allow must come after the .env.* deny: %s", s)
	assert.NotContains(t, s, `"*":`, "no catch-all inside the read rules")
}

// Project-level .opencode/opencode.jsonc overlays merge OVER the global
// config (live 2026-08-13: a worker-written overlay in the active repo
// carried agent-level "*":"allow" blocks that re-allowed .env reads, plus an
// external_directory ask-trap with unexpandable "~" patterns). Init must
// normalize existing overlays too - but never create one, and never add TUI
// commands to a project file.
func TestEnsureProjectOverlays(t *testing.T) {
	t.Run("normalizes an existing overlay", func(t *testing.T) {
		dir := t.TempDir()
		ocDir := filepath.Join(dir, ".opencode")
		require.NoError(t, os.MkdirAll(ocDir, 0o755))
		overlay := `{
  "permission": {
    "*": "allow",
    "external_directory": { "*": "ask", "~/Gits/**": "allow" },
    "doom_loop": "ask"
  },
  "agent": {
    "build": { "permission": { "*": "allow" } },
    "custom": { "model": "captain/free", "permission": { "*": "allow" } }
  }
}`
		path := filepath.Join(ocDir, "opencode.jsonc")
		require.NoError(t, os.WriteFile(path, []byte(overlay), 0o644))

		changed, notes, err := EnsureProjectOverlays(dir, true)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.NotEmpty(t, notes)

		cfg := readCfg(t, path)
		perm := cfg["permission"].(map[string]any)
		assert.NotContains(t, perm, "*")
		assert.Equal(t, "allow", perm["external_directory"])
		assert.Equal(t, "deny", perm["doom_loop"])
		agents := cfg["agent"].(map[string]any)
		build := agents["build"].(map[string]any)
		assert.NotContains(t, build, "permission")
		custom := agents["custom"].(map[string]any)
		assert.NotContains(t, custom, "permission")
		assert.Equal(t, "captain/free", custom["model"], "non-permission agent config survives")
		assert.NotContains(t, cfg, "command", "TUI commands never belong in a project overlay")
	})
	t.Run("never creates an overlay", func(t *testing.T) {
		dir := t.TempDir()
		changed, _, err := EnsureProjectOverlays(dir, true)
		require.NoError(t, err)
		assert.False(t, changed)
		_, statErr := os.Stat(filepath.Join(dir, ".opencode"))
		assert.True(t, os.IsNotExist(statErr))
	})
	t.Run("overlay without permission surface is untouched", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "opencode.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"mcp": {"x": {"type": "local"}}}`), 0o644))
		changed, _, err := EnsureProjectOverlays(dir, true)
		require.NoError(t, err)
		assert.False(t, changed)
		b, _ := os.ReadFile(path)
		assert.Equal(t, `{"mcp": {"x": {"type": "local"}}}`, string(b))
	})
}

func TestEnsureCaptainEnv(t *testing.T) {
	t.Run("scaffolds when missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "env")
		changed, _, err := EnsureCaptainEnv(path, true)
		require.NoError(t, err)
		assert.True(t, changed)
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Contains(t, string(b), "CAPTAIN_DIRECTOR")
		assert.Contains(t, string(b), "CAPTAIN_ROUTE_TIMEOUT_MS")
	})
	t.Run("never touches an existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "env")
		require.NoError(t, os.WriteFile(path, []byte("CAPTAIN_DIRECTOR=grok\n"), 0o644))
		changed, _, err := EnsureCaptainEnv(path, true)
		require.NoError(t, err)
		assert.False(t, changed)
		b, _ := os.ReadFile(path)
		assert.Equal(t, "CAPTAIN_DIRECTOR=grok\n", string(b))
	})
}

func TestRunInit_Report(t *testing.T) {
	dir := t.TempDir()
	report, changed, err := RunInit(InitOptions{
		Apply:        true,
		OpencodePath: filepath.Join(dir, "opencode.jsonc"),
		EnvPath:      filepath.Join(dir, "env"),
		ProjectDir:   filepath.Join(dir, "project"),
	})
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Contains(t, report, "permission")
	assert.Contains(t, strings.ToLower(report), "restart", "report must say a restart is needed to apply")
	assert.Contains(t, report, ".env", "report must state the secret-file protection")

	// check mode after apply: nothing left to do
	report2, changed2, err := RunInit(InitOptions{
		Apply:        false,
		OpencodePath: filepath.Join(dir, "opencode.jsonc"),
		EnvPath:      filepath.Join(dir, "env"),
		ProjectDir:   filepath.Join(dir, "project"),
	})
	require.NoError(t, err)
	assert.False(t, changed2, report2)
}

// A leg the brain serves but the TUI config does not list is unreachable: the
// fork validates model ids against the captain provider block, and the slash
// palette only knows the commands in the file. Adding a leg used to mean
// hand-editing opencode.jsonc (codex-cli, 2026-09-09); /init now adds what is
// missing and leaves every existing entry as it is.
func TestEnsureOpencodeConfig_AddsMissingLegEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(path, []byte(existingFixture), 0o644))
	changed, notes, err := EnsureOpencodeConfig(path, true)
	require.NoError(t, err)
	assert.True(t, changed)
	joined := strings.Join(notes, "\n")
	assert.Contains(t, joined, "codex-cli", "notes name the legs that were added")

	cfg := readCfg(t, path)
	models := cfg["provider"].(map[string]any)["captain"].(map[string]any)["models"].(map[string]any)
	for _, id := range append(workerLegIDs(), "team", "frontier", "workflow", "auto") {
		assert.Contains(t, models, id, "captain provider advertises %s", id)
	}
	assert.NotContains(t, models, string(LegJev), "a decision leg is not a model a turn can be sent to")
	assert.Contains(t, models, "auto", "every unprefixed prompt is sent as captain/auto; opencode refuses an id this block lacks")
	claude := models["claude"].(map[string]any)
	assert.Equal(t, "Claude", claude["name"], "an existing entry's name is the user's")
	asJSON := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	assert.Equal(t, `{"context":1000000,"output":32768}`, asJSON(claude["limit"]), "…but its context window is declared, so the TUI's '% used' means something")
	assert.Equal(t, `{"input":0,"output":0}`, asJSON(claude["cost"]), "a subscription leg prices at 0")
	assert.Equal(t, `{"input":1.09,"output":3.43}`, asJSON(models["glm"].(map[string]any)["cost"]), "an API leg carries its registry price")
	cmds := cfg["command"].(map[string]any)
	for _, id := range append(workerLegIDs(), "team", "frontier") {
		assert.Contains(t, cmds, id, "slash command /%s", id)
	}
	assert.NotContains(t, cmds, string(LegJev), "no /jev forcing command: it takes questions, not a prompt")
	assert.Equal(t, "route to the strongest model", cmds["quality"].(map[string]any)["description"], "existing commands keep their text")

	// Idempotent: a second pass finds nothing to do.
	changed, _, err = EnsureOpencodeConfig(path, true)
	require.NoError(t, err)
	assert.False(t, changed)
}

// workerLegIDs is LegIDs without the decision legs - what the TUI is wired for.
func workerLegIDs() []string {
	var out []string
	for _, l := range AllLegs {
		if ServesTasks(l) {
			out = append(out, string(l))
		}
	}
	return out
}

func TestInitReportListsCodexCLI(t *testing.T) {
	assert.Contains(t, cliTools(), "codex", "the codex-cli leg runs on the Codex CLI")
}
