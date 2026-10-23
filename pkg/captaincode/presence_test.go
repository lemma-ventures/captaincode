package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPresenceTierClassification(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", dir}, {"-C", dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk"}} {
		if out, e := exec.Command("git", args...).CombinedOutput(); e != nil {
			t.Fatal(string(out), e)
		}
	}
	for _, cmd := range []string{"git push origin main", "git push origin trunk", "git push origin HEAD:trunk", "git -C x push origin feature", "git push --force origin feature", "git push origin feature && rm file", "gh pr merge 1", "npm publish", "gh release create v1", "rm file", "payment transfer 100", "git merge feature"} {
		if !PresenceHighTier(GateAction{Tool: "bash", Command: cmd, Cwd: dir}) {
			t.Fatal(cmd)
		}
	}
	for _, cmd := range []string{"git push origin feature", "git commit -m change", "git add file.go", "go test ./..."} {
		if PresenceHighTier(GateAction{Tool: "bash", Command: cmd, Cwd: dir}) {
			t.Fatal(cmd)
		}
	}
	if !PresenceHighTier(GateAction{Tool: "payments_transfer"}) {
		t.Fatal("payment tool not gated")
	}
}

func TestPresencePublishAndPatchDeletionRequireDecision(t *testing.T) {
	for _, action := range []GateAction{
		{Tool: "bash", Command: "twine upload dist/package.whl"},
		{Tool: "apply_patch", Command: `{"patch":"*** Begin Patch\n*** Delete File: data.txt\n*** End Patch"}`},
		{Tool: "patch", Command: `{"patch":"--- data.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-data"}`},
		{Tool: "bash", Command: "git branch -d feature"},
	} {
		if !PresenceHighTier(action) {
			t.Errorf("no signed decision for %+v", action)
		}
	}
}

// A signed turn works in its project: the linked folder, a folder inside it,
// and every linked worktree of the same repository. Another repository stays
// refused, and a leading `cd` back into the project brings a worker home.
func TestPresenceProjectFoldersAndWorktrees(t *testing.T) {
	root := t.TempDir()
	repo, other := filepath.Join(root, "project-a"), filepath.Join(root, "captaincode")
	wt := filepath.Join(root, "project-a-wt-decoder")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatal(string(out), e)
		}
	}
	git("init", "--quiet", repo)
	git("-C", repo, "commit", "--quiet", "--allow-empty", "-m", "init")
	git("-C", repo, "worktree", "add", "--quiet", "-b", "decoder", wt)
	git("init", "--quiet", other)
	sub := filepath.Join(repo, ".worktrees", "x")
	if e := os.MkdirAll(sub, 0o755); e != nil {
		t.Fatal(e)
	}
	for _, c := range []string{repo, sub, wt} {
		if !presenceFolderAllowed(repo, c) {
			t.Errorf("%s is part of the project", c)
		}
	}
	for _, c := range []string{other, root} {
		if presenceFolderAllowed(repo, c) {
			t.Errorf("%s is not part of the project", c)
		}
	}
	back := GateAction{Tool: "bash", Command: "cd " + repo + " && git status", Cwd: other}
	if !presenceFolderAllowed(repo, presenceEffectiveCwd(back)) {
		t.Error("a cd back into the project is allowed from anywhere")
	}
	away := GateAction{Tool: "bash", Command: "cd " + other + " && git status", Cwd: repo}
	if presenceFolderAllowed(repo, presenceEffectiveCwd(away)) {
		t.Error("a cd out of the project is refused")
	}
	rel := GateAction{Tool: "bash", Command: "cd ../project-a-wt-decoder; git log", Cwd: repo}
	if !presenceFolderAllowed(repo, presenceEffectiveCwd(rel)) {
		t.Error("a relative cd into a worktree of the project is allowed")
	}
}

// Git counts by subcommand, not by the words around it.
func TestPresenceGitSubcommands(t *testing.T) {
	for _, cmd := range []string{"git merge feature", "git -C ../x merge feature", "git status && git reset --hard",
		"echo $(git rebase -i HEAD~2)", "git clean -fdx", "git tag v1.2", "git --no-pager -c core.pager=cat rebase main",
		"git filter-repo --path x"} {
		if !presenceGitHigh(cmd) {
			t.Errorf("%q rewrites or discards work", cmd)
		}
	}
	for _, cmd := range []string{"git merge-base main HEAD", "git log --grep merge --oneline", "git show HEAD:docs/reset.md",
		"git tag", "git tag -l 'v*'", "git status --short", "git log --format=%s origin/main..main", "cat merge.go"} {
		if presenceGitHigh(cmd) {
			t.Errorf("%q only reads", cmd)
		}
	}
}

// A feature push may name the project with -C and tidy its output; a push to
// the default branch, or into another repository, still needs a decision.
func TestPresenceFeaturePushForms(t *testing.T) {
	root := t.TempDir()
	repo, other := filepath.Join(root, "project-a"), filepath.Join(root, "project-b")
	for _, d := range []string{repo, other} {
		for _, args := range [][]string{{"init", "--quiet", d}, {"-C", d, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"}} {
			if out, e := exec.Command("git", args...).CombinedOutput(); e != nil {
				t.Fatal(string(out), e)
			}
		}
	}
	for _, cmd := range []string{"git push origin decoder-combined", "git -C " + repo + " push origin decoder-combined 2>&1 | tail -6",
		"git push origin feature | head -n 20"} {
		if !presenceFeaturePush(cmd, repo) {
			t.Errorf("%q is a feature push", cmd)
		}
	}
	for _, cmd := range []string{"git push origin main", "git -C " + other + " push origin feature", "git push origin feature | tail -5; rm -rf x",
		"git push origin feature && git push origin main", "git -C " + repo + " push origin main 2>&1 | tail -5"} {
		if presenceFeaturePush(cmd, repo) {
			t.Errorf("%q is not a plain feature push", cmd)
		}
	}
}
