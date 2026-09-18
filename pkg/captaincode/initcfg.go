package captaincode

// captain init - generate the configs that let prompted agents actually work
// headless: search online, edit the repo, run commands, without a permission
// prompt nobody can answer.
//
// The core problem: opencode's default agent ruleset is "*": allow but keeps
// three "ask" traps - external_directory, read of .env files, doom_loop. In
// the interactive TUI an ask is a dialog; in a worker session on the shared
// `opencode serve` an ask blocks a Deferred that nobody ever replies to, so
// the tool part sits "running" until the stall watchdog kills the run (live
// 2026-08-08: a read of a hallucinated out-of-worktree path wedged a codex
// worker exactly this way). Headless permission actions must therefore be
// allow or deny - never ask.
//
// Init owns the permission surface of ~/.config/opencode/opencode.jsonc: it
// writes one canonical top-level block and REMOVES per-agent permission
// overrides, because agent-level rules merge AFTER the global block
// (agent.ts: merge(defaults, specifics, user) + per-agent cfg last) - a
// hand-added `"*": "allow"` there silently re-allowed .env reads.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// canonicalPermission is the one permission block init maintains. Schema notes
// (core/v1/config/permission.ts): webfetch/websearch/doom_loop/question are
// Action-only keys - a plain string, never an object (an object fails config
// parse). read/edit/bash/external_directory accept either.
//
// Deliberately NO "*" catch-alls: opencode's evaluation is last-match-wins
// over the config's key order, so a catch-all that ends up after a deny
// silently shadows it (live 2026-08-13: a trailing "*":"allow" in a
// worker-written overlay let a worker read .env secrets). The built-in agent
// defaults already allow every tool - init only overrides the traps.
func canonicalPermission() map[string]any {
	return map[string]any{
		"edit":      "allow",
		"bash":      "allow",
		"webfetch":  "allow",
		"websearch": "allow",
		// "ask" wedges a headless worker forever; workers legitimately roam
		// outside the pinned worktree (multi-repo tasks, /tmp scratch). NOTE:
		// "~" never expands in opencode patterns - a "~/Gits/**" allow-list
		// matches nothing and leaves the ask-trap live.
		"external_directory": "allow",
		// A detected doom loop gets a hard "no" instead of burning the whole
		// 15m worker cap: the denial surfaces as a tool error the model sees,
		// which usually breaks the loop. "ask" would wedge, "allow" would burn.
		"doom_loop": "deny",
		// The question tool blocks waiting for a human reply that headless
		// workers can never receive. Captain legs deliver prose through the
		// brain, so the TUI session never runs this tool itself.
		"question": "deny",
		// The defaults allow read "*" and set the .env patterns to "ask";
		// these denies land after them in the merged ruleset. Go marshals
		// keys sorted, so "*.env.example" (allow) correctly serializes after
		// "*.env.*" (deny), which also matches it. Locked by
		// TestCanonicalReadRuleOrder.
		"read": map[string]any{
			"*.env":         "deny",
			"*.env.*":       "deny",
			"*.env.example": "allow",
		},
	}
}

// initCommandEntry makes /init typable in the fork's TUI: the input box
// rejects unknown slash commands, and the template expands back into message
// text which the brain intercepts (same mechanism as /quality, /wf).
func initCommandEntry() map[string]any {
	return map[string]any{
		"description": "generate captain configs: worker permissions (web, edits, bash), env scaffold",
		"template":    "/init $ARGUMENTS",
	}
}

func defaultCommands() map[string]any {
	c := map[string]any{
		"quality":       map[string]any{"description": "route to the strongest model (director constrained to the top legs, claude included)", "template": "/quality $ARGUMENTS"},
		"best":          map[string]any{"description": "alias of /quality", "template": "/quality $ARGUMENTS"},
		"speed":         map[string]any{"description": "route to the fastest capable leg", "template": "/speed $ARGUMENTS"},
		"save":          map[string]any{"description": "route to the cheapest plausible leg", "template": "/save $ARGUMENTS"},
		"claude":        map[string]any{"description": "force the claude leg (Max sub, claude -p)", "template": "/claude $ARGUMENTS"},
		"grok":          map[string]any{"description": "force the grok leg (SuperGrok)", "template": "/grok $ARGUMENTS"},
		"codex":         map[string]any{"description": "force the codex leg (ChatGPT sub)", "template": "/codex $ARGUMENTS"},
		"cursor":        map[string]any{"description": "force the cursor leg (cursor-agent)", "template": "/cursor $ARGUMENTS"},
		"glm":           map[string]any{"description": "force GLM-5.3 (OpenRouter, best open weights)", "template": "/glm $ARGUMENTS"},
		"grok-max":      map[string]any{"description": "force grok-4.6, xAI's flagship (frontier-class, SuperGrok)", "template": "/grok-max $ARGUMENTS"},
		"minimax":       map[string]any{"description": "force MiniMax M3 (OpenRouter)", "template": "/minimax $ARGUMENTS"},
		"free":          map[string]any{"description": "force the free leg (zero cost)", "template": "/free $ARGUMENTS"},
		"team":          map[string]any{"description": "force an ensemble: director plans parallel workers", "template": "/team $ARGUMENTS"},
		"frontier":      map[string]any{"description": "maximum effort: strongest model, strongest version, maxed thinking budget", "template": "/frontier $ARGUMENTS"},
		"init":          initCommandEntry(),
		"captain":       map[string]any{"description": "cheat sheet; the helm: /captain director | <leg> | frontier | quality | auto | reset", "template": "/captain $ARGUMENTS"},
		"euclid":        map[string]any{"description": "Euclid memory: status, or distill the run journal into registers", "template": "/euclid $ARGUMENTS"},
		"oss":           map[string]any{"description": "only open-weight models for this task (composes with /repeat, /team, /quality…)", "template": "/oss $ARGUMENTS"},
		"deterministic": map[string]any{"description": "only legs green in the Agentic Determinism Index, pinned to the measured serving tuple; `captain adi` lists them", "template": "/deterministic $ARGUMENTS"},
		"interrupt":     map[string]any{"description": "stop the running worker without losing its work: it hands off (done / left / resume) and ends; codex and cursor are stopped with what they produced", "template": "/interrupt $ARGUMENTS"},
		"btw":           map[string]any{"description": "a note for the worker already running: complements or amends the prompt it started with (claude and opencode legs take it mid-run; codex and cursor as the next turn)", "template": "/btw $ARGUMENTS"},
		"repeat":        map[string]any{"description": "re-run a prompt after each finish: /repeat 10 <prompt>, /repeat <prompt> (open-ended); /repeat finish (or wrapup) ends it after the current round, /repeat abort now, /repeat status|show|watch", "template": "/repeat $ARGUMENTS"},
	}
	for _, l := range AllLegs { // every worker leg gets a forcing command; hand-written ones above win
		if !ServesTasks(l) {
			continue // a decision leg takes questions, not a prompt
		}
		if _, has := c[string(l)]; !has {
			c[string(l)] = legCommandEntry(l)
		}
	}
	return c
}

// legCommandEntry is the TUI slash command that forces one leg.
func legCommandEntry(l Leg) map[string]any {
	desc := "force the " + string(l) + " leg"
	if s, ok := specs[l]; ok && s.Display != "" {
		desc = "force the " + string(l) + " leg - " + s.Display
	}
	return map[string]any{"description": desc, "template": "/" + string(l) + " $ARGUMENTS"}
}

// captainModelEntries is every model id the brain serves, as the captain
// provider block lists them: all legs plus the team/frontier/workflow
// pseudo-models. The fork validates model ids against this block, so a leg
// missing here is unreachable from the TUI.
func captainModelEntries() map[string]any {
	m := map[string]any{
		"team":     map[string]any{"name": "Team (captain · director-planned workers)"},
		"frontier": map[string]any{"name": "Frontier (captain · claude max effort)"},
		"workflow": map[string]any{"name": "Workflow (captain · your own topology)"},
		// Every prompt without a forced prefix is sent as captain/auto: the
		// brain routes inside the turn. opencode validates model ids against
		// THIS block, not the brain's /v1/models, so a config without "auto"
		// fails every unprefixed prompt with ProviderModelNotFoundError -
		// including `/repeat 5 /codex-cli …`, which is not a leg prefix
		// (live 2026-09-12).
		"auto": map[string]any{"name": "Auto (captain · routed by the director)"},
	}
	for _, l := range AllLegs {
		if !ServesTasks(l) {
			continue // not a model a turn can be sent to
		}
		m[string(l)] = captainModelEntry(l)
	}
	return m
}

// captainModelEntry is one leg's entry in the captain provider block: its
// display name, and the price and context window opencode needs to turn
// the usage the brain reports into the Context panel's "$ spent" and
// "% used" (2026-09-18: with neither declared the panel sat at $0.00 and
// 0% for every turn). A subscription leg prices at 0 - its marginal cost.
func captainModelEntry(l Leg) map[string]any {
	e := map[string]any{"name": LegDisplayName(l)}
	spec, ok := Spec(l)
	if !ok {
		return e
	}
	if spec.Ctx > 0 {
		e["limit"] = map[string]any{"context": spec.Ctx, "output": 32768}
	}
	cost := map[string]any{"input": 0, "output": 0}
	if !spec.Subscription && (spec.PriceIn > 0 || spec.PriceOut > 0) {
		cost = map[string]any{"input": spec.PriceIn, "output": spec.PriceOut}
	}
	e["cost"] = cost
	return e
}

// refreshCaptainModelEntries updates the limit and cost of every captain
// model entry already in cfg (names the user edited are kept). Reports
// whether anything changed.
func refreshCaptainModelEntries(cfg map[string]any) bool {
	providers, _ := cfg["provider"].(map[string]any)
	captain, ok := providers["captain"].(map[string]any)
	if !ok {
		return false
	}
	models, ok := captain["models"].(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for id, raw := range models {
		cur, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		want := captainModelEntry(Leg(id))
		for _, k := range []string{"limit", "cost"} {
			v, has := want[k]
			if !has {
				continue
			}
			// Compare as JSON: the file's numbers come back as float64, the
			// registry's as int, and a text compare called every run a change.
			a, _ := json.Marshal(cur[k])
			b, _ := json.Marshal(v)
			if string(a) != string(b) {
				cur[k] = v
				changed = true
			}
		}
	}
	return changed
}

// cliTools are the agent CLIs a leg may need on PATH.
func cliTools() []string { return []string{"opencode", "claude", "cursor-agent", "codex"} }

// pickDirectorForInit chooses a director leg whose driving binary is present on PATH.
// Prefers claude if installed; otherwise the first opencode-served director leg
// (auth may still be required; doctor will surface that). Falls back to claude.
func pickDirectorForInit() string {
	if _, err := exec.LookPath("claude"); err == nil {
		return "claude"
	}
	if _, err := exec.LookPath("opencode"); err == nil {
		for _, c := range DirectorCandidates() {
			if spec, ok := Spec(c.Leg); ok && spec.Transport == TransportOpencode {
				return string(c.Leg)
			}
		}
	}
	return "claude"
}

// pickFallbackForInit prefers an installed worker leg (cursor > codex > claude > ...)
// so that CAPTAIN_FALLBACK_LEG is usable on this machine.
func pickFallbackForInit() string {
	prefs := []string{"cursor", "codex", "claude", "glm", "grok"}
	bin := func(l string) string {
		switch l {
		case "cursor":
			return "cursor-agent"
		case "codex":
			return "codex"
		case "claude":
			return "claude"
		default:
			return "opencode"
		}
	}
	for _, p := range prefs {
		if _, err := exec.LookPath(bin(p)); err == nil {
			return p
		}
	}
	return "claude"
}

// starterConfig is written when no opencode config exists at all: the captain
// provider (every leg the brain serves), the routing slash commands, the free
// leg for titles, and the canonical permission block. NIM/Scaleway providers
// use {env:} refs - opencode silently drops a provider whose env is unset, so
// they are harmless until the key exists.
// captainProviderOptions is the captain provider's connection block. The
// header names the folder the TUI is open in - the launcher exports
// CAPTAIN_CWD to the TUI and opencode substitutes {env:} when it loads the
// config - so one brain can serve several TUIs, each in its own repo
// (brain_workspace.go). Without it every worker ran in the brain's folder.
func captainProviderOptions() map[string]any {
	return map[string]any{
		"baseURL": "http://127.0.0.1:14097/v1",
		"apiKey":  "captain",
		"headers": map[string]any{"X-Captain-Cwd": "{env:CAPTAIN_CWD}"},
	}
}

func starterConfig() map[string]any {
	return map[string]any{
		"$schema":     "https://opencode.ai/config.json",
		"small_model": "captain/free",
		"command":     defaultCommands(),
		"provider": map[string]any{
			"captain": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"name":    "Captain",
				"options": captainProviderOptions(),
				"models":  captainModelEntries(),
			},
			// "nim": uncatalogued id ON PURPOSE - opencode merges a provider
			// named like a models.dev provider with its bundled catalog and
			// DROPS custom models the catalog lacks.
			"nim": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"name":    "NVIDIA NIM (direct)",
				"options": map[string]any{"baseURL": "https://integrate.api.nvidia.com/v1", "apiKey": "{env:NVIDIA_API_KEY}"},
				"models": map[string]any{
					"z-ai/glm-5.3": map[string]any{"name": "GLM-5.3 (OpenRouter)"},
				},
			},
		},
		"agent":      map[string]any{"title": map[string]any{"model": "captain/free"}},
		"permission": canonicalPermission(),
	}
}

// StripJSONC removes // and /* */ comments (outside strings) and trailing
// commas so a hand-maintained opencode.jsonc can be parsed with encoding/json.
func StripJSONC(src []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++ // skip the trailing '/'
		default:
			out = append(out, c)
		}
	}
	// Trailing commas: a ',' whose next non-whitespace is '}' or ']'.
	var clean []byte
	for i := 0; i < len(out); i++ {
		if out[i] == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		clean = append(clean, out[i])
	}
	return clean
}

func configHome() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config")
}

// OpencodeConfigPath is the global opencode config both the fork TUI and the
// worker `opencode serve` read.
func OpencodeConfigPath() string { return filepath.Join(configHome(), "opencode", "opencode.jsonc") }

// CaptainEnvPath is sourced (set -a) by captain-code.sh before starting the
// brain, so the brain AND any opencode serve it spawns inherit it.
func CaptainEnvPath() string { return filepath.Join(configHome(), "captain", "env") }

// EnsureOpencodeConfig creates the config if missing, otherwise normalizes its
// permission surface: canonical top-level block, per-agent permission
// overrides removed, /init command entry added. Everything else is preserved.
// apply=false reports without writing. Comments in an existing file are not
// preserved on rewrite (the file is re-marshaled).
func EnsureOpencodeConfig(path string, apply bool) (changed bool, notes []string, err error) {
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return false, nil, readErr
		}
		notes = append(notes,
			"created "+path+" (captain provider, routing commands, worker permissions)")
		if apply {
			if err := writeConfig(path, starterConfig()); err != nil {
				return false, nil, err
			}
		}
		return true, notes, nil
	}

	var cfg map[string]any
	if err := json.Unmarshal(StripJSONC(raw), &cfg); err != nil {
		return false, nil, fmt.Errorf("%s does not parse (left untouched): %w", path, err)
	}

	if permChanged, permNotes := normalizePermissionSurface(cfg, true); permChanged {
		notes = append(notes, permNotes...)
		changed = true
	}

	cmds, ok := cfg["command"].(map[string]any)
	if !ok {
		cmds = map[string]any{}
		cfg["command"] = cmds
	}
	if _, has := cmds["init"]; !has {
		cmds["init"] = initCommandEntry()
		notes = append(notes, "added the /init slash command to the TUI")
		changed = true
	}

	// Legs the brain serves that the TUI cannot reach: the fork validates
	// model ids against the captain provider block and the slash palette only
	// knows the commands in the file. Fill what is missing, never touch what
	// exists (codex-cli needed both, 2026-09-09).
	if added := addMissingCaptainEntries(cfg, cmds); len(added) > 0 {
		notes = append(notes, "added captain model/command entries for: "+strings.Join(added, ", "))
		changed = true
	}
	if ensureWorkspaceHeader(cfg) {
		notes = append(notes, "captain provider now sends X-Captain-Cwd, so one brain serves every open TUI in its own folder")
		changed = true
	}

	if changed && apply {
		if !json.Valid(StripJSONC(raw)) {
			return false, nil, fmt.Errorf("refusing to rewrite %s: original no longer valid", path)
		}
		if bytes := StripJSONC(raw); len(bytes) != len(raw) {
			notes = append(notes, "note: comments in the existing file were not preserved")
		}
		if err := writeConfig(path, cfg); err != nil {
			return false, nil, err
		}
	}
	return changed, notes, nil
}

// ensureWorkspaceHeader adds the workspace header to an existing captain
// provider that predates it; other headers the user set are kept.
func ensureWorkspaceHeader(cfg map[string]any) bool {
	providers, _ := cfg["provider"].(map[string]any)
	captain, ok := providers["captain"].(map[string]any)
	if !ok {
		return false
	}
	options, ok := captain["options"].(map[string]any)
	if !ok {
		options = map[string]any{}
		captain["options"] = options
	}
	headers, ok := options["headers"].(map[string]any)
	if !ok {
		headers = map[string]any{}
		options["headers"] = headers
	}
	want := captainProviderOptions()["headers"].(map[string]any)["X-Captain-Cwd"]
	if headers["X-Captain-Cwd"] == want {
		return false
	}
	headers["X-Captain-Cwd"] = want
	return true
}

// addMissingCaptainEntries adds absent captain-provider model ids and leg
// slash commands to cfg, returning the ids it added (sorted, deduplicated).
// A config with no captain provider at all is left alone - /init creates the
// provider only from scratch, it does not retrofit one.
func addMissingCaptainEntries(cfg map[string]any, cmds map[string]any) []string {
	added := map[string]bool{}
	providers, _ := cfg["provider"].(map[string]any)
	if captain, ok := providers["captain"].(map[string]any); ok {
		models, ok := captain["models"].(map[string]any)
		if !ok {
			models = map[string]any{}
			captain["models"] = models
		}
		for id, entry := range captainModelEntries() {
			if _, has := models[id]; !has {
				models[id] = entry
				added[id] = true
			}
		}
		if refreshCaptainModelEntries(cfg) {
			added["(model limits and prices)"] = true
		}
	}
	for id, entry := range defaultCommands() {
		if id == "init" || id == "quality" || id == "best" || id == "speed" || id == "save" {
			continue // routing preferences and /init are handled elsewhere / are the user's wording
		}
		if _, has := cmds[id]; !has {
			cmds[id] = entry
			added[id] = true
		}
	}
	out := make([]string, 0, len(added))
	for id := range added {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// normalizePermissionSurface rewrites cfg's permission story in place:
// canonical top-level block and NO per-agent permission overrides (they merge
// AFTER the global block - agent.ts builds merge(defaults, specifics, user)
// and appends the agent's own cfg last - so a hand- or worker-added
// "*": "allow" there silently re-allowed .env reads). setTopLevel=false only
// replaces a permission block that already exists (project overlays must not
// grow one they never had).
func normalizePermissionSurface(cfg map[string]any, setTopLevel bool) (changed bool, notes []string) {
	_, hasPerm := cfg["permission"]
	if (setTopLevel || hasPerm) && !reflect.DeepEqual(cfg["permission"], canonicalPermission()) {
		cfg["permission"] = canonicalPermission()
		notes = append(notes, "set the permission block: web search/fetch, edits, bash and out-of-repo paths allowed; .env reads denied; doom-loop and question denied (an \"ask\" wedges a headless worker forever)")
		changed = true
	}
	if agents, ok := cfg["agent"].(map[string]any); ok {
		for name, v := range agents {
			a, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if _, has := a["permission"]; has {
				delete(a, "permission")
				notes = append(notes, "removed agent."+name+".permission - agent-level rules merge AFTER the global block, so a \"*\": \"allow\" there silently re-allowed .env reads")
				changed = true
			}
		}
	}
	return changed, notes
}

// EnsureProjectOverlays normalizes the permission surface of any opencode
// config the project directory carries (.opencode/opencode.json[c] or
// opencode.json[c] at the root) - these merge OVER the global config, so a
// stray overlay reintroduces every trap init just removed (live 2026-08-13: a
// worker-written overlay in the active repo did exactly that). Overlays are
// never created, and TUI command entries are never added to them.
func EnsureProjectOverlays(dir string, apply bool) (changed bool, notes []string, err error) {
	if dir == "" {
		return false, nil, nil
	}
	candidates := []string{
		filepath.Join(dir, ".opencode", "opencode.jsonc"),
		filepath.Join(dir, ".opencode", "opencode.json"),
		filepath.Join(dir, "opencode.jsonc"),
		filepath.Join(dir, "opencode.json"),
	}
	for _, path := range candidates {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return changed, notes, readErr
		}
		var cfg map[string]any
		if err := json.Unmarshal(StripJSONC(raw), &cfg); err != nil {
			notes = append(notes, "⚠ "+path+" does not parse - left untouched: "+err.Error())
			continue
		}
		fileChanged, fileNotes := normalizePermissionSurface(cfg, false)
		if !fileChanged {
			continue
		}
		changed = true
		notes = append(notes, "project overlay "+path+":")
		notes = append(notes, fileNotes...)
		if apply {
			if err := writeConfig(path, cfg); err != nil {
				return changed, notes, err
			}
		}
	}
	return changed, notes, nil
}

func writeConfig(path string, cfg map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

const captainEnvScaffoldTemplate = `
# captain env - sourced by captain-code.sh (set -a) before starting the brain,
# so the brain AND any opencode serve it spawns inherit these.
# Values below were derived from legs present on PATH at 'captain init' time.
# Edit and 'captain brain' (or launcher restart) to change the helm.
CAPTAIN_DIRECTOR=%s
CAPTAIN_ROUTE_TIMEOUT_MS=30000
CAPTAIN_FALLBACK_LEG=%s
# Restrict which legs may run (comma list). Unset = all wired legs.
#CAPTAIN_LEGS=free,grok,codex,claude,cursor,glm,minimax
# NVIDIA NIM key - enables the kimi leg (NIM retired its GLM and MiniMax lines).
#NVIDIA_API_KEY=
# Self-hosted Qwen (OpenAI root incl /v1) - enables the qwen leg.
#CAPTAIN_SCALEWAY_BASE_URL=
# Artificial Analysis key for 'captain priors sync' and the live perf ranking.
#CAPTAIN_AA_API_KEY=
# Secrets on the wire (docs/SECRETS.md): on|off|secrets|strict. Default on.
#CAPTAIN_REDACT=on
# Free leg model - the opencode zen free catalog rotates; when the free leg
# 500s, list live models (curl :14096/config/providers) and repoint here.
#CAPTAIN_FREE_MODEL=hy3-free
`

func captainEnvScaffold() string {
	d := pickDirectorForInit()
	f := pickFallbackForInit()
	if f == d {
		for _, alt := range []string{"cursor", "codex", "glm", "grok", "claude"} {
			if alt != d {
				f = alt
				break
			}
		}
	}
	return fmt.Sprintf(captainEnvScaffoldTemplate, d, f)
}

// EnsureCaptainEnv scaffolds ~/.config/captain/env when missing. An existing
// file is the user's - never modified.
func EnsureCaptainEnv(path string, apply bool) (changed bool, notes []string, err error) {
	if _, statErr := os.Stat(path); statErr == nil {
		return false, nil, nil
	} else if !os.IsNotExist(statErr) {
		return false, nil, statErr
	}
	notes = append(notes, "created "+path+" (director, route budget, fallback leg; API keys commented out)")
	if apply {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, nil, err
		}
		if err := os.WriteFile(path, []byte(captainEnvScaffold()), 0o644); err != nil {
			return false, nil, err
		}
	}
	return true, notes, nil
}

// InitOptions parameterizes RunInit; zero-value paths use the real locations.
type InitOptions struct {
	Apply        bool
	OpencodePath string
	EnvPath      string
	// ProjectDir is scanned for opencode overlays that merge over the global
	// config. Empty falls back to CAPTAIN_CWD, then the working directory.
	ProjectDir string
}

// RunInit generates/normalizes every config captain owns and returns a
// human-readable report. Used by `captain init` and the brain's /init.
func RunInit(opts InitOptions) (report string, changed bool, err error) {
	ocPath := opts.OpencodePath
	if ocPath == "" {
		ocPath = OpencodeConfigPath()
	}
	envPath := opts.EnvPath
	if envPath == "" {
		envPath = CaptainEnvPath()
	}

	var b strings.Builder
	verb := "applied"
	if !opts.Apply {
		verb = "would apply (check mode - nothing written)"
	}
	fmt.Fprintf(&b, "captain init - %s\n\n", verb)

	ocChanged, ocNotes, ocErr := EnsureOpencodeConfig(ocPath, opts.Apply)
	if ocErr != nil {
		return "", false, ocErr
	}
	section(&b, "opencode config ("+ocPath+")", ocChanged, ocNotes)

	projDir := opts.ProjectDir
	if projDir == "" {
		projDir = os.Getenv("CAPTAIN_CWD")
	}
	if projDir == "" {
		projDir, _ = os.Getwd()
	}
	projChanged, projNotes, projErr := EnsureProjectOverlays(projDir, opts.Apply)
	if projErr != nil {
		return "", false, projErr
	}
	if projChanged || len(projNotes) > 0 {
		section(&b, "project overlays ("+projDir+")", projChanged, projNotes)
	}

	envChanged, envNotes, envErr := EnsureCaptainEnv(envPath, opts.Apply)
	if envErr != nil {
		return "", false, envErr
	}
	section(&b, "captain env ("+envPath+")", envChanged, envNotes)

	b.WriteString("\nCLI legs (no config needed):\n")
	if v := os.Getenv("CAPTAIN_CLAUDE_PERMISSIONS"); v != "" && v != "skip" {
		fmt.Fprintf(&b, "  • claude: CAPTAIN_CLAUDE_PERMISSIONS=%s overrides the default full-permission mode\n", v)
	} else {
		b.WriteString("  ✓ claude -p runs with --dangerously-skip-permissions (sandbox thesis: the repo is the blast radius)\n")
	}
	b.WriteString("  ✓ cursor-agent runs with --trust\n")
	if v := os.Getenv("CAPTAIN_CODEX_CLI_SANDBOX"); v != "" && v != "skip" {
		fmt.Fprintf(&b, "  • codex-cli: codex exec --sandbox %s (approvals never)\n", v)
	} else {
		b.WriteString("  ✓ codex-cli: codex exec runs with --dangerously-bypass-approvals-and-sandbox (gpt-6-astra, effort per request)\n")
	}

	b.WriteString("\nAgent CLIs on PATH:\n")
	for _, tool := range cliTools() {
		if p, lookErr := exec.LookPath(tool); lookErr == nil {
			fmt.Fprintf(&b, "  ✓ %s (%s)\n", tool, p)
		} else {
			fmt.Fprintf(&b, "  ✗ %s not found - that leg cannot run\n", tool)
		}
	}

	b.WriteString("\nWorkers may now search online, fetch pages, edit files and run commands without asking.\nStill blocked: reading .env / .env.* files (secrets stay out of prompts).\n")
	changed = ocChanged || projChanged || envChanged
	if changed && opts.Apply {
		b.WriteString("\nConfig is read at startup - restart `opencode serve` and the TUI to apply\n(`captain upgrade` restarts both when idle, or relaunch captain-code.sh).\n")
	}
	return b.String(), changed, nil
}

func section(b *strings.Builder, title string, changed bool, notes []string) {
	fmt.Fprintf(b, "%s:\n", title)
	if !changed {
		b.WriteString("  ✓ already correct\n")
		return
	}
	for _, n := range notes {
		fmt.Fprintf(b, "  • %s\n", n)
	}
}

// EnsureProviderModel lists model under provider.<provider>.models in the
// opencode config when that provider block exists and lacks it. Catalogued
// providers (openrouter) do not need it; uncatalogued ones (nim) do - an
// unlisted model there is an opaque 500 (2026-08-24). Returns whether it
// wrote. A missing provider block is left alone: /init never invents
// credentials.
func EnsureProviderModel(path, provider, model, display string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(StripJSONC(raw), &cfg); err != nil {
		return false, fmt.Errorf("%s does not parse: %w", path, err)
	}
	providers, _ := cfg["provider"].(map[string]any)
	block, ok := providers[provider].(map[string]any)
	if !ok {
		return false, nil
	}
	models, ok := block["models"].(map[string]any)
	if !ok {
		models = map[string]any{}
		block["models"] = models
	}
	if _, has := models[model]; has {
		return false, nil
	}
	models[model] = map[string]any{"name": display}
	return true, writeConfig(path, cfg)
}

// EnsureEuclidMCP registers `captain euclid mcp` as a local MCP server in the
// opencode config (opencode's `mcp.<name>` block: type local, command, enabled)
// when absent. Idempotent; never rewrites an existing entry - an operator may
// have pointed it elsewhere. Returns whether it wrote.
func EnsureEuclidMCP(path, captainExe string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(StripJSONC(raw), &cfg); err != nil {
		return false, fmt.Errorf("%s does not parse: %w", path, err)
	}
	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		mcp = map[string]any{}
		cfg["mcp"] = mcp
	}
	if _, has := mcp["euclid"]; has {
		return false, nil
	}
	if captainExe == "" {
		captainExe = "captain"
	}
	mcp["euclid"] = map[string]any{"type": "local", "command": []string{captainExe, "euclid", "mcp"}, "enabled": true}
	return true, writeConfig(path, cfg)
}

// The two plugin halves go in DIFFERENT config files, which is not obvious and
// cost a wrong first fix: opencode.jsonc feeds the server plugin loader, while
// the TUI reads tui.jsonc / tui.json (ConfigPaths.files("tui", …)). A UI plugin
// listed in opencode.jsonc is silently never loaded - the panels simply do not
// appear, and the only log line is the server loader complaining that the
// module has no server().
var (
	captainServerPlugin = filepath.Join("plugin", "captain.ts")
	captainUIPlugin     = filepath.Join("plugin", "captain-ui", "index.tsx")
)

// TuiConfigPath is the TUI's own config, beside opencode's.
func TuiConfigPath() string { return filepath.Join(configHome(), "opencode", "tui.json") }

// EnsureCaptainPlugins registers the router in opencode's config and the panels
// in the TUI's config. This is what replaced the fork: stock opencode gets
// captain routing, the sidebar, the wordmark and the director tag.
//
// Entries are absolute file:// URLs because a GUI-launched opencode inherits
// neither a shell PATH nor a useful cwd, and a path that does not exist is
// skipped rather than registered - opencode logs a load failure for each
// missing entry, which looks like a broken install.
func EnsureCaptainPlugins(path, src string) (bool, error) {
	changed, err := ensurePluginEntry(path, filepath.Join(src, captainServerPlugin), captainServerPlugin)
	if err != nil {
		return false, err
	}
	uiChanged, err := ensurePluginEntry(TuiConfigPath(), filepath.Join(src, captainUIPlugin), captainUIPlugin)
	if err != nil {
		return changed, err
	}
	return changed || uiChanged, nil
}

// ensurePluginEntry adds one plugin to one config file, creating the file when
// it does not exist yet (the TUI config usually does not).
func ensurePluginEntry(cfgPath, abs, rel string) (bool, error) {
	if _, err := os.Stat(abs); err != nil {
		return false, nil // not an install that carries the plugin sources
	}
	cfg := map[string]any{}
	body, err := os.ReadFile(cfgPath)
	switch {
	case err == nil:
		if err := json.Unmarshal(StripJSONC(body), &cfg); err != nil {
			return false, fmt.Errorf("%s does not parse: %w", cfgPath, err)
		}
	case !os.IsNotExist(err):
		return false, err
	}
	existing, _ := cfg["plugin"].([]any)
	for _, v := range existing {
		if s, ok := v.(string); ok && strings.Contains(s, rel) {
			return false, nil
		}
	}
	cfg["plugin"] = append(existing, "file://"+abs)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return false, err
	}
	return true, writeConfig(cfgPath, cfg)
}

// TuiKVPath is the TUI's preference store (Global.Path.state + kv.json).
func TuiKVPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "opencode", "kv.json")
}

// ClaudeSettingsPath is Claude Code's user settings file.
func ClaudeSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// EnsureClaudeWorkerPermissions removes permissions.blockReadsOutsideWorkingDirectories
// from Claude Code's user settings. It is a user-level switch ("true in any
// settings source wins", so a worker cannot override it), and under it
// `claude -p` refuses any shell command it cannot statically prove stays
// inside the working directory: every `$VAR`, `$(…)`, redirect, computed
// `cd` and sed script - a worker's bread and butter. An arc worker lost run
// after run to "Contains expansion; under the read block … refused"
// (2026-09-13). Workers already run with --dangerously-skip-permissions and
// widened --add-dir; the switch only starves them.
func EnsureClaudeWorkerPermissions() (bool, string, error) {
	path := ClaudeSettingsPath()
	if path == "" {
		return false, "", nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", err
	}
	var cfg map[string]any
	if err := json.Unmarshal(StripJSONC(body), &cfg); err != nil {
		return false, "", fmt.Errorf("%s does not parse (left untouched): %w", path, err)
	}
	perms, ok := cfg["permissions"].(map[string]any)
	if !ok {
		return false, "", nil
	}
	if v, set := perms["blockReadsOutsideWorkingDirectories"]; !set || v != true {
		return false, "", nil
	}
	delete(perms, "blockReadsOutsideWorkingDirectories")
	if len(perms) == 0 {
		delete(cfg, "permissions")
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return false, "", err
	}
	return true, "removed permissions.blockReadsOutsideWorkingDirectories from " + path + " - it made claude workers refuse every shell command with a $expansion, redirect or computed path", nil
}

// EnsureThinkingShown seeds the TUI's thinking_mode when the user has never
// set it. Captain narrates every run on the reasoning channel; in the TUI's
// "hide" mode that block folds to ONE line - "captain · <leg>" with a
// spinner - and a click on it opens the full feed, a click closes it again;
// "show" keeps it open. "show" was the seed while the feed was the only sign
// of life (2026-09-11); with the feed now carrying tool lines, outcomes and
// the worker's own words, the collapsed line is what a busy TUI should show
// and the details are one click away (2026-09-13). `/thinking` toggles the
// mode at any time; an explicit choice, either way, is left alone.
func EnsureThinkingShown() (bool, error) {
	path := TuiKVPath()
	kv := map[string]any{}
	body, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(body, &kv); err != nil {
			return false, fmt.Errorf("%s does not parse: %w", path, err)
		}
	case !os.IsNotExist(err):
		return false, err
	}
	if _, set := kv["thinking_mode"]; set {
		return false, nil
	}
	kv["thinking_mode"] = "hide"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	out, _ := json.MarshalIndent(kv, "", "  ")
	return true, os.WriteFile(path, out, 0o644)
}
