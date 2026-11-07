package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sweepRepo is a repo on main with one worktree per case the sweeper must
// tell apart. Every worktree starts merged and clean; a test dirties the one
// it is about.
func sweepRepo(t *testing.T) (repo string, git func(dir string, args ...string) string) {
	t.Helper()
	repo, _ = wtFixtureRepo(t)
	git = func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	return repo, git
}

func addWorktree(t *testing.T, repo, name string, git func(string, ...string) string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	git(repo, "worktree", "add", "--quiet", "-b", name, dir, "main")
	return dir
}

func sweepOpts() SweepOptions {
	return SweepOptions{Apply: true, Busy: func(string) bool { return false }}
}

func actionFor(acts []SweepAction, kind, target string) (SweepAction, bool) {
	for _, a := range acts {
		if a.Kind == kind && (a.Target == target || strings.HasSuffix(a.Target, string(filepath.Separator)+target)) {
			return a, true
		}
	}
	return SweepAction{}, false
}

func TestSweepRemovesMergedCleanWorktreeAndBranch(t *testing.T) {
	repo, git := sweepRepo(t)
	dir := addWorktree(t, repo, "done", git)
	acts, err := SweepRepo(repo, sweepOpts())
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := actionFor(acts, "worktree", "done"); !ok || !a.Done {
		t.Fatalf("merged clean worktree not removed: %+v", acts)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still on disk: %v", err)
	}
	if a, ok := actionFor(acts, "branch", "done"); !ok || !a.Done {
		t.Fatalf("merged branch not deleted: %+v", acts)
	}
	if out := git(repo, "branch", "--list", "done"); out != "" {
		t.Fatalf("branch still listed: %q", out)
	}
}

func TestSweepDryRunChangesNothing(t *testing.T) {
	repo, git := sweepRepo(t)
	dir := addWorktree(t, repo, "done", git)
	o := sweepOpts()
	o.Apply = false
	acts, err := SweepRepo(repo, o)
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := actionFor(acts, "worktree", "done"); !ok || a.Done || a.Skip != "" {
		t.Fatalf("dry run should list the worktree as removable and not remove it: %+v", acts)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dry run removed the worktree: %v", err)
	}
	if out := git(repo, "branch", "--list", "done"); out == "" {
		t.Fatal("dry run deleted the branch")
	}
}

// Each case below is work a sweep must never lose, and the one that failed
// in practice was the reverse: nothing swept, 91 merged worktrees left in
// one repository. Both directions are tested.
func TestSweepKeepsWorktreesThatHoldWork(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, repo, dir string, git func(string, ...string) string, o *SweepOptions)
		skip  string
	}{
		{"modified", func(t *testing.T, repo, dir string, git func(string, ...string) string, o *SweepOptions) {
			os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0o644)
		}, "uncommitted"},
		{"untracked", func(t *testing.T, repo, dir string, git func(string, ...string) string, o *SweepOptions) {
			os.WriteFile(filepath.Join(dir, "notes.md"), []byte("draft\n"), 0o644)
		}, "uncommitted"},
		{"ignored-output", func(t *testing.T, repo, dir string, git func(string, ...string) string, o *SweepOptions) {
			os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("results/\n"), 0o644)
			git(dir, "add", ".gitignore")
			git(dir, "commit", "--quiet", "-m", "ignore results")
			git(repo, "merge", "--quiet", "--ff-only", "ignored-output")
			os.MkdirAll(filepath.Join(dir, "results"), 0o755)
			os.WriteFile(filepath.Join(dir, "results", "run.json"), []byte("{}\n"), 0o644)
		}, "ignored files"},
		{"unmerged", func(t *testing.T, repo, dir string, git func(string, ...string) string, o *SweepOptions) {
			os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\n"), 0o644)
			git(dir, "add", "new.txt")
			git(dir, "commit", "--quiet", "-m", "not on main")
		}, "not merged"},
		{"locked", func(t *testing.T, repo, dir string, git func(string, ...string) string, o *SweepOptions) {
			git(repo, "worktree", "lock", "--reason", "cited by a record", dir)
		}, "locked"},
		{"busy", func(t *testing.T, repo, dir string, git func(string, ...string) string, o *SweepOptions) {
			real, _ := filepath.EvalSymlinks(dir)
			o.Busy = func(p string) bool { return p == real }
		}, "in use"},
		{"recent", func(t *testing.T, repo, dir string, git func(string, ...string) string, o *SweepOptions) {
			o.MinIdle = time.Hour
		}, "active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, git := sweepRepo(t)
			dir := addWorktree(t, repo, tc.name, git)
			o := sweepOpts()
			tc.setup(t, repo, dir, git, &o)
			acts, err := SweepRepo(repo, o)
			if err != nil {
				t.Fatal(err)
			}
			a, ok := actionFor(acts, "worktree", tc.name)
			if !ok || a.Done || !strings.Contains(a.Skip, tc.skip) {
				t.Fatalf("want worktree kept with skip %q, got %+v", tc.skip, acts)
			}
			if _, err := os.Stat(dir); err != nil {
				t.Fatalf("worktree removed: %v", err)
			}
			if out := git(repo, "branch", "--list", tc.name); out == "" {
				t.Fatal("branch of a kept worktree was deleted")
			}
		})
	}
}

func TestSweepIgnoresBuildCaches(t *testing.T) {
	repo, git := sweepRepo(t)
	dir := addWorktree(t, repo, "cached", git)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("__pycache__/\n.lake/\n"), 0o644)
	git(dir, "add", ".gitignore")
	git(dir, "commit", "--quiet", "-m", "ignore caches")
	git(repo, "merge", "--quiet", "--ff-only", "cached")
	os.MkdirAll(filepath.Join(dir, "__pycache__"), 0o755)
	os.WriteFile(filepath.Join(dir, "__pycache__", "m.pyc"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".lake", "build"), 0o755)
	os.WriteFile(filepath.Join(dir, ".lake", "build", "a.olean"), []byte("x"), 0o644)
	acts, err := SweepRepo(repo, sweepOpts())
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := actionFor(acts, "worktree", "cached"); !ok || !a.Done {
		t.Fatalf("build caches alone should not keep a merged worktree: %+v", acts)
	}
}

func TestSweepBranches(t *testing.T) {
	repo, git := sweepRepo(t)
	git(repo, "branch", "merged-free")
	git(repo, "switch", "--quiet", "-c", "ahead")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	git(repo, "add", "a.txt")
	git(repo, "commit", "--quiet", "-m", "ahead of main")
	git(repo, "switch", "--quiet", "main")
	acts, err := SweepRepo(repo, sweepOpts())
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := actionFor(acts, "branch", "merged-free"); !ok || !a.Done {
		t.Fatalf("merged branch without a worktree not deleted: %+v", acts)
	}
	if _, ok := actionFor(acts, "branch", "ahead"); ok {
		t.Fatalf("a branch with commits off main must not be touched: %+v", acts)
	}
	if _, ok := actionFor(acts, "branch", "main"); ok {
		t.Fatalf("the base branch must not be touched: %+v", acts)
	}
	if out := git(repo, "branch", "--list", "ahead"); out == "" {
		t.Fatal("unmerged branch deleted")
	}
}

// A repository whose decision records cite branches by name keeps every
// branch; its merged worktrees still go.
func TestSweepBranchOptOutKeepsBranchesButRemovesWorktrees(t *testing.T) {
	repo, git := sweepRepo(t)
	git(repo, "config", "captain.sweep.branches", "false")
	dir := addWorktree(t, repo, "done", git)
	git(repo, "branch", "merged-free")
	acts, err := SweepRepo(repo, sweepOpts())
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := actionFor(acts, "worktree", "done"); !ok || !a.Done {
		t.Fatalf("worktree not removed under branch opt-out: %+v", acts)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("worktree still on disk")
	}
	for _, b := range []string{"done", "merged-free"} {
		if out := git(repo, "branch", "--list", b); out == "" {
			t.Fatalf("branch %s deleted despite captain.sweep.branches=false", b)
		}
	}
}

func TestSweepRepoOptOut(t *testing.T) {
	repo, git := sweepRepo(t)
	git(repo, "config", "captain.sweep", "false")
	dir := addWorktree(t, repo, "done", git)
	acts, err := SweepRepo(repo, sweepOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 0 {
		t.Fatalf("opted-out repo swept: %+v", acts)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("opted-out repo lost a worktree")
	}
}

// A stale origin/main makes fewer things look merged, never more; the
// sweeper prefers it over a local main that may carry unpushed commits.
func TestSweepBaseIsOriginWhenPresent(t *testing.T) {
	upstream, git := sweepRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	git(upstream, "clone", "--quiet", upstream, clone)
	os.WriteFile(filepath.Join(clone, "local.txt"), []byte("l\n"), 0o644)
	git(clone, "add", "local.txt")
	git(clone, "commit", "--quiet", "-m", "local only")
	dir := addWorktree(t, clone, "on-local-main", git)
	acts, err := SweepRepo(clone, sweepOpts())
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := actionFor(acts, "worktree", "on-local-main"); !ok || a.Done || !strings.Contains(a.Skip, "not merged") {
		t.Fatalf("a worktree only on the unpushed local main must stay: %+v", acts)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("worktree removed")
	}
}
