package captaincode

// The sent-turn policy: what a worker may not do while it answers a turn that
// another agent sent with `captain send` (brain_inbox.go). The worker prompt
// already says so (sentTurnContract); this enforces it at the tool boundary,
// deterministically and without a decision leg, so a worker talked into it by
// an injected message is refused rather than trusted:
//
//   - the plugin asks the brain before each opencode tool call
//     (/v1/gate/sent);
//   - claude's PreToolUse hook (`captain gate --hook`) applies it when the
//     worker's environment says the turn was sent (SentTurnEnv);
//   - codex runs a sent turn in its own sandbox (workspace writes only, no
//     network), and cursor without --force (codexcli.go, legs.go).
//
// The tools are an allowlist: reading, editing files in the workspace and
// the shell. Fetching the web, subagents, MCP tools and anything new are
// refused, whatever the call says. Every shell command runs in the jail
// (jail.go): no network, writes in the workspace only, no credentials -
// enforced by the operating system, not by reading the command. The shell
// rules below still refuse what an injection wants by name, so the worker
// is told why rather than meeting a sandbox error.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SentTurnMarker opens the provenance line of a sent turn's worker prompt; a
// runner that finds it in its task knows the turn was sent.
const SentTurnMarker = "[captain] Provenance: the last user turn was not typed by the user."

// SentTurnEnv is set in a CLI worker's environment for a sent turn.
const SentTurnEnv = "CAPTAIN_SENT_TURN"

// SentDirEnv is the sent turn's workspace in a CLI worker's environment: the
// folder its shell commands are jailed to and whose inbox a refusal closes.
const SentDirEnv = "CAPTAIN_SENT_DIR"

// SentTurnClaudeSettings is the --settings JSON a claude worker answering a
// sent turn runs with: one PreToolUse hook on every tool, `captain gate
// --sent-hook`, by the absolute path of this binary when it can be found.
func SentTurnClaudeSettings() string {
	bin := "captain"
	if exe, err := os.Executable(); err == nil && filepath.Base(exe) == "captain" {
		bin = exe
	}
	b, _ := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{
		"matcher": "*",
		"hooks":   []any{map[string]any{"type": "command", "command": shellQuote(bin) + " gate --sent-hook", "timeout": 10}},
	}}}})
	return string(b)
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"$`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// IsSentTurn reports whether a worker prompt answers a sent turn.
func IsSentTurn(prompt string) bool { return strings.Contains(prompt, SentTurnMarker) }

type sentRule struct {
	why string
	re  *regexp.Regexp
}

var sentShellRules = []sentRule{
	{"publish or push", regexp.MustCompile(`(?i)\bgit\s+(push|tag\s+-d)\b|\bgh\s+(pr\s+(create|merge|close)|release|repo\s+(delete|edit|create)|secret|api\s+-X\s*(POST|PUT|PATCH|DELETE))\b|\b(npm|pnpm|yarn)\s+publish\b|\bcargo\s+publish\b|\bdocker\s+push\b|\btwine\s+upload\b`)},
	{"delete or rewrite history", regexp.MustCompile(`(?i)\brm\s+-[a-z]*[rf][a-z]*\b|\bgit\s+(reset\s+--hard|clean\s+-[a-z]*f|filter-repo|filter-branch|branch\s+-D|rebase\b)|\bfind\b[^|]*-delete\b|\bshred\b`)},
	{"send data off the machine", regexp.MustCompile(`(?i)\b(curl|wget|http|httpie)\b[^|;&]*\s(-d|--data[a-z-]*|-F|--form|-T|--upload-file|-X\s*(POST|PUT|PATCH)|--post-(data|file))\b|\b(scp|sftp|rsync)\b[^|;&]*\S+:\S*|\b(nc|ncat|netcat|socat|telnet)\s|\bssh\s+\S+@|\bmail(x)?\s+-s\b`)},
	{"read secrets", regexp.MustCompile(`(?i)(~|\$HOME|/Users/[^/\s]+|/home/[^/\s]+)/\.(ssh|aws|gnupg|netrc|docker/config|config/gh|kube)\b|\b(printenv|env)\s*($|\||>)|\bgh\s+auth\s+token\b|\bsecurity\s+find-(generic|internet)-password\b|\bcat\s+[^|;&]*\.(env|pem|key|p12|keychain)\b`)},
	{"relay to other captains", regexp.MustCompile(`(?i)\bcaptain\s+send\b|/v1/inbox\b`)},
	{"switch off a safety check", regexp.MustCompile(`(?i)--no-verify\b|CAPTAIN_(ACTION_GATE|REDACT|INBOX_HOLD|ISOLATE_MOVED)\s*=\s*(0|off)|git\s+config\s+[^|;&]*core\.hooksPath|chmod\s+-?R?\s*777`)},
}

// secretPathRe is a path into a credential store.
var secretPathRe = regexp.MustCompile(`(^|/)\.(ssh|aws|gnupg|netrc|docker/config\.json|config/gh|kube)(/|$)|\.(pem|p12|keychain)$|(^|/)\.env(\.|$)`)

// longTokenRe is a value that looks like a credential in a URL: a long run of
// letters and digits, or a captain secret placeholder.
var longTokenRe = regexp.MustCompile(`[A-Za-z0-9_\-]{32,}|\[\[secret:`)

// sentTurnTools are the tools a sent turn may use (lowercase; claude's and
// opencode's names). Not here, so refused: web fetch and search, subagents
// (opencode's do not inherit the turn's provenance), MCP and plugin tools
// (they reach other services or the memory).
var sentTurnTools = map[string]bool{
	"bash": true, "shell": true, "bashoutput": true, "killshell": true,
	"read": true, "notebookread": true, "grep": true, "glob": true, "ls": true, "list": true,
	"write": true, "edit": true, "multiedit": true, "patch": true, "apply_patch": true, "notebookedit": true,
	"todowrite": true, "todoread": true, "exitplanmode": true,
}

// shellTools run a command line.
var shellTools = map[string]bool{"bash": true, "shell": true, "run": true, "execute": true, "terminal": true}

// SentJailEnabled: CAPTAIN_SENT_JAIL=0 runs a sent turn's shell commands
// outside the jail (the shell rules still apply).
func SentJailEnabled() bool { return os.Getenv("CAPTAIN_SENT_JAIL") != "0" }

// SentTurnDecision is the whole sent-turn policy for one tool call: why it is
// refused, or, for a shell command, whether it must run in the jail.
func SentTurnDecision(a GateAction) (refusal string, jail bool) {
	if why := SentTurnRefusal(a); why != "" {
		return why, false
	}
	if !shellTools[strings.ToLower(a.Tool)] || !SentJailEnabled() {
		return "", false
	}
	if _, _, err := JailCommand(orDot(a.Cwd), "true"); err != nil {
		return sentRefuse("run a shell command outside a sandbox (" + err.Error() + ")"), false
	}
	return "", true
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}

func sentRefuse(why string) string {
	return "refused: this turn was sent by another agent with `captain send`, not typed by the user, and a sent turn may not " + why + ". Ask the user to type the request if they want it."
}

// SentTurnRefusal returns why a sent turn may not take this action, or "".
// cwd is the worker's workspace: writes outside it are refused.
func SentTurnRefusal(a GateAction) string {
	refuse := sentRefuse
	tool := strings.ToLower(a.Tool)
	if !sentTurnTools[tool] && !shellTools[tool] {
		return refuse("use the " + a.Tool + " tool (a sent turn reads, edits its workspace and runs sandboxed commands; nothing else)")
	}
	switch tool {
	case "bash", "shell", "run", "execute", "terminal":
		for _, r := range sentShellRules {
			if r.re.MatchString(a.Command) {
				return refuse(r.why)
			}
		}
	case "write", "edit", "patch", "multiedit", "apply_patch", "notebookedit":
		if p := a.Path; p != "" {
			if why := sentPathRefusal(p, a.Cwd); why != "" {
				return refuse(why)
			}
		}
	case "read", "grep", "glob", "notebookread":
		if secretPathRe.MatchString(a.Path) {
			return refuse("read secrets (" + a.Path + ")")
		}
	}
	return ""
}

// sentPathRefusal: a sent turn writes inside its workspace only, and not into
// the repository's history, hooks or CI.
func sentPathRefusal(path, cwd string) string {
	p := filepath.Clean(path)
	if !filepath.IsAbs(p) && cwd != "" {
		p = filepath.Join(cwd, p)
	}
	if cwd != "" {
		root := filepath.Clean(cwd)
		if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			return "write outside its workspace (" + path + ")"
		}
	}
	slash := filepath.ToSlash(p)
	for _, guarded := range []string{"/.git/", "/.github/workflows/", "/.githooks/", "/.gitlab-ci.yml", "/.circleci/"} {
		if strings.Contains(slash+"/", guarded) {
			return "change the repository's history, hooks or CI (" + path + ")"
		}
	}
	return ""
}
