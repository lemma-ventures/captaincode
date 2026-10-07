package captaincode

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

// CumulativeDiffResult contains the result of checking and applying
// the cumulative diff of a workflow (ROADMAP M3.2 / Q6).
type CumulativeDiffResult struct {
	ChangedFiles []string
	Patch        []byte
	Applied      bool
	Conflicts    []string
	PatchPath    string
	Error        error
}

// SnapshotCheckoutBase commits the current checkout's working tree using a
// private temporary index file (GIT_INDEX_FILE), capturing tracked, uncommitted,
// and untracked (non-ignored) files. The user's index and checkout are untouched.
// Returns the commit sha to serve as the initial base of the workflow.
func SnapshotCheckoutBase(ctx context.Context, repo string) (string, error) {
	root := GitRoot(repo)
	if root == "" {
		return "", fmt.Errorf("snapshot: %s is not inside a git repository", repo)
	}
	tmp, err := os.MkdirTemp("", "captain-snap-")
	if err != nil {
		return "", fmt.Errorf("snapshot temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	indexPath := filepath.Join(tmp, "index")

	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	headOut, err := exec.CommandContext(c, "git", "-C", root, "rev-parse", "HEAD").Output()
	parent := ""
	env := append(os.Environ(),
		"GIT_INDEX_FILE="+indexPath,
		"GIT_AUTHOR_NAME=Captain Code",
		"GIT_AUTHOR_EMAIL=captain@local",
		"GIT_COMMITTER_NAME=Captain Code",
		"GIT_COMMITTER_EMAIL=captain@local",
	)
	if err == nil {
		parent = strings.TrimSpace(string(headOut))
		metadata, metaErr := exec.CommandContext(c, "git", "-C", root, "show", "-s", "--format=%an%x00%ae%x00%at%x00%cn%x00%ce%x00%ct", parent).Output()
		if metaErr == nil {
			parts := strings.Split(strings.TrimSuffix(string(metadata), "\n"), "\x00")
			if len(parts) == 6 {
				env = append(os.Environ(),
					"GIT_INDEX_FILE="+indexPath,
					"GIT_AUTHOR_NAME="+parts[0],
					"GIT_AUTHOR_EMAIL="+parts[1],
					"GIT_AUTHOR_DATE=@"+parts[2]+" +0000",
					"GIT_COMMITTER_NAME="+parts[3],
					"GIT_COMMITTER_EMAIL="+parts[4],
					"GIT_COMMITTER_DATE=@"+parts[5]+" +0000",
				)
			}
		}
		readTree := exec.CommandContext(c, "git", "-C", root, "read-tree", parent)
		readTree.Env = env
		if out, err := readTree.CombinedOutput(); err != nil {
			return "", fmt.Errorf("snapshot read-tree %s: %w: %s", parent, err, strings.TrimSpace(string(out)))
		}
	}

	add := exec.CommandContext(c, "git", "-C", root, "add", "-A", ".")
	add.Env = env
	if out, err := add.CombinedOutput(); err != nil {
		return "", fmt.Errorf("snapshot git add -A: %w: %s", err, strings.TrimSpace(string(out)))
	}

	writeTree := exec.CommandContext(c, "git", "-C", root, "write-tree")
	writeTree.Env = env
	outTree, err := writeTree.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("snapshot write-tree: %w: %s", err, strings.TrimSpace(string(outTree)))
	}
	tree := strings.TrimSpace(string(outTree))

	args := []string{"-C", root, "-c", "commit.gpgsign=false", "commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	args = append(args, "-m", "Workflow base snapshot")
	commitCmd := exec.CommandContext(c, "git", args...)
	commitCmd.Env = env
	outCommit, err := commitCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("snapshot commit-tree: %w: %s", err, strings.TrimSpace(string(outCommit)))
	}
	commit := strings.TrimSpace(string(outCommit))
	if !pinnedRevision(commit) {
		return "", fmt.Errorf("snapshot commit %q is invalid", commit)
	}
	return commit, nil
}

// CommitStageHandoff applies an integration candidate into a scratch worktree
// at stageBase and commits the tree with commit-tree. The returned commit sha
// becomes the base for the next workflow stage (ROADMAP M3.2 / Q6).
func CommitStageHandoff(ctx context.Context, repo string, stageBase string, ic IntegrationCandidate) (string, error) {
	if ic.Status != IntegrationClean && ic.Status != IntegrationResolved {
		return stageBase, nil
	}
	if len(ic.Manifests) == 0 {
		return stageBase, nil
	}
	hasChanges := false
	for _, m := range ic.Manifests {
		if m.HasChanges() && !ic.dropped(m) {
			hasChanges = true
			break
		}
	}
	if !hasChanges {
		return stageBase, nil
	}

	root := GitRoot(repo)
	if root == "" {
		return stageBase, fmt.Errorf("handoff: %s is not inside a git repository", repo)
	}

	scratchWT, err := NewWorktree(ctx, root, stageBase)
	if err != nil {
		return stageBase, fmt.Errorf("handoff worktree: %w", err)
	}
	defer scratchWT.Close()

	if err := ApplyIntegrationCandidate(ctx, scratchWT.Dir, ic); err != nil {
		return stageBase, fmt.Errorf("handoff apply: %w", err)
	}

	tmp, err := os.MkdirTemp("", "captain-handoff-index-")
	if err != nil {
		return stageBase, fmt.Errorf("handoff temp index: %w", err)
	}
	defer os.RemoveAll(tmp)
	indexPath := filepath.Join(tmp, "index")
	env := append(os.Environ(),
		"GIT_INDEX_FILE="+indexPath,
		"GIT_AUTHOR_NAME=Captain Code",
		"GIT_AUTHOR_EMAIL=captain@local",
		"GIT_COMMITTER_NAME=Captain Code",
		"GIT_COMMITTER_EMAIL=captain@local",
	)

	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	readTree := exec.CommandContext(c, "git", "-C", scratchWT.Dir, "read-tree", stageBase)
	readTree.Env = env
	if out, err := readTree.CombinedOutput(); err != nil {
		return stageBase, fmt.Errorf("handoff read-tree %s: %w: %s", stageBase, err, strings.TrimSpace(string(out)))
	}

	add := exec.CommandContext(c, "git", "-C", scratchWT.Dir, "add", "-A", ".")
	add.Env = env
	if out, err := add.CombinedOutput(); err != nil {
		return stageBase, fmt.Errorf("handoff git add -A: %w: %s", err, strings.TrimSpace(string(out)))
	}

	writeTree := exec.CommandContext(c, "git", "-C", scratchWT.Dir, "write-tree")
	writeTree.Env = env
	outTree, err := writeTree.CombinedOutput()
	if err != nil {
		return stageBase, fmt.Errorf("handoff write-tree: %w: %s", err, strings.TrimSpace(string(outTree)))
	}
	tree := strings.TrimSpace(string(outTree))

	stageMsg := "Workflow stage snapshot"
	if ic.StageID != "" {
		stageMsg = fmt.Sprintf("Workflow %s snapshot", ic.StageID)
	}
	commitCmd := exec.CommandContext(c, "git", "-C", scratchWT.Dir, "-c", "commit.gpgsign=false", "commit-tree", tree, "-p", stageBase, "-m", stageMsg)
	commitCmd.Env = env
	outCommit, err := commitCmd.CombinedOutput()
	if err != nil {
		return stageBase, fmt.Errorf("handoff commit-tree: %w: %s", err, strings.TrimSpace(string(outCommit)))
	}
	nextBase := strings.TrimSpace(string(outCommit))
	if !pinnedRevision(nextBase) {
		return stageBase, fmt.Errorf("handoff commit %q is invalid", nextBase)
	}
	return nextBase, nil
}

// ApplyWorkflowCumulativeDiff makes one diff from baseCommit to finalCommit,
// checks it with git apply --check against the checkout, and applies it. If the
// user concurrently modified any of the touched files during the run, or if
// apply check fails, the patch is saved as a task artifact and not applied.
func ApplyWorkflowCumulativeDiff(ctx context.Context, repo string, baseCommit, finalCommit string, patchDir, taskID string) CumulativeDiffResult {
	if baseCommit == "" || finalCommit == "" || baseCommit == finalCommit {
		return CumulativeDiffResult{Applied: true}
	}
	root := GitRoot(repo)
	if root == "" {
		return CumulativeDiffResult{Applied: false, Error: fmt.Errorf("cumulative diff: %s is not in a git repo", repo)}
	}

	diffCmd := exec.CommandContext(ctx, "git", "-C", root, "diff", "--binary", "--no-renames", baseCommit, finalCommit, "--")
	patchBytes, err := diffCmd.Output()
	if err != nil {
		return CumulativeDiffResult{Applied: false, Error: fmt.Errorf("cumulative diff: %w", err)}
	}
	if len(bytes.TrimSpace(patchBytes)) == 0 {
		return CumulativeDiffResult{Applied: true}
	}

	filesCmd := exec.CommandContext(ctx, "git", "-C", root, "diff", "--name-only", baseCommit, finalCommit, "--")
	outFiles, err := filesCmd.Output()
	if err != nil {
		return CumulativeDiffResult{Applied: false, Error: fmt.Errorf("cumulative diff files: %w", err)}
	}
	files := strings.Fields(string(outFiles))

	for _, f := range files {
		if !safeRelativePath(f) {
			return CumulativeDiffResult{
				Applied: false,
				Error:   fmt.Errorf("cumulative diff: unsafe path %q in patch", f),
			}
		}
	}

	var conflicted []string
	for _, f := range files {
		diffCheck := exec.CommandContext(ctx, "git", "-C", root, "diff", "--name-only", baseCommit, "--", f)
		out, err := diffCheck.Output()
		if err == nil && len(bytes.TrimSpace(out)) > 0 {
			conflicted = append(conflicted, f)
			continue
		}
		catCmd := exec.CommandContext(ctx, "git", "-C", root, "cat-file", "-e", baseCommit+":"+f)
		if catCmd.Run() != nil {
			if _, err := os.Stat(filepath.Join(root, f)); err == nil {
				conflicted = append(conflicted, f)
			}
		}
	}

	checkCmd := exec.CommandContext(ctx, "git", "-C", root, "apply", "--check", "--whitespace=nowarn", "-")
	checkCmd.Stdin = bytes.NewReader(patchBytes)
	if checkErr := checkCmd.Run(); checkErr != nil && len(conflicted) == 0 {
		conflicted = append(conflicted, files...)
	}

	if len(conflicted) > 0 {
		patchPath := ""
		if patchDir != "" {
			_ = os.MkdirAll(patchDir, 0o755)
			patchPath = filepath.Join(patchDir, fmt.Sprintf("%s.cumulative.patch", taskID))
			_ = os.WriteFile(patchPath, patchBytes, 0o644)
		}
		return CumulativeDiffResult{
			ChangedFiles: files,
			Patch:        patchBytes,
			Applied:      false,
			Conflicts:    conflicted,
			PatchPath:    patchPath,
		}
	}

	applyCmd := exec.CommandContext(ctx, "git", "-C", root, "apply", "--whitespace=nowarn", "-")
	applyCmd.Stdin = bytes.NewReader(patchBytes)
	if out, err := applyCmd.CombinedOutput(); err != nil {
		return CumulativeDiffResult{
			ChangedFiles: files,
			Patch:        patchBytes,
			Applied:      false,
			Error:        fmt.Errorf("git apply: %w: %s", err, strings.TrimSpace(string(out))),
		}
	}

	return CumulativeDiffResult{
		ChangedFiles: files,
		Patch:        patchBytes,
		Applied:      true,
	}
}

func safeRelativePath(p string) bool {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for _, part := range parts {
		if part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}
