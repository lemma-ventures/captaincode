package captaincode

// Worker isolation (ROADMAP M3.1). A workflow stage or team plan that runs
// multiple workers in PARALLEL against one workspace directory is a data race
// on the user's files: two workers editing the same tree can stomp each
// other's changes, and a gate that runs after one worker may see another's
// half-written output. A git worktree gives each concurrent writer its own
// checkout at the same base revision, so the isolation is filesystem-level
// and not a prompt instruction the worker may ignore.
//
// Three properties carry the weight:
//
//   - isolation is per-writer, not per-stage. A stage with one worker does
//     not need a worktree; a stage with three does, and each gets its own.
//   - the worktree is at the pinned base revision, not the dirty working
//     tree. The user's uncommitted changes survive an evaluation run
//     untouched, the same property eval.go's snapshot enforces.
//   - when isolation is not possible (not a git repo, worktree subcommand
//     unavailable, disk full) the caller SERIALIZES rather than proceeds
//     with concurrent writers in one directory. Serialized fallback is
//     slower but safe; silent concurrency is fast and wrong.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worktree is one isolated checkout. Close removes it; failing to call Close
// leaves a worktree on disk (git cleans stale worktrees on its own schedule,
// but a captain run should not rely on that).
type Worktree struct {
	Dir  string
	repo string
}

// gitRoot finds the top-level directory of the git repo containing dir, or ""
// if dir is not inside a git repository. A worker's workspace is often a
// subdirectory of the repo (cmd/captaincode, packages/opencode/src), so
// checking for .git in dir alone would miss it.
func gitRoot(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// NewWorktree creates a git worktree at the pinned revision. The worktree is
// a sibling of the source repo (under the system temp dir) so it does not
// disturb the repo's own working tree. Returns an error when the repo is not
// a git repository or the worktree subcommand fails — the caller must
// serialize in that case.
func NewWorktree(ctx context.Context, repo, revision string) (*Worktree, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, fmt.Errorf("worktree: resolve repo path: %w", err)
	}
	root := gitRoot(abs)
	if root == "" {
		return nil, fmt.Errorf("worktree: %s is not inside a git repository", abs)
	}
	if !pinnedRevision(revision) {
		return nil, fmt.Errorf("worktree: revision %q is not a pinned commit sha", revision)
	}
	dir, err := os.MkdirTemp("", "captain-wt-")
	if err != nil {
		return nil, fmt.Errorf("worktree: temp dir: %w", err)
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "git", "-C", root, "worktree", "add", "--detach", "--quiet", dir, revision)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("worktree: git worktree add %s: %w: %s", revision, err, strings.TrimSpace(string(out)))
	}
	return &Worktree{Dir: dir, repo: root}, nil
}

// Close removes the worktree via `git worktree remove --force`, which both
// deletes the directory and unregisters it from the source repo in one step.
// If that fails (directory already gone, or the worktree was moved), it falls
// back to os.RemoveAll plus `git worktree prune`. Safe to call multiple times.
func (w *Worktree) Close() error {
	if w == nil || w.Dir == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "git", "-C", w.repo, "worktree", "remove", "--force", w.Dir).Run(); err == nil {
		return nil
	}
	var first error
	if err := os.RemoveAll(w.Dir); err != nil && first == nil {
		first = fmt.Errorf("worktree: remove %s: %w", w.Dir, err)
	}
	pruneCtx, pruneCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pruneCancel()
	exec.CommandContext(pruneCtx, "git", "-C", w.repo, "worktree", "prune").Run()
	return first
}

// IsolateWorkers creates n worktrees at the pinned revision. On success each
// worker gets its own directory; on failure the caller serializes. Partial
// success is all-or-nothing: a stage that needs three worktrees and gets two
// cannot run three isolated workers, so the two are cleaned up and the stage
// falls back to serialized execution in the shared workspace.
func IsolateWorkers(ctx context.Context, repo, revision string, n int) ([]*Worktree, error) {
	if n <= 1 {
		return nil, nil
	}
	wts := make([]*Worktree, 0, n)
	for i := 0; i < n; i++ {
		wt, err := NewWorktree(ctx, repo, revision)
		if err != nil {
			for _, w := range wts {
				w.Close()
			}
			return nil, fmt.Errorf("isolate worker %d/%d: %w", i+1, n, err)
		}
		wts = append(wts, wt)
	}
	return wts, nil
}

// CloseAll closes every worktree in the slice, returning the first error. It
// always attempts to close all of them even if one fails, because a leaked
// worktree is a disk leak the user will not notice until git complains.
func CloseAll(wts []*Worktree) error {
	var first error
	for _, w := range wts {
		if err := w.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// CurrentRevision returns the HEAD commit sha of the repo containing dir, or
// "" if dir is not inside a git repository. This is the pinned base revision a
// worktree is created at.
func CurrentRevision(repo string) string {
	root := gitRoot(repo)
	if root == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
