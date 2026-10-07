package captaincode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "--quiet", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "init.txt"), []byte("initial\n"), 0o644))
	run("add", "init.txt")
	run("commit", "--quiet", "-m", "init")
	return dir
}

func TestSnapshotCheckoutBaseIncludesDirtyAndUntracked(t *testing.T) {
	repo := initTestGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "init.txt"), []byte("dirty content\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked file\n"), 0o644))

	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repo
	statusBefore, err := statusCmd.CombinedOutput()
	require.NoError(t, err)

	commit, err := SnapshotCheckoutBase(context.Background(), repo)
	require.NoError(t, err)
	require.True(t, pinnedRevision(commit))

	// Status before == status after
	statusCmdAfter := exec.Command("git", "status", "--porcelain")
	statusCmdAfter.Dir = repo
	statusAfter, err := statusCmdAfter.CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, string(statusBefore), string(statusAfter), "user's index/status must be untouched")

	// Tree of commit contains both dirty init.txt and untracked.txt
	lsCmd := exec.Command("git", "ls-tree", "-r", "--name-only", commit)
	lsCmd.Dir = repo
	lsOut, err := lsCmd.CombinedOutput()
	require.NoError(t, err)
	files := strings.Fields(string(lsOut))
	assert.Contains(t, files, "init.txt")
	assert.Contains(t, files, "untracked.txt")

	// Verify content of init.txt in the commit is the dirty content
	showCmd := exec.Command("git", "show", commit+":init.txt")
	showCmd.Dir = repo
	showOut, err := showCmd.CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, "dirty content\n", string(showOut))
}

func TestCommitStageHandoffAndApplyCumulativeDiff(t *testing.T) {
	repo := initTestGitRepo(t)
	baseCommit, err := SnapshotCheckoutBase(context.Background(), repo)
	require.NoError(t, err)

	// Create a worktree at baseCommit, make a change
	wt, err := NewWorktree(context.Background(), repo, baseCommit)
	require.NoError(t, err)
	defer wt.Close()

	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, "feature.txt"), []byte("feature 1\n"), 0o644))
	diffDir := t.TempDir()
	manifest, err := CaptureManifest(context.Background(), wt.Dir, diffDir, baseCommit, "t-1", "s-1", "a-1", "claude")
	require.NoError(t, err)

	ic := BuildIntegrationCandidate("t-1", "s-1", baseCommit, []PatchManifest{manifest})
	assert.Equal(t, IntegrationClean, ic.Status)

	nextCommit, err := CommitStageHandoff(context.Background(), repo, baseCommit, ic)
	require.NoError(t, err)
	assert.NotEqual(t, baseCommit, nextCommit)
	assert.True(t, pinnedRevision(nextCommit))

	// Next commit should contain feature.txt
	showCmd := exec.Command("git", "show", nextCommit+":feature.txt")
	showCmd.Dir = repo
	showOut, err := showCmd.CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, "feature 1\n", string(showOut))

	// Checkout should still NOT have feature.txt before final apply
	assert.NoFileExists(t, filepath.Join(repo, "feature.txt"))

	// Final cumulative apply
	res := ApplyWorkflowCumulativeDiff(context.Background(), repo, baseCommit, nextCommit, diffDir, "t-1")
	assert.True(t, res.Applied)
	assert.Empty(t, res.Conflicts)
	assert.Contains(t, res.ChangedFiles, "feature.txt")
	assert.FileExists(t, filepath.Join(repo, "feature.txt"))
}

func TestApplyWorkflowCumulativeDiffConcurrentConflict(t *testing.T) {
	repo := initTestGitRepo(t)
	baseCommit, err := SnapshotCheckoutBase(context.Background(), repo)
	require.NoError(t, err)

	wt, err := NewWorktree(context.Background(), repo, baseCommit)
	require.NoError(t, err)
	defer wt.Close()

	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, "init.txt"), []byte("worker change\n"), 0o644))
	diffDir := t.TempDir()
	manifest, err := CaptureManifest(context.Background(), wt.Dir, diffDir, baseCommit, "t-2", "s-1", "a-1", "claude")
	require.NoError(t, err)

	ic := BuildIntegrationCandidate("t-2", "s-1", baseCommit, []PatchManifest{manifest})
	nextCommit, err := CommitStageHandoff(context.Background(), repo, baseCommit, ic)
	require.NoError(t, err)

	// User makes a concurrent edit to init.txt in repo checkout
	require.NoError(t, os.WriteFile(filepath.Join(repo, "init.txt"), []byte("user concurrent edit\n"), 0o644))

	res := ApplyWorkflowCumulativeDiff(context.Background(), repo, baseCommit, nextCommit, diffDir, "t-2")
	assert.False(t, res.Applied, "must refuse to overwrite user concurrent edit")
	assert.Contains(t, res.Conflicts, "init.txt")
	assert.FileExists(t, res.PatchPath, "must keep patch as task artifact")

	// User's file is untouched
	data, err := os.ReadFile(filepath.Join(repo, "init.txt"))
	require.NoError(t, err)
	assert.Equal(t, "user concurrent edit\n", string(data))
}
