package captaincode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
	return "\n\n" + PresenceMarker + id + "\nThis prompt has passkey approval. It has typed-turn tools in its linked folder. A default-branch push, merge, release, publish, delete or payment requires a separate signed decision. A chat reply cannot approve that decision. Later repeat rounds need fresh approval."
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
	if a.Cwd != "" && a.Cwd != g.Dir {
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
