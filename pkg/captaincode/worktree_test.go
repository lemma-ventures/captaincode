package captaincode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func wtFixtureRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	run("git", "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "--quiet", "-m", "seed")
	rev, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(rev))
}

func TestNewWorktreeCreatesIsolatedCheckout(t *testing.T) {
	repo, rev := wtFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()
	if _, err := os.Stat(filepath.Join(wt.Dir, "file.txt")); err != nil {
		t.Fatalf("worktree missing the repo's file: %v", err)
	}
	gotRev, err := exec.Command("git", "-C", wt.Dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse in worktree: %v", err)
	}
	if strings.TrimSpace(string(gotRev)) != rev {
		t.Fatalf("worktree at %s, want %s", strings.TrimSpace(string(gotRev)), rev)
	}
}

func TestWorktreeCloseRemovesDirectory(t *testing.T) {
	repo, rev := wtFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	dir := wt.Dir
	if err := wt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists after Close: %v", err)
	}
}

func TestNewWorktreeRejectsNonGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := NewWorktree(context.Background(), dir, strings.Repeat("0", 40))
	if err == nil {
		t.Fatal("expected error for non-git repo")
	}
	if !strings.Contains(err.Error(), "not inside a git repository") {
		t.Fatalf("expected 'not inside a git repository' error, got %v", err)
	}
}

func TestNewWorktreeRejectsUnpinnedRevision(t *testing.T) {
	repo, _ := wtFixtureRepo(t)
	_, err := NewWorktree(context.Background(), repo, "main")
	if err == nil {
		t.Fatal("expected error for unpinned revision")
	}
}

func TestIsolateWorkersCreatesNWorktrees(t *testing.T) {
	repo, rev := wtFixtureRepo(t)
	wts, err := IsolateWorkers(context.Background(), repo, rev, 3)
	if err != nil {
		t.Fatalf("IsolateWorkers: %v", err)
	}
	defer CloseAll(wts)
	if len(wts) != 3 {
		t.Fatalf("got %d worktrees, want 3", len(wts))
	}
	seen := map[string]bool{}
	for _, w := range wts {
		if seen[w.Dir] {
			t.Fatalf("duplicate worktree directory %s", w.Dir)
		}
		seen[w.Dir] = true
		if _, err := os.Stat(filepath.Join(w.Dir, "file.txt")); err != nil {
			t.Fatalf("worktree %s missing file.txt: %v", w.Dir, err)
		}
	}
}

func TestIsolateWorkersSingleWorkerReturnsNil(t *testing.T) {
	repo, rev := wtFixtureRepo(t)
	wts, err := IsolateWorkers(context.Background(), repo, rev, 1)
	if err != nil {
		t.Fatalf("IsolateWorkers: %v", err)
	}
	if wts != nil {
		t.Fatalf("got %d worktrees for single worker, want nil", len(wts))
	}
}

func TestIsolateWorkersPartialFailureCleansUp(t *testing.T) {
	repo, rev := wtFixtureRepo(t)
	wts, err := IsolateWorkers(context.Background(), repo, rev, 3)
	if err != nil {
		t.Fatalf("IsolateWorkers: %v", err)
	}
	dirs := make([]string, len(wts))
	for i, w := range wts {
		dirs[i] = w.Dir
	}
	if err := CloseAll(wts); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
	for _, d := range dirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("worktree %s still exists after CloseAll", d)
		}
	}
}

func TestIsolateWorkersNonGitRepoReturnsError(t *testing.T) {
	dir := t.TempDir()
	_, err := IsolateWorkers(context.Background(), dir, strings.Repeat("0", 40), 3)
	if err == nil {
		t.Fatal("expected error for non-git repo")
	}
	if !strings.Contains(err.Error(), "not inside a git repository") {
		t.Fatalf("expected 'not inside a git repository' error, got %v", err)
	}
}

func TestCurrentRevisionReturnsHEAD(t *testing.T) {
	repo, rev := wtFixtureRepo(t)
	got := CurrentRevision(repo)
	if got != rev {
		t.Fatalf("CurrentRevision returned %q, want %q", got, rev)
	}
}

func TestCurrentRevisionNonGitRepoReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := CurrentRevision(dir); got != "" {
		t.Fatalf("CurrentRevision on non-git repo returned %q, want empty", got)
	}
}

func TestWorktreeIsolationDoesNotMutateSourceTree(t *testing.T) {
	repo, rev := wtFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()
	if err := os.WriteFile(filepath.Join(wt.Dir, "file.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(repo, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(src) != "hello\n" {
		t.Fatalf("source tree mutated: %q, want %q", string(src), "hello\n")
	}
}

func TestCloseAllOnNilIsNoOp(t *testing.T) {
	if err := CloseAll(nil); err != nil {
		t.Fatalf("CloseAll(nil) returned error: %v", err)
	}
}

func TestWorktreeCloseIdempotent(t *testing.T) {
	repo, rev := wtFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := wt.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := wt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
