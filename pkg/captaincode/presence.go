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

var presenceHighRE = regexp.MustCompile(`(?i)\b(rm|rmdir|unlink|shred|truncate|goreleaser)\b|\b(delete|destroy|terminate|purchase|payment|charge|transfer|spend|publish|release)\b|\bgit\b[^\n]*\b(merge|tag|reset|clean|rebase|filter-repo|filter-branch)\b|\bgit\b[^\n]*\bbranch\b[^\n]*\s(-d\b|--delete\b)|\bgh\s+pr\s+merge\b|\b(docker|gem)\s+(image\s+)?push\b`)

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
	if presenceHighRE.MatchString(a.Command) {
		return true
	}
	if regexp.MustCompile(`(?i)\bgit\b[^\n]*\bpush\b`).MatchString(a.Command) {
		return !presenceFeaturePush(a.Command, a.Cwd)
	}
	return false
}
func presenceFeaturePush(command, dir string) bool {
	if strings.ContainsAny(command, ";&|\n\r`$\\'\"<>()") {
		return false
	}
	words := strings.Fields(command)
	if len(words) != 4 || words[0] != "git" || words[1] != "push" || words[2] != "origin" {
		return false
	}
	ref := words[3]
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
	raw, _ := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": shellQuote(CaptainBinary()) + " gate --presence-hook", "timeout": 10}}}}}})
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
