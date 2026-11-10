package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func TestParseTUIFoldersKeepsTUIsOnly(t *testing.T) {
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	ps := strings.Join([]string{
		"/Users/x/.opencode/bin/opencode " + dir + " -s ses_abc",
		"opencode serve --port 14096",
		"opencode run hello",
		"/bin/zsh -l",
		"opencode " + dir, // the same TUI folder twice
	}, "\n")
	got := parseTUIFolders(ps)
	if len(got) != 1 || got[0] != real {
		t.Fatalf("parseTUIFolders = %v, want [%s]", got, real)
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestSiblingsOfSeesTheOtherWorktreeOnly(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(base, "ws")
	other := filepath.Join(base, "other")
	for _, d := range []string{repo, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		gitT(t, d, "init", "-q", "-b", "main")
		gitT(t, d, "commit", "-q", "--allow-empty", "-m", "init")
	}
	wt := filepath.Join(repo, ".worktrees", "tui-b")
	gitT(t, repo, "worktree", "add", "-q", "-b", "tui/b", wt)
	if err := os.WriteFile(filepath.Join(wt, "taken.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runs := []activeRun{
		{leg: captaincode.Leg("claude"), task: "refactor the parser", dir: wt, origin: wt, started: time.Now()},
		{leg: captaincode.Leg("codex"), task: "unrelated", dir: other, origin: other, started: time.Now()},
	}
	sibs := siblingsOf(repo, []string{repo, wt, other}, runs)
	if len(sibs) != 1 {
		t.Fatalf("siblings of %s = %+v, want the tui-b worktree only", repo, sibs)
	}
	s := sibs[0]
	if s.dir != wt || s.branch != "tui/b" || len(s.running) != 1 || !strings.Contains(s.running[0], "refactor the parser") {
		t.Fatalf("sibling = %+v", s)
	}
	if len(s.dirty) != 1 || s.dirty[0] != "taken.go" {
		t.Fatalf("dirty = %v, want [taken.go]", s.dirty)
	}

	// From the worktree, the main checkout is the sibling.
	back := siblingsOf(wt, []string{repo, wt}, nil)
	if len(back) != 1 || back[0].dir != repo || len(back[0].running) != 0 {
		t.Fatalf("siblings of the worktree = %+v", back)
	}

	text := siblingText(captaincode.Workspace{Dir: repo}, sibs)
	for _, want := range []string{wt, "tui/b", "taken.go", "refactor the parser", "captain send --cwd", "/btw [sibling notice]", "--from \"sibling " + repo, "never answer one"} {
		if !strings.Contains(text, want) {
			t.Errorf("sibling contract lacks %q:\n%s", want, text)
		}
	}
}

func TestSiblingTextAloneAndNotice(t *testing.T) {
	if got := siblingText(captaincode.Workspace{Dir: "/x"}, nil); got != "" {
		t.Fatalf("no sibling, no notice: want no section, got %q", got)
	}
	got := siblingText(captaincode.Workspace{Dir: "/x", SentBy: "sibling /x/.worktrees/tui-b"}, nil)
	if !strings.Contains(got, "notice from a sibling captain") || !strings.Contains(got, "do not reply") {
		t.Fatalf("a sibling's notice turn: %q", got)
	}
	t.Setenv("CAPTAIN_WORKER_SIBLINGS", "0")
	if got := (*brain)(nil).siblingContract(captaincode.Workspace{Dir: "/x"}); got != "" {
		t.Fatalf("CAPTAIN_WORKER_SIBLINGS=0 should drop the section, got %q", got)
	}
}
