package captaincode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The Euclid ENGINE (~/Gits/euclid/engine, or a brain's vendored bin/) is
// where retrieval lives: search.py ranks the catalog the launch reindex
// builds (every governed doc and source file) fused with a ripgrep full-text
// pass and a relation-graph walk; ask.py adds git provenance; git-recall.py
// is decision archaeology over the history. Captain's own register BM25
// (EuclidSearch) stays as the fallback for a machine without the engine -
// until 2026-09-13 it was the ONLY search a worker had, so the index
// rebuilt at every launch fed nothing but the dashboard.

// engineScript returns the engine script to run for a brain: the copy
// vendored in the brain's bin/ when it has one, else the engine checkout's.
func engineScript(root, name string) string {
	if p := filepath.Join(root, "bin", name); isFile(p) {
		return p
	}
	if engine := EuclidEngine(); engine != "" {
		if p := filepath.Join(engine, "engine", name); isFile(p) {
			return p
		}
	}
	return ""
}

// RepoBrainRoot is <repo>/.euclid for cwd's repository, "" when there is
// none: the engine indexes a repository's corpus, not the main brain's.
func RepoBrainRoot(cwd string) string {
	repo := RepoRoot(cwd)
	if repo == "" {
		return ""
	}
	if root := filepath.Join(repo, ".euclid"); isDir(root) {
		return root
	}
	return ""
}

// runEngine runs one engine script in the brain's host with EUCLID_ROOT set
// and returns its text. Output is what the model reads, so it is bounded.
func runEngine(root, script string, args []string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	host := filepath.Dir(root)
	cmd := exec.CommandContext(ctx, "python3", append([]string{script}, args...)...)
	cmd.Dir = host
	cmd.Env = append(os.Environ(), "EUCLID_ROOT="+host, "EUCLID_NO_AUTOBUILD=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if err != nil && text == "" {
		return "", fmt.Errorf("%s: %v: %s", filepath.Base(script), err, tailOf(errb.String(), 400))
	}
	if len(text) > 12000 {
		text = CutHead(text, 12000) + "\n…[truncated]" // rune-safe: this reaches codex exec's argv
	}
	return text, nil
}

// EngineSearch is search.py over the repo brain's catalog and full text.
// ok=false when no engine or no repo brain is available (caller falls back
// to the register search).
func EngineSearch(cwd, query string, limit int) (string, bool) {
	root := RepoBrainRoot(cwd)
	if root == "" {
		return "", false
	}
	script := engineScript(root, "search.py")
	if script == "" {
		return "", false
	}
	if limit <= 0 {
		limit = 8
	}
	out, err := runEngine(root, script, []string{"--limit", strconv.Itoa(limit), query}, 60*time.Second)
	if err != nil {
		return "", false
	}
	return out, true
}

// EngineAsk is ask.py: catalog + full text + relation graph + git provenance
// through one door. lane "" = doc+code, "all" adds the live git lane.
func EngineAsk(cwd, query, lane string, limit int) (string, error) {
	root := RepoBrainRoot(cwd)
	if root == "" {
		return "", fmt.Errorf("no repo brain for %s", cwd)
	}
	script := engineScript(root, "ask.py")
	if script == "" {
		return "", fmt.Errorf("no Euclid engine found (ask.py)")
	}
	if limit <= 0 {
		limit = 6
	}
	args := []string{"--limit", strconv.Itoa(limit)}
	if lane != "" {
		args = append(args, "--lane", lane)
	}
	return runEngine(root, script, append(args, query), 120*time.Second)
}

// EngineRecall is git-recall.py: ranked who/when/why over the repository's
// history for a query, optionally narrowed to a path.
func EngineRecall(cwd, query, path string, limit int) (string, error) {
	root := RepoBrainRoot(cwd)
	if root == "" {
		return "", fmt.Errorf("no repo brain for %s", cwd)
	}
	script := engineScript(root, "git-recall.py")
	if script == "" {
		return "", fmt.Errorf("no Euclid engine found (git-recall.py)")
	}
	if limit <= 0 {
		limit = 12
	}
	args := []string{"-n", strconv.Itoa(limit)}
	if path != "" {
		args = append(args, "--path", path)
	}
	return runEngine(root, script, append(args, query), 120*time.Second)
}

// noteFiles maps a note kind to the append-only register that holds it.
var noteFiles = map[string]string{
	"decision": "memory/decisions-ledger.md",
	"question": "memory/open-questions.md",
	"memory":   "memory/MEMORIES.md",
	"failure":  "memory/FAILURES.md",
}

// AppendNote appends one dated entry to an append-only register of the
// session's WRITE brain, and as a promoted note file when the shared brain
// travels in git (euclid_share.go). This
// is how a worker records a decision it made or a question it left open
// while the work is fresh, instead of hoping the distill infers it later.
func AppendNote(cwd, kind, text, by string) (string, error) {
	rel, ok := noteFiles[strings.ToLower(strings.TrimSpace(kind))]
	if !ok {
		return "", fmt.Errorf("kind must be one of decision, question, memory, failure")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("text is required")
	}
	wb, ok := WriteBrain(cwd)
	if !ok {
		return "", fmt.Errorf("no write brain for %s (run `captain euclid init`)", cwd)
	}
	p := filepath.Join(wb.Root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line := "- " + time.Now().Format("2006-01-02") + " " + strings.ReplaceAll(text, "\n", " ")
	if by != "" {
		line += " _(" + by + ")_"
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		return "", err
	}
	// The same note, promoted: one file under the shared brain's notes/ when
	// that brain travels in git (euclid_share.go). Local brains stay local.
	if wb.Kind == "developer" {
		ShareNote(cwd, kind, text, by)
	}
	return p, nil
}

// The CLI legs (claude -p, codex exec) run outside opencode, so the euclid
// MCP registered in opencode.jsonc never reached them: the frontier leg did
// the heaviest work with nothing but the 2.5k orientation block. Each CLI is
// handed the same server on its own flag. A server that fails to start is
// reported by the CLI and the run continues without it - memory augments a
// worker, it never gates one.

// euclidMCPServer is the server spec both CLIs get: captain's own binary,
// pinned to the workspace so the tools read that repository's brain.
func euclidMCPServer(dir string) (command string, args []string, env map[string]string, ok bool) {
	if !euclidEnabled() || dir == "" {
		return "", nil, nil, false
	}
	if RepoBrainRoot(dir) == "" && MainBrainPath() == "" {
		return "", nil, nil, false
	}
	exe, err := os.Executable()
	if err != nil {
		return "", nil, nil, false
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, []string{"euclid", "mcp"}, map[string]string{"CAPTAIN_CWD": dir}, true
}

// ClaudeMCPArgs returns the `--mcp-config` argument for claude -p, a JSON
// string with the euclid server; nil when Euclid is off or has no brain.
func ClaudeMCPArgs(dir string) []string {
	command, args, env, ok := euclidMCPServer(dir)
	if !ok {
		return nil
	}
	spec := map[string]any{"mcpServers": map[string]any{"euclid": map[string]any{"command": command, "args": args, "env": env}}}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil
	}
	return []string{"--mcp-config", string(b)}
}

// CodexMCPArgs returns the `-c mcp_servers.euclid.*` overrides for codex
// exec (TOML values); nil when Euclid is off or has no brain.
func CodexMCPArgs(dir string) []string {
	command, args, env, ok := euclidMCPServer(dir)
	if !ok {
		return nil
	}
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, strconv.Quote(a))
	}
	var envParts []string
	for k, v := range env {
		envParts = append(envParts, k+"="+strconv.Quote(v))
	}
	return []string{
		"-c", "mcp_servers.euclid.command=" + strconv.Quote(command),
		"-c", "mcp_servers.euclid.args=[" + strings.Join(quoted, ",") + "]",
		"-c", "mcp_servers.euclid.env={" + strings.Join(envParts, ",") + "}",
	}
}

// DirectorMemory is what the director reads before planning: the brain's
// current thesis and map (so a brief can say where things live), the lessons
// (WISDOM, so briefs carry them), and the tail of what failed and what was
// decided (so it does not plan what already failed - round 3 of a /repeat
// waited an hour for a benchmark a FAILURES line would have flagged,
// 2026-09-12). Bounded to about 2,500 characters; "" without a brain.
func DirectorMemory(dir string) string { return DirectorMemoryWith(dir, nil) }

// DirectorMemoryWith is DirectorMemory over ReadSetWith: a task that names
// other repositories plans with their brains in view.
func DirectorMemoryWith(dir string, also []string) string {
	if !euclidEnabled() || dir == "" {
		return ""
	}
	set := ReadSetWith(dir, also)
	if len(set) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Project memory (Euclid) - decisions and lessons already recorded; plan and brief consistently with them:\n")
	read := func(root, name string) string {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return ""
		}
		return string(b)
	}
	var wb *EuclidBrain
	for i := range set {
		if set[i].Writable {
			wb = &set[i]
		}
	}
	for _, b := range set {
		if b.Kind != "repo" && b.Kind != "main" && !b.Writable {
			continue
		}
		if t := registerThesis(read(b.Root, "BRAIN.md"), 500); t != "" {
			fmt.Fprintf(&sb, "- [%s] BRAIN: %s\n", b.Label, t)
		}
		if b.Kind == "repo" {
			if m := mapRows(read(b.Root, "MAP.md"), 700); m != "" {
				fmt.Fprintf(&sb, "- [%s] MAP:\n%s\n", b.Label, m)
			}
		}
		if t := registerThesis(read(b.Root, "WISDOM.md"), 400); t != "" {
			fmt.Fprintf(&sb, "- [%s] WISDOM: %s\n", b.Label, t)
		}
	}
	if wb != nil {
		if tail := lastLines(read(wb.Root, "memory/FAILURES.md"), 5, 500); tail != "" {
			fmt.Fprintf(&sb, "- recent FAILURES:\n%s\n", tail)
		}
		if tail := lastLines(read(wb.Root, "memory/decisions-ledger.md"), 5, 500); tail != "" {
			fmt.Fprintf(&sb, "- recent decisions:\n%s\n", tail)
		}
		if tail := lastLines(read(wb.Root, "memory/open-questions.md"), 3, 300); tail != "" {
			fmt.Fprintf(&sb, "- open questions:\n%s\n", tail)
		}
	}
	out := sb.String()
	if strings.Count(out, "\n") <= 1 {
		return ""
	}
	if len(out) > 2800 {
		out = CutHead(out, 2800) + "…"
	}
	return out
}

// mapRows keeps a MAP register's table rows (the where-things-live lines).
func mapRows(text string, limit int) string {
	var rows []string
	n := 0
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "|") || strings.HasPrefix(l, "|--") || strings.HasPrefix(l, "| --") {
			continue
		}
		if n+len(l) > limit {
			break
		}
		rows = append(rows, "  "+l)
		n += len(l)
	}
	return strings.Join(rows, "\n")
}

// lastLines returns the last n non-empty, non-heading lines of an
// append-only register, bounded to limit characters.
func lastLines(text string, n, limit int) string {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var out []string
	total := 0
	for _, l := range lines {
		if len(l) > 200 {
			l = CutHead(l, 200) + "…"
		}
		if total+len(l) > limit {
			break
		}
		out = append(out, "  "+l)
		total += len(l)
	}
	return strings.Join(out, "\n")
}

// ClaudeDirArgs widens a claude -p worker's working directories to the
// places its task legitimately reaches beyond the workspace: the workspace
// root's siblings (a multi-repo task - arc reading lemma), and the system
// temp dirs (scratch clones, benchmark logs). With Claude Code's
// blockReadsOutsideWorkingDirectories these are the difference between a
// read and a refusal; without it they cost nothing.
func ClaudeDirArgs(dir string) []string {
	if dir == "" {
		return nil
	}
	var out []string
	add := func(p string) {
		if p == "" || p == dir || !isDir(p) {
			return
		}
		out = append(out, "--add-dir", p)
	}
	root := strings.TrimSpace(os.Getenv("CAPTAIN_WORKSPACE_ROOT"))
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, "Gits")
		}
	}
	if root != "" && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		add(root)
	}
	add(os.TempDir())
	add("/private/tmp")
	add("/tmp")
	return out
}

// ShortErr is an error's first clause, for a ledger line.
func ShortErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.Join(strings.Fields(err.Error()), " ")
	if i := strings.Index(s, ": "); i > 0 && i < 60 {
		s = s[i+2:]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
