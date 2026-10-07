package captaincode

// The worker guard: what no worker does unless the user's typed request asks
// for it, whatever leg it runs on. A cheap leg asked to "build it" committed
// another session's uncommitted work, pushed a main that failed CI, then
// tagged and published a public release nobody asked for (2026-10-07). The
// guard refuses, at the tool boundary:
//
//   - publishing: creating or pushing tags, creating or editing releases,
//     publishing packages or images - unless the user's turn asks for a
//     release or a publish (AsksToPublish);
//   - bulk staging in a checkout that already had uncommitted changes when
//     the turn started (`git add -A`, `git add .`, `git commit -a`): some of
//     those changes are other sessions' work, so the worker stages its own
//     files by name.
//
// The brain decides both from the turn and writes them into the worker
// prompt as captain markers. opencode workers are checked by the plugin
// through the brain (/v1/gate/sent); CLI workers (claude, codex, cursor) run
// with small shims for git, gh and the package tools first on their PATH
// (`captain guard-exec`). Every refusal is a line in
// ~/.captaincode/conduct.jsonl, which the audit reads (audit.go).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// PublishGrantMarker in a worker prompt: the user's turn asked to publish.
const PublishGrantMarker = "[captain] Publishing: the user asked for a release or a publish in this turn"

// DirtyCheckoutMarker in a worker prompt: the folder had uncommitted changes
// before the turn started.
const DirtyCheckoutMarker = "[captain] Shared checkout: this folder had uncommitted changes before this turn"

// Worker guard environment for CLI workers.
const (
	GuardEnv       = "CAPTAIN_WORKER_GUARD" // "1": the shims apply the guard
	MayPublishEnv  = "CAPTAIN_MAY_PUBLISH"
	DirtyBeforeEnv = "CAPTAIN_DIRTY_BEFORE"
)

var publishAskRe = regexp.MustCompile(`(?i)\b(release|publish|re-?tag|tag\s+(it|this|the\s+release|v?\d)|cut\s+(a\s+)?(new\s+)?(version|tag)|ship\s+v?\d|bump\s+(the\s+)?version)`)

// AsksToPublish reports whether the user's own turn asks for a release, a
// tag or a publish. Pushing commits is not publishing.
func AsksToPublish(turn string) bool { return publishAskRe.MatchString(turn) }

// GuardContract is the worker-prompt line for one turn.
func GuardContract(mayPublish, dirtyBefore bool) string {
	var b strings.Builder
	if mayPublish {
		b.WriteString("\n\n" + PublishGrantMarker + ": tags, releases and package publishing are allowed for what it asks.")
	} else {
		b.WriteString("\n\n[captain] Publishing: not requested. Do not create or push tags, create or edit releases, or publish packages or images; captain refuses them. Pushing commits is fine when the user asked for it.")
	}
	if dirtyBefore {
		b.WriteString("\n\n" + DirtyCheckoutMarker + ", and some of them may be other sessions' work. Stage only the files you changed, by name: `git add -A`, `git add .` and `git commit -a` are refused here.")
	}
	return b.String()
}

type guardRule struct {
	why string
	re  *regexp.Regexp
}

var publishRules = []guardRule{
	{"create or push a tag", regexp.MustCompile(`(?i)\bgit\s+(-C\s+\S+\s+)?tag\s+(-[asfmu]\b|--annotate|--sign|--force|[^-\s|;&])|\bgit\s+(-C\s+\S+\s+)?push\b[^|;&]*(--tags|--follow-tags|refs/tags|\s(origin|upstream)\s+:?v?\d+\.\d+)`)},
	{"create, edit or delete a release", regexp.MustCompile(`(?i)\bgh\s+release\s+(create|edit|delete|upload)\b|\bgh\s+api\b[^|;&]*/releases\b|\bgoreleaser\b`)},
	{"publish a package or an image", regexp.MustCompile(`(?i)\b(npm|pnpm|yarn|bun)\s+publish\b|\bcargo\s+publish\b|\btwine\s+upload\b|\bdocker\s+(image\s+)?push\b|\bgem\s+push\b|\bpoetry\s+publish\b`)},
}

var bulkStageRe = regexp.MustCompile(`(?i)\bgit\s+(-C\s+\S+\s+)?add\s+([^|;&]*\s)?(-A|--all|\.|:/|\*)(\s|$|[;&|])|\bgit\s+(-C\s+\S+\s+)?commit\s+([^|;&]*\s)?(-[a-zA-Z]*a[a-zA-Z]*|--all)(\s|$|[;&|])`)

// WorkerGuardRefusal returns why a worker may not run cmd this turn, or "".
func WorkerGuardRefusal(cmd string, mayPublish, dirtyBefore bool) string {
	if !mayPublish {
		for _, r := range publishRules {
			if r.re.MatchString(cmd) {
				return "refused: " + r.why + " - the user did not ask for a release or a publish in this turn. Say what is ready and let the user ask for it."
			}
		}
	}
	if dirtyBefore && bulkStageRe.MatchString(cmd) {
		return "refused: this checkout had uncommitted changes before this turn, some of them other sessions' work. Stage only the files you changed, by name (`git add path/to/file`), and commit those."
	}
	return ""
}

// ConductEvent is one guard refusal, for the audit.
type ConductEvent struct {
	At      time.Time `json:"at"`
	Rule    string    `json:"rule"`
	Command string    `json:"command"`
	Dir     string    `json:"dir,omitempty"`
	Leg     string    `json:"leg,omitempty"`
	TaskID  string    `json:"task_id,omitempty"`
	Session string    `json:"session,omitempty"`
}

// ConductLogPath is ~/.captaincode/conduct.jsonl.
func ConductLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".captaincode", "conduct.jsonl")
}

// AppendConduct records a refusal.
func AppendConduct(e ConductEvent) {
	path := ConductLogPath()
	if path == "" {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.Command = CutHead(Scrub(e.Command), 300)
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	raw, _ := json.Marshal(e)
	_, _ = f.Write(append(raw, '\n'))
}

// ReadConduct returns the refusals recorded since t.
func ReadConduct(since time.Time) []ConductEvent {
	raw, err := os.ReadFile(ConductLogPath())
	if err != nil {
		return nil
	}
	var out []ConductEvent
	for _, line := range strings.Split(string(raw), "\n") {
		var e ConductEvent
		if json.Unmarshal([]byte(line), &e) == nil && !e.At.Before(since) {
			out = append(out, e)
		}
	}
	return out
}

// guardedTools are the programs the CLI shims stand in for.
var guardedTools = []string{"git", "gh", "npm", "pnpm", "yarn", "bun", "cargo", "twine", "docker", "goreleaser", "gem", "poetry"}

// ShimDir is where the shims live.
func ShimDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "shims")
}

// EnsureShims writes the shims, each a two-line script that hands its
// command to `captain guard-exec`, and returns their folder.
func EnsureShims() (string, error) {
	dir := ShimDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, t := range guardedTools {
		body := fmt.Sprintf("#!/bin/sh\nexec %s guard-exec %s \"$@\"\n", shellQuote(CaptainBinary()), t)
		p := filepath.Join(dir, t)
		if old, err := os.ReadFile(p); err == nil && string(old) == body {
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// WorkerGuardEnv is the environment a CLI worker runs the task with: the
// shims first on PATH, and the turn's decisions read from its prompt.
func WorkerGuardEnv(task string) []string {
	dir, err := EnsureShims()
	if err != nil {
		return nil
	}
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	return []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		GuardEnv + "=1",
		MayPublishEnv + "=" + flag(strings.Contains(task, PublishGrantMarker)),
		DirtyBeforeEnv + "=" + flag(strings.Contains(task, DirtyCheckoutMarker)),
	}
}

// RealBinary finds name on PATH, skipping the shim folder.
func RealBinary(name string) (string, error) {
	shims := filepath.Clean(ShimDir())
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d == "" || filepath.Clean(d) == shims {
			continue
		}
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found on PATH", name)
}
