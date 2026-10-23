package captaincode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/internal/presence"
)

const PresenceMarker = "[captain] Signed prompt grant: "
const PresenceEnv = "CAPTAIN_PRESENCE_GRANT"

func PresenceContract(id string) string {
	if id == "" {
		return ""
	}
	return "\n\n" + PresenceMarker + id + "\nThis prompt has passkey approval. It has typed-turn tools in its project: the linked folder, the folders inside it and the worktrees of its repository. A default-branch push, merge, release, publish, delete or payment requires a separate signed decision. A chat reply cannot approve that decision. Later repeat rounds need fresh approval."
}
func PresenceGrantID(prompt string) string {
	i := strings.LastIndex(prompt, PresenceMarker)
	if i < 0 {
		return ""
	}
	rest := prompt[i+len(PresenceMarker):]
	id, _, _ := strings.Cut(rest, "\n")
	raw, e := presence.Decode(id)
	if e != nil || len(raw) != 32 {
		return ""
	}
	return id
}

var presenceHighRE = regexp.MustCompile(`(?i)\b(rm|rmdir|unlink|shred|truncate|goreleaser)\b|\b(delete|destroy|terminate|purchase|payment|charge|transfer|spend|publish|release)\b|\bgit\b[^\n]*\bbranch\b[^\n]*\s(-d\b|--delete\b)|\bgh\s+pr\s+merge\b|\b(docker|gem)\s+(image\s+)?push\b`)

// presenceGitHigh finds a git subcommand that rewrites or discards history
// or work: merge, tag (not a listing), reset, clean, rebase, filter-repo,
// filter-branch. It reads the subcommand, not the words around it: on 23 Oct
// `git merge-base`, a merge in a --grep pattern and a path held a signed
// turn for approval. Every git invocation in the command counts, in a
// pipeline, a list or a subshell.
func presenceGitHigh(command string) bool {
	subs := presenceGitSubcommands(command)
	for _, sub := range []string{"merge", "reset", "clean", "rebase", "filter-repo", "filter-branch", "tag"} {
		if subs[sub] {
			return true
		}
	}
	return false
}

// presenceGitSubcommands is the set of git subcommands a shell command runs,
// in a pipeline, a list or a subshell, past git's global options and their
// values. "tag" is only listed when it creates or deletes a tag.
func presenceGitSubcommands(command string) map[string]bool {
	subs := map[string]bool{}
	for _, seg := range presenceShellSplit.Split(command, -1) {
		f := strings.Fields(seg)
		for i := 0; i < len(f); i++ {
			if filepath.Base(strings.Trim(f[i], `"'`)) != "git" {
				continue
			}
			j := i + 1
			for j < len(f) && strings.HasPrefix(f[j], "-") {
				switch f[j] {
				case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env":
					j++ // the option's value
				}
				j++
			}
			if j >= len(f) {
				break
			}
			sub := strings.Trim(f[j], `"'`)
			if sub == "tag" && !(j+1 < len(f) && f[j+1] != "-l" && f[j+1] != "--list" && f[j+1] != "-n" && !strings.HasPrefix(f[j+1], "--sort") && f[j+1] != "--contains" && f[j+1] != "--points-at") {
				sub = "tag-list"
			}
			subs[sub] = true
			i = j
		}
	}
	return subs
}

// presencePlainPushOnly: the command is high-tier only for plain pushes of
// this project: no force, delete, tags, mirror or --all, no + or : refspec,
// and any -C inside the project. Everything else in it must pass on its own.
func presencePlainPushOnly(a GateAction, dir string) bool {
	if !shellTools[strings.ToLower(a.Tool)] {
		return false
	}
	segs := presenceShellSplit.Split(a.Command, -1)
	rest := a.Command
	pushes := 0
	for _, seg := range segs {
		f := strings.Fields(seg)
		for i := 0; i < len(f); i++ {
			if filepath.Base(strings.Trim(f[i], `"'`)) != "git" {
				continue
			}
			j := i + 1
			for j < len(f) && strings.HasPrefix(f[j], "-") {
				switch f[j] {
				case "-C":
					if j+1 >= len(f) {
						return false
					}
					target := strings.Trim(f[j+1], `"'`)
					if !filepath.IsAbs(target) {
						target = filepath.Join(a.Cwd, target)
					}
					if !presenceFolderAllowed(dir, target) {
						return false
					}
					j++
				case "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env":
					j++
				}
				j++
			}
			if j >= len(f) || strings.Trim(f[j], `"'`) != "push" {
				i = j
				continue
			}
			for _, arg := range f[j+1:] {
				arg = strings.Trim(arg, `"'`)
				switch {
				case arg == "-f", strings.HasPrefix(arg, "--force"), arg == "--mirror", arg == "--delete", arg == "-d",
					arg == "--tags", arg == "--follow-tags", arg == "--all", arg == "--prune", strings.HasPrefix(arg, "--push-option"), arg == "-o",
					strings.HasPrefix(arg, "+"), strings.HasPrefix(arg, ":"), strings.Contains(arg, "refs/tags"):
					return false
				}
			}
			pushes++
			rest = strings.Replace(rest, strings.TrimSpace(seg), "true", 1)
			break
		}
	}
	if pushes == 0 {
		return false
	}
	// What is left once the pushes are taken out must need no decision.
	return !PresenceHighTier(GateAction{Tool: a.Tool, Command: rest, Path: a.Path, Cwd: a.Cwd})
}

var presenceShellSplit = regexp.MustCompile("[;&|\n()`]+")

func PresenceHighTier(a GateAction) bool {
	tool := strings.ToLower(a.Tool)
	if presenceHighRE.MatchString(tool) {
		return true
	}
	if !shellTools[tool] {
		if tool == "patch" || tool == "apply_patch" {
			if strings.Contains(a.Command, "*** Delete File:") || strings.Contains(a.Command, "+++ /dev/null") {
				return true
			}
		}
		return !sentTurnTools[tool]
	}
	for _, rule := range publishRules {
		if rule.re.MatchString(a.Command) {
			return true
		}
	}
	if presenceHighRE.MatchString(a.Command) || presenceGitHigh(a.Command) {
		return true
	}
	// push by its subcommand too: Claude Code's own background git calls carry
	// `-c hook.pre-push.enabled=false`, and the word "push" in that option made
	// git status, git config and git log wait for approval (23 Oct).
	if presenceGitSubcommands(a.Command)["push"] {
		return !presenceFeaturePush(a.Command, a.Cwd)
	}
	return false
}
func presenceFeaturePush(command, dir string) bool {
	// Output handling after the push changes nothing it sends: 23 Oct, a
	// worker's `git -C <project> push origin feature 2>&1 | tail -6` waited
	// for a signed decision like a push to main.
	command = presencePushEcho.ReplaceAllString(command, "")
	command = presencePushTail.ReplaceAllString(command, "")
	if strings.ContainsAny(command, ";&|\n\r`$\\'\"<>()") {
		return false
	}
	words := strings.Fields(command)
	if len(words) == 6 && words[0] == "git" && words[1] == "-C" {
		target := words[2]
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		// -C may only point into the same repository as the turn's folder.
		if c := presenceGitCommon(presenceRealPath(target)); c == "" || c != presenceGitCommon(presenceRealPath(dir)) {
			return false
		}
		dir, words = target, append([]string{"git"}, words[3:]...)
	}
	if len(words) != 4 || words[0] != "git" || words[1] != "push" || words[2] != "origin" {
		return false
	}
	ref := words[3]
	// HEAD:<branch> and HEAD:refs/heads/<branch> push the current commit to
	// that branch: the branch is what decides.
	if after, ok := strings.CutPrefix(ref, "HEAD:"); ok {
		ref = strings.TrimPrefix(after, "refs/heads/")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]*$`).MatchString(ref) || ref == "HEAD" || strings.Contains(ref, "..") || strings.HasPrefix(ref, "refs/") {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	cmd.Dir = dir
	raw, e := cmd.Output()
	if e != nil {
		return false
	}
	def := strings.TrimSpace(strings.TrimPrefix(string(raw), "refs/remotes/origin/"))
	return ref != def && ref != "main" && ref != "master"
}

// presencePushEcho is a trailing `; echo "push exit $?"`: it prints, it sends nothing.
var presencePushEcho = regexp.MustCompile(`\s*;\s*echo\s+"[^"$` + "`" + `]*(\$\?)?[^"$` + "`" + `]*"\s*$`)

var presencePushTail = regexp.MustCompile(`\s*(2>&1)?\s*(\|\s*(tail|head)(\s+-n)?(\s+-?[0-9]+)?)?\s*$`)

// presenceFolderAllowed: a signed turn works in its project - the linked
// folder, a folder inside it, or a linked worktree of the same repository
// (captain's scratch worktrees included). On 23 Oct a worker that ran one
// `cd` elsewhere had every later tool call refused, even Read, and could not
// get back.
func presenceFolderAllowed(dir, cwd string) bool {
	d, c := presenceRealPath(dir), presenceRealPath(cwd)
	if d == "" || c == "" {
		return false
	}
	if c == d || strings.HasPrefix(c, d+string(filepath.Separator)) {
		return true
	}
	dc, cc := presenceGitCommon(d), presenceGitCommon(c)
	return dc != "" && dc == cc
}

// presenceEffectiveCwd is where a shell command runs: a leading `cd <dir>`
// moves it, so a worker can always step back into its project.
func presenceEffectiveCwd(a GateAction) string {
	if !shellTools[strings.ToLower(a.Tool)] {
		return a.Cwd
	}
	m := presenceLeadingCd.FindStringSubmatch(a.Command)
	if m == nil {
		return a.Cwd
	}
	target := strings.Trim(m[1], `"'`)
	if target == "~" || strings.HasPrefix(target, "~/") {
		if home, e := os.UserHomeDir(); e == nil {
			target = filepath.Join(home, strings.TrimPrefix(target, "~"))
		}
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(a.Cwd, target)
	}
	return target
}

var presenceLeadingCd = regexp.MustCompile(`^\s*cd\s+("[^"]+"|'[^']+'|[^\s;&|]+)\s*(?:&&|;|$)`)

func presenceRealPath(p string) string {
	if p == "" {
		return ""
	}
	if r, e := filepath.EvalSymlinks(filepath.Clean(p)); e == nil {
		return r
	}
	return filepath.Clean(p)
}

// presenceGitCommon is the repository a folder belongs to: its shared .git
// directory, the same for the main checkout and every linked worktree.
func presenceGitCommon(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	out, e := cmd.Output()
	if e != nil {
		return ""
	}
	return presenceRealPath(strings.TrimSpace(string(out)))
}

func PresenceAction(grantID string, a GateAction) error { return presenceAction(grantID, a, false) }
func PresenceActionForHook(grantID string, a GateAction) error {
	return presenceAction(grantID, a, true)
}
func presenceAction(grantID string, a GateAction, hook bool) error {
	if grantID == "" {
		return nil
	}
	g, e := presence.DefaultStore().Grant(grantID)
	if e != nil {
		return e
	}
	if a.Cwd != "" && !presenceFolderAllowed(g.Dir, presenceEffectiveCwd(a)) {
		return fmt.Errorf("signed_folder_mismatch")
	}
	if !PresenceHighTier(a) {
		return nil
	}
	if g.Push && presencePlainPushOnly(a, g.Dir) {
		return nil // the signed prompt asked for this push
	}
	preview := false
	if shellTools[strings.ToLower(a.Tool)] {
		if words, ok := presenceWords(a.Command); ok {
			preview = hook && slices.Contains(guardedTools, words[0])
			canonical, _ := json.Marshal(words)
			a.Command = string(canonical)
		}
	}
	raw, e := json.Marshal(struct {
		Tool    string `json:"tool"`
		Command string `json:"command"`
		Path    string `json:"path"`
		Dir     string `json:"dir"`
	}{a.Tool, a.Command, a.Path, g.Dir})
	if e != nil {
		return e
	}
	if preview {
		return presence.DefaultStore().PreviewAction(grantID, string(raw))
	}
	return presence.DefaultStore().CheckAction(grantID, string(raw))
}

func PresenceClaudeSettings() string {
	raw, _ := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": shellQuote(CaptainBinary()) + " gate --presence-hook", "timeout": 600}}}}}})
	return string(raw)
}

func presenceWords(command string) ([]string, bool) {
	var words []string
	s := strings.TrimSpace(command)
	for s != "" {
		word, rest, ok := gateLiteralWord(s)
		if !ok || len(rest) == len(s) {
			return nil, false
		}
		words = append(words, word)
		if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
			return nil, false
		}
		s = strings.TrimSpace(rest)
	}
	return words, len(words) > 0
}
