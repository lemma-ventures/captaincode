package captaincode

// The jail: the operating system's sandbox around every shell command a sent
// turn runs (sentpolicy.go). The shell rules there read the command's words,
// and a command can always be worded past a pattern (`python -c`, an alias, a
// script it wrote first). The jail does not read the command: whatever it
// says, it runs with
//
//   - no network at all - no TCP or UDP, localhost included (the brain's
//     inbox and the opencode serve listen there), and no unix sockets (DNS
//     lookups, ssh-agent, docker);
//   - writes only inside the workspace, the temporary directories and the
//     build caches, and never into the repository's hooks, git config or CI;
//   - no reads of credential stores (~/.ssh, ~/.aws, captain's own keys…);
//   - an environment with every variable that looks like a credential
//     removed.
//
// macOS uses sandbox-exec (Seatbelt), Linux bubblewrap. Where neither is
// available the jail refuses to run the command: a sent turn's shell is
// sandboxed or it does not run.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// secretDirs are the credential stores under $HOME a jailed command may not
// read.
var secretDirs = []string{
	".ssh", ".aws", ".gnupg", ".config/gh", ".config/gcloud", ".azure", ".kube", ".docker",
	".config/captain", ".captaincode", ".local/share/opencode", ".config/opencode", ".codex",
	".cursor", ".claude", ".gemini", ".password-store", "Library/Keychains",
}

// secretFiles are single credential files under $HOME.
var secretFiles = []string{".netrc", ".npmrc", ".pypirc", ".git-credentials", ".claude.json"}

// writableCaches are the build caches a jailed build may fill.
var writableCaches = []string{"Library/Caches", ".cache", ".npm", ".bun/install/cache", "go/pkg/mod", ".cargo/registry"}

// guardedInWorkspace are the paths inside the workspace a jailed command may
// not write: what would run later, outside the jail, as the user.
var guardedInWorkspace = []string{".git/hooks", ".git/config", ".github/workflows", ".githooks"}

// SeatbeltProfile is the sandbox-exec profile for a command in cwd.
func SeatbeltProfile(cwd, home string) string {
	q := func(p string) string {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(p, `\`, `\\`), `"`, `\"`) + `"`
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	b.WriteString("(deny network*)\n")
	b.WriteString("(deny file-write*)\n(allow file-write*\n")
	for _, p := range []string{cwd, "/private/tmp", "/private/var/folders"} {
		b.WriteString("  (subpath " + q(p) + ")\n")
	}
	for _, c := range writableCaches {
		b.WriteString("  (subpath " + q(filepath.Join(home, c)) + ")\n")
	}
	b.WriteString("  (regex #\"^/dev/\"))\n")
	b.WriteString("(deny file-write*\n")
	for _, g := range guardedInWorkspace {
		b.WriteString("  (subpath " + q(filepath.Join(cwd, g)) + ")\n")
	}
	b.WriteString(")\n(deny file-read* file-write*\n")
	for _, d := range secretDirs {
		b.WriteString("  (subpath " + q(filepath.Join(home, d)) + ")\n")
	}
	for _, f := range secretFiles {
		b.WriteString("  (literal " + q(filepath.Join(home, f)) + ")\n")
	}
	b.WriteString(")\n(deny mach-lookup (global-name \"com.apple.SecurityServer\") (global-name \"com.apple.secd\"))\n")
	return b.String()
}

// BwrapArgs are the bubblewrap arguments for a command in cwd (Linux).
func BwrapArgs(cwd, home string) []string {
	args := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		"--unshare-net", "--unshare-ipc", "--die-with-parent", "--new-session", "--bind", cwd, cwd}
	for _, c := range writableCaches {
		if p := filepath.Join(home, c); isDir(p) {
			args = append(args, "--bind", p, p)
		}
	}
	for _, g := range guardedInWorkspace {
		if p := filepath.Join(cwd, g); exists(p) {
			args = append(args, "--ro-bind", p, p)
		}
	}
	for _, d := range secretDirs {
		if p := filepath.Join(home, d); isDir(p) {
			args = append(args, "--tmpfs", p)
		}
	}
	for _, f := range secretFiles {
		if p := filepath.Join(home, f); exists(p) {
			args = append(args, "--ro-bind", "/dev/null", p)
		}
	}
	return args
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// credentialVarRe names an environment variable that may hold a credential.
var credentialVarRe = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSW|CREDENTIAL|AUTH|COOKIE|SESSION|PRIVATE|_PAT$|DSN|WEBHOOK|SSH_AUTH_SOCK|GPG_AGENT)`)

// JailEnv is env with every credential-like variable removed, and the
// toolchains told not to reach the network.
func JailEnv(env []string) []string {
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if credentialVarRe.MatchString(name) || name == "GOPROXY" || name == "CAPTAIN_JAILED" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GOPROXY=off", "CAPTAIN_JAILED=1")
}

// ErrNoJail: neither sandbox-exec nor bubblewrap is available.
var ErrNoJail = errors.New("no operating-system sandbox is available (sandbox-exec on macOS, bubblewrap on Linux), so a sent turn's shell command cannot run - install bubblewrap, or ask the user to type the request")

// JailCommand is the program and arguments that run shell command cmd in cwd
// inside the jail.
func JailCommand(cwd, cmd string) (string, []string, error) {
	home, _ := os.UserHomeDir()
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	switch runtime.GOOS {
	case "darwin":
		if p, err := exec.LookPath("sandbox-exec"); err == nil {
			return p, []string{"-p", SeatbeltProfile(cwd, home), "/bin/sh", "-c", cmd}, nil
		}
	case "linux":
		if p, err := exec.LookPath("bwrap"); err == nil {
			return p, append(BwrapArgs(cwd, home), "--chdir", cwd, "/bin/sh", "-c", cmd), nil
		}
	}
	return "", nil, ErrNoJail
}

// CaptainBinary is the captain executable's path, for commands the workers
// run back into it (hooks, the jail).
func CaptainBinary() string {
	if exe, err := os.Executable(); err == nil {
		if b := filepath.Base(exe); b == "captain" || b == "captaincode" {
			return exe
		}
	}
	return "captain"
}

// JailPrefix is the command line that runs a shell command in the jail; the
// command itself follows as one quoted argument (JailWrap).
func JailPrefix(cwd string) []string {
	return []string{CaptainBinary(), "jail", "--cwd", cwd, "--"}
}

// JailWrap is cmd rewritten to run inside the jail.
func JailWrap(cwd, cmd string) string {
	parts := JailPrefix(cwd)
	for i := range parts {
		parts[i] = shellQuote(parts[i])
	}
	return strings.Join(parts, " ") + " " + shellQuoteAlways(cmd)
}

func shellQuoteAlways(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
