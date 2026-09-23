package captaincode

// Artifact and integration contract (ROADMAP M3.2). M3.1 gave each concurrent
// writer its own git worktree so parallel workers do not stomp each other's
// files. But when the stage finishes, those worktrees are closed and the FILE
// CHANGES are discarded — only the worker's text output survives in the
// conversation. A workflow that asks three workers to fix different parts of
// the codebase loses all three patches.
//
// M3.2 captures what each worker changed before the worktree closes:
//
//   - a PatchManifest is an immutable record of one worker's changes: the
//     files it touched, a sha256 digest of its diff, the base revision it
//     started from, and the check evidence (what passed, what failed).
//   - an IntegrationCandidate collects the manifests from one stage's workers,
//     detects conflicts (two workers changing the same file), and carries a
//     status the director review can read before anything touches the user's
//     branch.
//   - conflict detection is file-level, not semantic: two workers that both
//     edit `queue.ts` conflict even if their edits are compatible, because
//     the review — not the harness — decides whether a mechanical merge is
//     safe. A worktree that produced no changes is not a conflict; it is a
//     worker that answered in text only.
//
// The manifest is the foundation for M3.3 (durable lifecycle persists artifact
// references) and M3.5 (structured handoffs carry verified artifacts).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ArtifactVersion is stamped on every manifest and candidate. A reader that
// does not understand the shape refuses it, the same contract the other
// versioned types carry.
const ArtifactVersion = 1

// PatchManifest is an immutable record of what one worker changed in its
// isolated worktree. Once created it is never mutated; the diff is saved to a
// file so the manifest is a reference, not a container.
type PatchManifest struct {
	Version   int    `json:"version"`
	TaskID    string `json:"task_id"`
	StageID   string `json:"stage_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
	Leg       string `json:"leg"`
	// Worker names the worker within its stage (a leg can run twice in one
	// stage). The director's ruling on a conflict names this id.
	Worker       string         `json:"worker,omitempty"`
	BaseRevision string         `json:"base_revision"`
	WorktreeDir  string         `json:"worktree_dir,omitempty"`
	ChangedFiles []string       `json:"changed_files"`
	DiffDigest   string         `json:"diff_digest"`
	DiffPath     string         `json:"diff_path,omitempty"`
	Check        *CheckEvidence `json:"check,omitempty"`
	// TestEvidence is the project's own test suite run in the worktree after
	// the worker's changes (M3.2). Distinct from Check (the gate): a gate is
	// a specific acceptance command the workflow defines; the test suite is
	// broader evidence that the changes did not break existing tests. Nil
	// when the project has no detectable test suite.
	TestEvidence *CheckEvidence `json:"test_evidence,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// CheckEvidence records what happened when the worker's own checks ran in its
// worktree. A worker whose gate passed has different evidence from one whose
// gate failed and was repaired, and the integration candidate carries both.
type CheckEvidence struct {
	Command  []string `json:"command,omitempty"`
	ExitCode int      `json:"exit_code"`
	Passed   bool     `json:"passed"`
	Output   string   `json:"output,omitempty"`
	// TimedOut: the deadline killed the command, so it returned no verdict.
	// A suite that did not finish has NOT failed, and must not be treated as
	// a failure (2026-09-21: `cargo test` in a large workspace ran 22 minutes
	// past a 5-minute deadline, was called a failure, and bought a repair
	// attempt on a cheap leg for work nobody had judged).
	TimedOut bool          `json:"timed_out,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
}

// IntegrationStatus classifies what an IntegrationCandidate contains.
const (
	IntegrationClean      = "clean"      // no conflicts; changes can be merged
	IntegrationConflicted = "conflicted" // two or more workers changed the same file
	IntegrationEmpty      = "empty"      // no worker produced any file changes
	IntegrationResolved   = "resolved"   // conflicted, and the director picked whose changes land
)

// IntegrationCandidate collects the manifests from one stage's workers and
// classifies whether their changes can be merged without human adjudication.
type IntegrationCandidate struct {
	Version           int                `json:"version"`
	TaskID            string             `json:"task_id"`
	StageID           string             `json:"stage_id,omitempty"`
	BaseRevision      string             `json:"base_revision"`
	Manifests         []PatchManifest    `json:"manifests"`
	Conflicts         []FileConflict     `json:"conflicts,omitempty"`
	SemanticConflicts []SemanticConflict `json:"semantic_conflicts,omitempty"`
	Status            string             `json:"status"`
	// Winner, Ruling and Dropped record the director's call on a conflicted
	// candidate (status resolved): whose changes land, why, and which
	// workers' changes were set aside. Their diffs stay on disk.
	Winner    string    `json:"winner,omitempty"`
	Ruling    string    `json:"ruling,omitempty"`
	Dropped   []string  `json:"dropped,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// FileConflict records a file that two or more workers changed.
type FileConflict struct {
	File    string   `json:"file"`
	Workers []string `json:"workers"`
}

// SemanticConflict records a cross-worker dependency: worker A changed file X,
// worker B changed file Y, and Y imports or depends on X (or vice versa).
// Their changes do not overlap at the file level, but they are semantically
// coupled — a signature change in X may break the usage in Y — and the review
// should check. This is advisory: it does not change the integration status
// (file-level conflicts still drive conflicted), but it surfaces the coupling
// the file-level check cannot see.
type SemanticConflict struct {
	File      string   `json:"file"`
	DependsOn string   `json:"depends_on"`
	Workers   []string `json:"workers"`
	Reason    string   `json:"reason"`
}

// CaptureManifest inspects one worker's worktree and produces an immutable
// PatchManifest. The diff is saved to diffDir so the manifest is a reference
// rather than a container. A worktree that produced no file changes returns a
// manifest with empty ChangedFiles and a zero DiffDigest — that is a valid
// record of a worker that answered in text only.
func CaptureManifest(ctx context.Context, dir, diffDir, baseRevision, taskID, stageID, attemptID, leg string) (PatchManifest, error) {
	m := PatchManifest{
		Version:      ArtifactVersion,
		TaskID:       taskID,
		StageID:      stageID,
		AttemptID:    attemptID,
		Leg:          leg,
		BaseRevision: baseRevision,
		WorktreeDir:  dir,
		CreatedAt:    time.Now(),
	}
	files, err := changedFilesInWorktree(ctx, dir, baseRevision)
	if err != nil {
		return m, fmt.Errorf("capture manifest: %w", err)
	}
	m.ChangedFiles = files
	if len(files) == 0 {
		return m, nil
	}
	diff, err := diffWorktree(ctx, dir, baseRevision)
	if err != nil {
		return m, fmt.Errorf("capture manifest: diff: %w", err)
	}
	h := sha256.Sum256(diff)
	m.DiffDigest = fmt.Sprintf("%x", h)
	if diffDir != "" {
		path := filepath.Join(diffDir, fmt.Sprintf("%s.%s.diff", leg, m.DiffDigest[:12]))
		if err := os.MkdirAll(diffDir, 0o755); err != nil {
			return m, fmt.Errorf("capture manifest: diff dir: %w", err)
		}
		if err := os.WriteFile(path, diff, 0o644); err != nil {
			return m, fmt.Errorf("capture manifest: write diff: %w", err)
		}
		m.DiffPath = path
	}
	return m, nil
}

// RecordCheck attaches check evidence to a manifest after the gate has run.
// A manifest without check evidence is one whose stage had no gate.
func (m *PatchManifest) RecordCheck(command []string, exitCode int, passed bool, output string) {
	m.Check = &CheckEvidence{
		Command:  command,
		ExitCode: exitCode,
		Passed:   passed,
		Output:   tail(output),
	}
}

// HasChanges reports whether the worker touched any files.
func (m PatchManifest) HasChanges() bool { return len(m.ChangedFiles) > 0 }

// CaptureTestEvidence runs the project's own test suite in the worker's
// worktree directory and captures the result (M3.2). This is distinct from
// the gate check: the gate is a specific acceptance command the workflow
// defines; the test suite is broader evidence that the worker's changes did
// not break existing tests. The test command is auto-detected from the
// project's manifest files (go.mod, package.json, pyproject.toml, Cargo.toml).
// Returns nil when no test suite is detected — not a failure, just no signal.
func CaptureTestEvidence(ctx context.Context, dir string) (*CheckEvidence, error) {
	if os.Getenv(testEvidenceDepthEnv) != "" {
		return nil, nil
	}
	cmd, ok := detectTestCommand(dir)
	if !ok {
		return nil, nil
	}
	budget := TestEvidenceTimeout()
	c, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	start := time.Now()
	execCmd := exec.CommandContext(c, "sh", "-c", cmd)
	execCmd.Dir = dir
	execCmd.Env = append(os.Environ(), testEvidenceDepthEnv+"=1")
	// The deadline has to kill the whole PROCESS GROUP, not the shell alone:
	// `sh -c "cargo test"` leaves cargo and every rustc holding the output
	// pipe, so CombinedOutput keeps reading long after the shell is dead - a
	// 5-minute budget took 22 minutes to return (live 2026-09-21, lemma).
	// WaitDelay is the second backstop: if something still holds the pipe,
	// Wait returns anyway.
	execCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	execCmd.Cancel = func() error {
		if execCmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-execCmd.Process.Pid, syscall.SIGKILL); err != nil {
			return execCmd.Process.Kill() // no group (already reaped): the shell alone
		}
		return nil
	}
	execCmd.WaitDelay = 10 * time.Second
	out, err := execCmd.CombinedOutput()
	ce := &CheckEvidence{
		Command:  []string{"sh", "-c", cmd},
		ExitCode: 0,
		Passed:   err == nil,
		Output:   tail(string(out)),
		Duration: time.Since(start),
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			ce.ExitCode = ee.ExitCode()
		} else {
			ce.ExitCode = -1
			ce.Output = fmt.Sprintf("%s\n(error: %v)", ce.Output, err)
		}
	}
	// A killed command reports no verdict at all: the caller must not read
	// "did not finish in time" as "the tests fail".
	if c.Err() != nil && !ce.Passed {
		ce.TimedOut = true
		ce.Output = fmt.Sprintf("%s\n(captain: no verdict - `%s` did not finish within %s and was killed)",
			ce.Output, cmd, budget)
	}
	return ce, nil
}

// TestEvidenceTimeout bounds one verification run.
// CAPTAIN_VERIFY_TIMEOUT (Go duration), default 5m.
func TestEvidenceTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_VERIFY_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 5 * time.Minute
}

// detectTestCommand inspects the project directory for manifest files and
// returns the appropriate test command. Returns ok=false when no known
// project type is detected — the worktree has no test suite to run.
func detectTestCommand(dir string) (string, bool) {
	for _, tc := range testCommandByFile {
		if fileExists(filepath.Join(dir, tc.file)) {
			return tc.command, true
		}
	}
	return "", false
}

var testCommandByFile = []struct {
	file    string
	command string
}{
	{"go.mod", "go test ./... 2>&1"},
	{"package.json", "npm test 2>&1"},
	{"pyproject.toml", "python -m pytest 2>&1"},
	{"pytest.ini", "python -m pytest 2>&1"},
	{"setup.py", "python -m pytest 2>&1"},
	{"Cargo.toml", "cargo test 2>&1"},
	{"Makefile", "make test 2>&1"},
}

// testEvidenceDepthEnv marks a process already inside a test-evidence run. A
// worktree of this very repo detects `go test ./...`, which would recurse:
// the suite re-enters CaptureTestEvidence and forks without bound. The child
// inherits the marker and the nested call returns no signal instead.
const testEvidenceDepthEnv = "CAPTAIN_TEST_EVIDENCE_DEPTH"

// BuildIntegrationCandidate collects manifests from one stage's workers and
// detects file-level conflicts. Two workers that changed the same file
// conflict even if their edits are compatible: the review decides whether a
// mechanical merge is safe, not the harness.
func BuildIntegrationCandidate(taskID, stageID, baseRevision string, manifests []PatchManifest) IntegrationCandidate {
	c := IntegrationCandidate{
		Version:      ArtifactVersion,
		TaskID:       taskID,
		StageID:      stageID,
		BaseRevision: baseRevision,
		Manifests:    manifests,
		CreatedAt:    time.Now(),
	}
	fileOwners := map[string][]string{}
	anyChanges := false
	for _, m := range manifests {
		if !m.HasChanges() {
			continue
		}
		anyChanges = true
		for _, f := range m.ChangedFiles {
			fileOwners[f] = append(fileOwners[f], m.Leg)
		}
	}
	for f, workers := range fileOwners {
		if len(workers) > 1 {
			sort.Strings(workers)
			c.Conflicts = append(c.Conflicts, FileConflict{File: f, Workers: workers})
		}
	}
	sort.Slice(c.Conflicts, func(i, j int) bool { return c.Conflicts[i].File < c.Conflicts[j].File })
	c.SemanticConflicts = DetectSemanticConflicts(manifests)
	sort.Slice(c.SemanticConflicts, func(i, j int) bool {
		if c.SemanticConflicts[i].File != c.SemanticConflicts[j].File {
			return c.SemanticConflicts[i].File < c.SemanticConflicts[j].File
		}
		return c.SemanticConflicts[i].DependsOn < c.SemanticConflicts[j].DependsOn
	})
	switch {
	case !anyChanges:
		c.Status = IntegrationEmpty
	case len(c.Conflicts) > 0:
		c.Status = IntegrationConflicted
	default:
		c.Status = IntegrationClean
	}
	return c
}

// HasConflicts reports whether any two workers changed the same file.
func (c IntegrationCandidate) HasConflicts() bool { return len(c.Conflicts) > 0 }

// HasSemanticConflicts reports whether any two workers changed files that have
// an import/dependency relationship without a file-level conflict.
func (c IntegrationCandidate) HasSemanticConflicts() bool { return len(c.SemanticConflicts) > 0 }

// ManifestID is the id a ruling names: the worker id when the caller set
// one, else the leg.
func (m PatchManifest) ManifestID() string {
	if m.Worker != "" {
		return m.Worker
	}
	return m.Leg
}

// Contested lists the ids of the workers whose changes overlap another
// worker's, in manifest order. These are the workers a ruling chooses between.
func (c IntegrationCandidate) Contested() []string {
	owners := map[string]int{}
	for _, m := range c.Manifests {
		for _, f := range m.ChangedFiles {
			owners[f]++
		}
	}
	var out []string
	for _, m := range c.Manifests {
		for _, f := range m.ChangedFiles {
			if owners[f] > 1 {
				out = append(out, m.ManifestID())
				break
			}
		}
	}
	return out
}

// Resolve settles a conflicted candidate on one worker's changes. Workers do
// not get their overlapping edits merged: the winner's changes land, every
// other contested worker's changes are dropped whole, and a worker that
// touched nothing anyone else touched keeps its changes. The result has no
// overlapping files by construction, so it applies mechanically.
func (c IntegrationCandidate) Resolve(winner, ruling string) (IntegrationCandidate, error) {
	if c.Status != IntegrationConflicted {
		return c, fmt.Errorf("resolve: candidate is %s, not conflicted", c.Status)
	}
	contested := c.Contested()
	found := false
	for _, id := range contested {
		if id == winner {
			found = true
		}
	}
	if !found {
		return c, fmt.Errorf("resolve: %q is not one of the conflicting workers (%s)", winner, strings.Join(contested, ", "))
	}
	c.Winner, c.Ruling, c.Dropped = winner, ruling, nil
	for _, id := range contested {
		if id != winner {
			c.Dropped = append(c.Dropped, id)
		}
	}
	c.Status = IntegrationResolved
	return c, nil
}

// dropped reports whether a ruling set this manifest's changes aside.
func (c IntegrationCandidate) dropped(m PatchManifest) bool {
	for _, id := range c.Dropped {
		if id == m.ManifestID() {
			return true
		}
	}
	return false
}

// ApplyIntegrationCandidate replays a clean candidate's diffs into the target
// directory. Each manifest's saved diff (tracked changes + new files) is
// applied via `git apply`, so the result is a working-tree change the user can
// review, stage or discard. A conflicted or empty candidate is refused: only
// a clean candidate (no file-level overlap), or a resolved one with the
// losing workers' changes left out, is safe to apply mechanically.
// Every diff is checked with git apply --check before any of them is written,
// so a later diff that does not apply leaves the tree untouched.
func ApplyIntegrationCandidate(ctx context.Context, targetDir string, ic IntegrationCandidate) error {
	if ic.Status != IntegrationClean && ic.Status != IntegrationResolved {
		return fmt.Errorf("apply: candidate is %s, not clean or resolved", ic.Status)
	}
	type pending struct {
		leg  string
		diff []byte
	}
	var diffs []pending
	for _, m := range ic.Manifests {
		if !m.HasChanges() || m.DiffPath == "" || ic.dropped(m) {
			continue
		}
		diff, err := os.ReadFile(m.DiffPath)
		if err != nil {
			return fmt.Errorf("apply: read diff %s: %w", m.DiffPath, err)
		}
		if len(diff) == 0 {
			continue
		}
		diffs = append(diffs, pending{leg: m.Leg, diff: diff})
	}
	apply := func(check bool, p pending) error {
		args := []string{"-C", targetDir, "apply", "--whitespace=nowarn"}
		if check {
			args = append(args, "--check")
		}
		args = append(args, "-")
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(c, "git", args...)
		cmd.Stdin = bytes.NewReader(p.diff)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("apply: git apply %s: %w: %s", p.leg, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	for _, p := range diffs {
		if err := apply(true, p); err != nil {
			return err
		}
	}
	for _, p := range diffs {
		if err := apply(false, p); err != nil {
			return err
		}
	}
	return nil
}

// ChangedFiles returns the union of all files touched across every manifest.
func (c IntegrationCandidate) ChangedFiles() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range c.Manifests {
		for _, f := range m.ChangedFiles {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ConflictFiles returns just the file paths that are in conflict.
func (c IntegrationCandidate) ConflictFiles() []string {
	out := make([]string, len(c.Conflicts))
	for i, cf := range c.Conflicts {
		out[i] = cf.File
	}
	return out
}

// Summary returns a one-line status for the progress feed and `captain why`.
func (c IntegrationCandidate) Summary() string {
	workers := len(c.Manifests)
	files := len(c.ChangedFiles())
	var testSummary string
	passed, total := c.TestCounts()
	if total > 0 {
		if passed == total {
			testSummary = fmt.Sprintf(", tests %d/%d pass", passed, total)
		} else {
			testSummary = fmt.Sprintf(", tests %d/%d FAIL", passed, total)
		}
	}
	var semSummary string
	if len(c.SemanticConflicts) > 0 {
		semSummary = fmt.Sprintf(", %d semantic", len(c.SemanticConflicts))
	}
	switch c.Status {
	case IntegrationEmpty:
		return fmt.Sprintf("%d workers, no file changes%s%s", workers, testSummary, semSummary)
	case IntegrationConflicted:
		return fmt.Sprintf("%d workers, %d files changed, %d conflicts%s%s", workers, files, len(c.Conflicts), testSummary, semSummary)
	case IntegrationResolved:
		return fmt.Sprintf("%d workers, %d files changed, %d conflicts settled on %s%s%s", workers, files, len(c.Conflicts), c.Winner, testSummary, semSummary)
	default:
		return fmt.Sprintf("%d workers, %d files changed, no conflicts%s%s", workers, files, testSummary, semSummary)
	}
}

// TestCounts returns how many workers' test suites passed and the total that
// ran. A worker whose project had no detectable test suite is not counted.
func (c IntegrationCandidate) TestCounts() (passed, total int) {
	for _, m := range c.Manifests {
		if m.TestEvidence == nil {
			continue
		}
		total++
		if m.TestEvidence.Passed {
			passed++
		}
	}
	return passed, total
}

// changedFilesInWorktree lists tracked and untracked changes relative to the
// base revision, the same approach eval.go's changedFiles uses. A worktree is
// a full checkout, so git diff and git ls-files work directly.
func changedFilesInWorktree(ctx context.Context, dir, revision string) ([]string, error) {
	seen := map[string]bool{}
	for _, args := range [][]string{
		{"git", "diff", "--name-only", "--no-renames", "-z", revision, "--"},
		{"git", "ls-files", "--others", "--exclude-standard", "-z"},
	} {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		cmd := exec.CommandContext(c, args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.Output()
		cancel()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return nil, fmt.Errorf("%s exited %d: %s", args[0], ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
			}
			return nil, err
		}
		for _, name := range strings.Split(string(out), "\x00") {
			if name != "" {
				seen[name] = true
			}
		}
	}
	var files []string
	for name := range seen {
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
}

// diffWorktree captures the full unified diff of all changes relative to the
// base revision, including untracked (new) files. Untracked files are staged
// as intent-to-add so git diff includes them as additions, then unstaged so
// the worktree's index is unchanged. The diff is what ApplyIntegrationCandidate
// replays into the user's workspace.
func diffWorktree(ctx context.Context, dir, revision string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	exec.CommandContext(c, "git", "-C", dir, "add", "-N", ".").Run()
	defer exec.CommandContext(c, "git", "-C", dir, "reset", "--quiet", "--").Run()
	cmd := exec.CommandContext(c, "git", "-C", dir, "diff", "--binary", "--no-renames", revision, "--")
	cmd.Dir = dir
	return cmd.Output()
}

// CaptureSoloArtifact captures what a solo worker changed in the user's
// workspace (M3.5). A solo turn has no worktree and no IntegrationCandidate,
// but it may still have produced file changes. This function diffs the
// workspace against HEAD and returns the changed files and a sha256 digest of
// the diff, the same data CaptureManifest produces for parallel workers.
//
// diffDir is where the diff file is saved (same pattern as CaptureManifest).
// When empty, no file is written and only the digest and file list are
// returned. Returns empty ChangedFiles and a zero digest when the worker
// produced no file changes — a valid record of a text-only answer.
func CaptureSoloArtifact(ctx context.Context, dir, diffDir, leg string) (files []string, digest string, diffPath string, err error) {
	head, err := currentHead(ctx, dir)
	if err != nil {
		return nil, "", "", fmt.Errorf("solo artifact: %w", err)
	}
	files, err = changedFilesInWorktree(ctx, dir, head)
	if err != nil {
		return nil, "", "", fmt.Errorf("solo artifact: changed files: %w", err)
	}
	if len(files) == 0 {
		return nil, "", "", nil
	}
	diff, err := diffWorktree(ctx, dir, head)
	if err != nil {
		return files, "", "", fmt.Errorf("solo artifact: diff: %w", err)
	}
	h := sha256.Sum256(diff)
	digest = fmt.Sprintf("%x", h)
	if diffDir != "" {
		diffPath = filepath.Join(diffDir, fmt.Sprintf("solo-%s.%s.diff", leg, digest[:12]))
		if err := os.MkdirAll(diffDir, 0o755); err != nil {
			return files, digest, "", fmt.Errorf("solo artifact: diff dir: %w", err)
		}
		if err := os.WriteFile(diffPath, diff, 0o644); err != nil {
			return files, digest, "", fmt.Errorf("solo artifact: write diff: %w", err)
		}
	}
	return files, digest, diffPath, nil
}

// currentHead returns the commit sha of HEAD in the given directory, or an
// error when the directory is not a git repository.
func currentHead(ctx context.Context, dir string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD in %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DetectSemanticConflicts finds cross-worker dependency relationships that
// file-level conflict detection misses. Worker A changes types.go, worker B
// changes handler.go which imports types.go: no file overlap, but their
// changes are semantically coupled because a signature change in types.go
// may break the usage in handler.go.
//
// The function reads each manifest's changed files from its WorktreeDir
// (when set), parses import statements for Go, JS/TS and Python, resolves
// them to repo-relative paths, and checks whether any resolved dependency
// was changed by a different worker. A semantic conflict is advisory: it
// does not change the integration status, but surfaces the coupling for the
// review to check. File-level conflicts (same file, two workers) are already
// caught and are not duplicated here.
func DetectSemanticConflicts(manifests []PatchManifest) []SemanticConflict {
	changedByWorker := map[string]string{}
	for _, m := range manifests {
		for _, f := range m.ChangedFiles {
			if _, exists := changedByWorker[f]; !exists {
				changedByWorker[f] = m.Leg
			}
		}
	}
	if len(changedByWorker) < 2 {
		return nil
	}
	changedFiles := make([]string, 0, len(changedByWorker))
	for f := range changedByWorker {
		changedFiles = append(changedFiles, f)
	}
	sort.Strings(changedFiles)
	var conflicts []SemanticConflict
	seen := map[string]bool{}
	for _, m := range manifests {
		if m.WorktreeDir == "" || !m.HasChanges() {
			continue
		}
		for _, f := range m.ChangedFiles {
			deps := parseImports(m.WorktreeDir, f)
			for _, dep := range deps {
				if dep == f || dep == "." {
					continue
				}
				matchedFile, matchedLeg := findChangedDependency(dep, changedFiles, changedByWorker, m.Leg)
				if matchedFile == "" {
					continue
				}
				if changedByWorker[f] == matchedLeg {
					continue
				}
				key := conflictKey(f, matchedFile, m.Leg, matchedLeg)
				if seen[key] {
					continue
				}
				seen[key] = true
				workers := []string{m.Leg, matchedLeg}
				sort.Strings(workers)
				conflicts = append(conflicts, SemanticConflict{
					File:      f,
					DependsOn: matchedFile,
					Workers:   workers,
					Reason:    deps_reason(f),
				})
			}
		}
	}
	return conflicts
}

// findChangedDependency checks whether a dependency path (which may be a file
// or a directory) matches any changed file changed by a different worker.
// For directory-level deps (Go: import resolves to pkg/types), any changed
// file under that directory (pkg/types/types.go) counts.
func findChangedDependency(dep string, changedFiles []string, changedByWorker map[string]string, importerLeg string) (string, string) {
	for _, cf := range changedFiles {
		if cf == dep {
			leg := changedByWorker[cf]
			if leg != importerLeg {
				return cf, leg
			}
			continue
		}
		if strings.HasPrefix(cf, dep+"/") {
			leg := changedByWorker[cf]
			if leg != importerLeg {
				return cf, leg
			}
		}
	}
	return "", ""
}

func conflictKey(f, dep, leg1, leg2 string) string {
	pair := []string{leg1 + ":" + f, leg2 + ":" + dep}
	sort.Strings(pair)
	return pair[0] + "|" + pair[1]
}

func deps_reason(file string) string {
	ext := strings.ToLower(filepath.Ext(file))
	switch ext {
	case ".go":
		return "go import"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs":
		return "js/ts import"
	case ".py":
		return "python import"
	default:
		return "dependency"
	}
}

// parseImports reads a file from the worktree and returns the repo-relative
// paths of other files it imports or depends on. Only same-project imports
// are returned — external packages (npm, PyPI, Go stdlib + third-party) are
// not resolvable to file paths and are skipped.
func parseImports(worktreeDir, file string) []string {
	abs := filepath.Join(worktreeDir, file)
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(file))
	switch ext {
	case ".go":
		return parseGoImports(worktreeDir, file, data)
	case ".ts", ".tsx", ".js", ".jsx", ".mjs":
		return parseJSImports(worktreeDir, file, data)
	case ".py":
		return parsePythonImports(worktreeDir, file, data)
	default:
		return nil
	}
}

// parseGoImports resolves Go import paths to repo-relative directories using
// the module path from go.mod. An import like "github.com/user/project/pkg/types"
// resolves to "pkg/types" when go.mod declares module github.com/user/project.
// We then check if any changed file lives in that directory.
func parseGoImports(worktreeDir, importingFile string, data []byte) []string {
	modulePath := readGoModulePath(worktreeDir)
	if modulePath == "" {
		return nil
	}
	var deps []string
	inImportBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import (") || strings.HasPrefix(trimmed, "import\t(") {
			inImportBlock = true
			continue
		}
		if inImportBlock && trimmed == ")" {
			inImportBlock = false
			continue
		}
		var path string
		if inImportBlock {
			if idx := strings.Index(trimmed, "\""); idx >= 0 {
				rest := trimmed[idx+1:]
				if end := strings.Index(rest, "\""); end >= 0 {
					path = rest[:end]
				}
			}
		} else if strings.HasPrefix(trimmed, "import ") {
			if idx := strings.Index(trimmed, "\""); idx >= 0 {
				rest := trimmed[idx+1:]
				if end := strings.Index(rest, "\""); end >= 0 {
					path = rest[:end]
				}
			}
		}
		if path == "" {
			continue
		}
		if strings.HasPrefix(path, modulePath+"/") {
			deps = append(deps, strings.TrimPrefix(path, modulePath+"/"))
		} else if path == modulePath {
			deps = append(deps, ".")
		}
	}
	return uniqueNonEmpty(deps)
}

// readGoModulePath reads the module path from go.mod in the worktree root.
func readGoModulePath(worktreeDir string) string {
	data, err := os.ReadFile(filepath.Join(worktreeDir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// parseJSImports resolves relative JS/TS imports (./, ../, ~/) to
// repo-relative file paths. External packages (no leading dot) are skipped.
func parseJSImports(worktreeDir, importingFile string, data []byte) []string {
	var deps []string
	importerDir := filepath.Dir(importingFile)
	for _, m := range jsImportRe.FindAllSubmatch(data, -1) {
		spec := strings.TrimSpace(string(m[1]))
		if spec == "" || spec[0] != '.' {
			continue
		}
		resolved := normalizePath(filepath.Join(importerDir, spec))
		deps = append(deps, resolveJSImportPath(worktreeDir, resolved)...)
	}
	for _, m := range jsRequireRe.FindAllSubmatch(data, -1) {
		spec := strings.TrimSpace(string(m[1]))
		if spec == "" || spec[0] != '.' {
			continue
		}
		resolved := normalizePath(filepath.Join(importerDir, spec))
		deps = append(deps, resolveJSImportPath(worktreeDir, resolved)...)
	}
	return uniqueNonEmpty(deps)
}

// resolveJSImportPath tries common extensions for a JS/TS import path and
// returns the ones that exist as files. An import of "./types" may resolve
// to "types.ts", "types.tsx", "types.js", "types/index.ts", etc.
func resolveJSImportPath(worktreeDir, basePath string) []string {
	var results []string
	exts := []string{"", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".d.ts"}
	for _, ext := range exts {
		candidate := basePath + ext
		if fileExists(filepath.Join(worktreeDir, candidate)) {
			results = append(results, candidate)
		}
	}
	indexBase := filepath.Join(basePath, "index")
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
		candidate := indexBase + ext
		if fileExists(filepath.Join(worktreeDir, candidate)) {
			results = append(results, candidate)
		}
	}
	return results
}

// parsePythonImports resolves relative Python imports (., .., package.module)
// to repo-relative file paths. Absolute imports are resolved against the
// worktree root using package/directory structure.
func parsePythonImports(worktreeDir, importingFile string, data []byte) []string {
	var deps []string
	importerDir := filepath.Dir(importingFile)
	for _, m := range pyFromRe.FindAllSubmatch(data, -1) {
		mod := strings.TrimSpace(string(m[1]))
		if mod == "" {
			continue
		}
		if strings.HasPrefix(mod, ".") {
			// Relative import: dots indicate package levels up from importer
			dots := 0
			for dots < len(mod) && mod[dots] == '.' {
				dots++
			}
			rest := mod[dots:]
			base := importerDir
			for i := 1; i < dots; i++ {
				base = filepath.Dir(base)
			}
			if rest != "" {
				candidate := normalizePath(filepath.Join(base, strings.ReplaceAll(rest, ".", "/")))
				deps = append(deps, resolvePythonPath(worktreeDir, candidate)...)
			}
			deps = append(deps, resolvePythonDir(worktreeDir, base)...)
		} else {
			candidate := strings.ReplaceAll(mod, ".", "/")
			deps = append(deps, resolvePythonPath(worktreeDir, candidate)...)
		}
	}
	return uniqueNonEmpty(deps)
}

func resolvePythonPath(worktreeDir, basePath string) []string {
	var results []string
	for _, ext := range []string{".py"} {
		candidate := basePath + ext
		if fileExists(filepath.Join(worktreeDir, candidate)) {
			results = append(results, candidate)
		}
	}
	if fileExists(filepath.Join(worktreeDir, basePath, "__init__.py")) {
		deps := resolvePythonDir(worktreeDir, basePath)
		results = append(results, deps...)
	}
	return results
}

func resolvePythonDir(worktreeDir, dirPath string) []string {
	entries, err := os.ReadDir(filepath.Join(worktreeDir, dirPath))
	if err != nil {
		return nil
	}
	var results []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".py") {
			continue
		}
		results = append(results, normalizePath(filepath.Join(dirPath, name)))
	}
	return results
}

func normalizePath(p string) string {
	return filepath.ToSlash(filepath.Clean(p))
}

func uniqueNonEmpty(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range items {
		if s == "" || s == "." {
			continue
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

var (
	jsImportRe  = regexp.MustCompile(`(?:import\s+[^;]*?\s+from\s+|export\s+[^;]*?\s+from\s+|import\s+)\(?["'](\.[^"']+)["']\)?`)
	jsRequireRe = regexp.MustCompile(`require\s*\(\s*["'](\.[^"']+)["']\s*\)`)
	pyFromRe    = regexp.MustCompile(`(?m)^\s*from\s+(\S+)\s+import`)
)
