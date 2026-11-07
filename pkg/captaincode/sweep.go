package captaincode

// Sweeping merged worktrees and branches. On 2026-11-07 one repository on
// the 64 GB Mac held 116 worktrees and 202 branches; 91 of the worktrees and
// 183 of the branches were already merged into main with nothing left in
// them. The landing skill tells workers to remove both after a merge, and
// they did not, so a rule in a prompt is not enough: `captain sweep` removes
// what is provably done, on a schedule.
//
// A worktree goes only when nothing in it can be lost: its HEAD is on the
// base branch, git status is empty, no ignored file outside the build caches
// exists, no process has its working directory there, git has not touched it
// for MinIdle, and it is not locked. `git worktree lock` is how a record
// keeps a cited checkout. A branch goes only when its tip is on the base
// branch and no worktree has it checked out. The base is origin's default
// branch when there is one: a stale origin makes fewer things look merged,
// while a local main may carry commits that were never pushed.
//
// Per repository, `git config captain.sweep false` skips it entirely and
// `git config captain.sweep.branches false` keeps every branch.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SweepOptions controls one sweep. Without Apply nothing changes on disk.
type SweepOptions struct {
	Apply   bool
	MinIdle time.Duration
	// Busy reports whether a process has its working directory at or under
	// path. A worker between two commands has no file open, only a cwd.
	// path has its symlinks resolved, as git and lsof both print it.
	Busy func(path string) bool
}

// SweepAction is one worktree or branch the sweep looked at. Skip says why
// it stays; Done says it was removed. Neither means a dry run would remove it.
type SweepAction struct {
	Repo   string `json:"repo"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Head   string `json:"head,omitempty"`
	Skip   string `json:"skip,omitempty"`
	Done   bool   `json:"done,omitempty"`
	Err    string `json:"error,omitempty"`
}

// sweepCaches are ignored paths that hold only rebuildable output.
var sweepCaches = []string{"__pycache__/", ".lake/", ".mypy_cache/", ".ruff_cache/", ".pytest_cache/",
	"node_modules/", ".venv/", "target/", ".gradle/", ".next/", ".DS_Store"}

func sweepGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// sweepBase is the ref a worktree or branch must be merged into.
func sweepBase(repo string) (ref, name string) {
	if out, err := sweepGit(repo, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil && out != "" {
		return out, strings.TrimPrefix(out, "origin/")
	}
	for _, r := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := sweepGit(repo, "rev-parse", "-q", "--verify", r+"^{commit}"); err == nil {
			return r, strings.TrimPrefix(r, "origin/")
		}
	}
	return "", ""
}

func sweepMerged(repo, commit, base string) bool {
	_, err := sweepGit(repo, "merge-base", "--is-ancestor", commit, base)
	return err == nil
}

type sweepWorktree struct {
	path, head, branch string
	locked, prunable   bool
}

func sweepWorktrees(repo string) ([]sweepWorktree, error) {
	out, err := sweepGit(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var all []sweepWorktree
	for _, block := range strings.Split(out, "\n\n") {
		var w sweepWorktree
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				w.path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "HEAD "):
				w.head = strings.TrimPrefix(line, "HEAD ")
			case strings.HasPrefix(line, "branch "):
				w.branch = strings.TrimPrefix(line, "branch refs/heads/")
			case line == "locked" || strings.HasPrefix(line, "locked "):
				w.locked = true
			case line == "prunable" || strings.HasPrefix(line, "prunable "):
				w.prunable = true
			}
		}
		if w.path != "" {
			all = append(all, w)
		}
	}
	return all, nil
}

// sweepLastTouch is when git last wrote the worktree's index, HEAD or reflog.
func sweepLastTouch(dir string) time.Time {
	gitDir, err := sweepGit(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return time.Now()
	}
	var last time.Time
	for _, f := range []string{"index", "HEAD", filepath.Join("logs", "HEAD")} {
		if st, err := os.Stat(filepath.Join(gitDir, f)); err == nil && st.ModTime().After(last) {
			last = st.ModTime()
		}
	}
	return last
}

func sweepIgnoredOutput(dir string) (string, error) {
	out, err := sweepGit(dir, "status", "--porcelain=v1", "--ignored=matching", "--untracked-files=normal")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		p, ok := strings.CutPrefix(line, "!! ")
		if !ok {
			continue
		}
		cache := false
		for _, c := range sweepCaches {
			if p == c || strings.HasPrefix(p, c) || strings.Contains(p, "/"+c) || strings.HasSuffix(p, ".pyc") {
				cache = true
				break
			}
		}
		if !cache {
			return p, nil
		}
	}
	return "", nil
}

func sweepWorktreeSkip(repo, base string, w sweepWorktree, o SweepOptions) string {
	switch {
	case w.locked:
		return "locked"
	case w.prunable:
		return "directory missing; git worktree prune clears it"
	case w.head == "":
		return "no HEAD"
	case !sweepMerged(repo, w.head, base):
		return "not merged into " + base
	}
	if o.Busy != nil && o.Busy(w.path) {
		return "in use by a running process"
	}
	if st, err := sweepGit(w.path, "status", "--porcelain=v1", "--untracked-files=all"); err != nil {
		return "status failed: " + err.Error()
	} else if st != "" {
		return fmt.Sprintf("uncommitted changes (%d paths)", strings.Count(st, "\n")+1)
	}
	if p, err := sweepIgnoredOutput(w.path); err != nil {
		return "status failed: " + err.Error()
	} else if p != "" {
		return "ignored files outside build caches, e.g. " + p
	}
	if o.MinIdle > 0 {
		if idle := time.Since(sweepLastTouch(w.path)); idle < o.MinIdle {
			return fmt.Sprintf("active %s ago", idle.Round(time.Minute))
		}
	}
	return ""
}

func sweepConfigFalse(repo, key string) bool {
	out, err := sweepGit(repo, "config", "--type=bool", "--get", key)
	return err == nil && out == "false"
}

// SweepRepo sweeps one repository. The main worktree and the base branch are
// never touched.
func SweepRepo(repo string, o SweepOptions) ([]SweepAction, error) {
	if sweepConfigFalse(repo, "captain.sweep") {
		return nil, nil
	}
	base, baseName := sweepBase(repo)
	if base == "" {
		return nil, fmt.Errorf("%s: no main or master branch to sweep against", repo)
	}
	wts, err := sweepWorktrees(repo)
	if err != nil {
		return nil, err
	}
	var acts []SweepAction
	checkedOut := map[string]bool{}
	for i, w := range wts {
		if i == 0 {
			if w.branch != "" {
				checkedOut[w.branch] = true
			}
			continue
		}
		a := SweepAction{Repo: repo, Kind: "worktree", Target: w.path, Head: w.head}
		a.Skip = sweepWorktreeSkip(repo, base, w, o)
		if a.Skip == "" && o.Apply {
			// Without --force git refuses a worktree that changed since the
			// checks above, so a race leaves the work in place.
			if _, err := sweepGit(repo, "worktree", "remove", w.path); err != nil {
				a.Err = err.Error()
			} else {
				a.Done = true
			}
		}
		if !a.Done && w.branch != "" {
			checkedOut[w.branch] = true
		}
		acts = append(acts, a)
	}
	if o.Apply {
		_, _ = sweepGit(repo, "worktree", "prune")
	}
	if sweepConfigFalse(repo, "captain.sweep.branches") {
		return acts, nil
	}
	out, err := sweepGit(repo, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads")
	if err != nil {
		return acts, err
	}
	for _, line := range strings.Split(out, "\n") {
		name, sha, ok := strings.Cut(line, " ")
		if !ok || name == baseName || name == "main" || name == "master" || checkedOut[name] || !sweepMerged(repo, sha, base) {
			continue
		}
		a := SweepAction{Repo: repo, Kind: "branch", Target: name, Head: sha}
		if o.Apply {
			// The old value makes the delete fail if the branch moved since
			// it was checked.
			if _, err := sweepGit(repo, "update-ref", "-d", "refs/heads/"+name, sha); err != nil {
				a.Err = err.Error()
			} else {
				a.Done = true
			}
		}
		acts = append(acts, a)
	}
	return acts, nil
}
