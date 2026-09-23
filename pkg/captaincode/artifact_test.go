package captaincode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func artifactFixtureRepo(t *testing.T) (string, string) {
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
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("beta\n"), 0o644); err != nil {
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

func TestCaptureManifestNoChanges(t *testing.T) {
	repo, rev := artifactFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()
	m, err := CaptureManifest(context.Background(), wt.Dir, t.TempDir(), rev, "t1", "s1", "a1", "grok")
	if err != nil {
		t.Fatalf("CaptureManifest: %v", err)
	}
	if m.HasChanges() {
		t.Fatalf("expected no changes, got %v", m.ChangedFiles)
	}
	if m.DiffDigest != "" {
		t.Fatalf("expected empty digest for no changes, got %s", m.DiffDigest)
	}
	if m.Leg != "grok" {
		t.Fatalf("expected leg grok, got %s", m.Leg)
	}
}

func TestCaptureManifestWithChanges(t *testing.T) {
	repo, rev := artifactFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()
	if err := os.WriteFile(filepath.Join(wt.Dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffDir := t.TempDir()
	m, err := CaptureManifest(context.Background(), wt.Dir, diffDir, rev, "t1", "s1", "a1", "codex")
	if err != nil {
		t.Fatalf("CaptureManifest: %v", err)
	}
	if !m.HasChanges() {
		t.Fatal("expected changes")
	}
	sort.Strings(m.ChangedFiles)
	if len(m.ChangedFiles) != 2 {
		t.Fatalf("expected 2 changed files, got %v", m.ChangedFiles)
	}
	if m.DiffDigest == "" {
		t.Fatal("expected non-empty diff digest")
	}
	if m.DiffPath == "" {
		t.Fatal("expected diff path")
	}
	if _, err := os.Stat(m.DiffPath); err != nil {
		t.Fatalf("diff file not written: %v", err)
	}
}

func TestCaptureManifestUntrackedOnly(t *testing.T) {
	repo, rev := artifactFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()
	if err := os.WriteFile(filepath.Join(wt.Dir, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := CaptureManifest(context.Background(), wt.Dir, "", rev, "t1", "s1", "a1", "claude")
	if err != nil {
		t.Fatalf("CaptureManifest: %v", err)
	}
	if !m.HasChanges() {
		t.Fatal("expected changes from untracked file")
	}
	if len(m.ChangedFiles) != 1 || m.ChangedFiles[0] != "untracked.txt" {
		t.Fatalf("expected [untracked.txt], got %v", m.ChangedFiles)
	}
}

func TestRecordCheck(t *testing.T) {
	m := PatchManifest{Version: ArtifactVersion, Leg: "grok"}
	m.RecordCheck([]string{"go", "test", "./..."}, 0, true, "ok\n")
	if m.Check == nil {
		t.Fatal("expected check evidence")
	}
	if !m.Check.Passed {
		t.Fatal("expected passed")
	}
	if m.Check.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", m.Check.ExitCode)
	}
}

func TestBuildIntegrationCandidateClean(t *testing.T) {
	m1 := PatchManifest{Leg: "grok", ChangedFiles: []string{"a.txt"}}
	m2 := PatchManifest{Leg: "codex", ChangedFiles: []string{"b.txt"}}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{m1, m2})
	if ic.Status != IntegrationClean {
		t.Fatalf("expected clean, got %s", ic.Status)
	}
	if ic.HasConflicts() {
		t.Fatal("expected no conflicts")
	}
	files := ic.ChangedFiles()
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %v", files)
	}
}

func TestBuildIntegrationCandidateConflicted(t *testing.T) {
	m1 := PatchManifest{Leg: "grok", ChangedFiles: []string{"a.txt", "b.txt"}}
	m2 := PatchManifest{Leg: "codex", ChangedFiles: []string{"b.txt", "c.txt"}}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{m1, m2})
	if ic.Status != IntegrationConflicted {
		t.Fatalf("expected conflicted, got %s", ic.Status)
	}
	if !ic.HasConflicts() {
		t.Fatal("expected conflicts")
	}
	if len(ic.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(ic.Conflicts))
	}
	if ic.Conflicts[0].File != "b.txt" {
		t.Fatalf("expected conflict on b.txt, got %s", ic.Conflicts[0].File)
	}
	sort.Strings(ic.Conflicts[0].Workers)
	if len(ic.Conflicts[0].Workers) != 2 {
		t.Fatalf("expected 2 workers, got %v", ic.Conflicts[0].Workers)
	}
}

func TestBuildIntegrationCandidateEmpty(t *testing.T) {
	m1 := PatchManifest{Leg: "grok", ChangedFiles: nil}
	m2 := PatchManifest{Leg: "codex", ChangedFiles: nil}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{m1, m2})
	if ic.Status != IntegrationEmpty {
		t.Fatalf("expected empty, got %s", ic.Status)
	}
}

func TestBuildIntegrationCandidateOneEmptyOneChanged(t *testing.T) {
	m1 := PatchManifest{Leg: "grok", ChangedFiles: nil}
	m2 := PatchManifest{Leg: "codex", ChangedFiles: []string{"a.txt"}}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{m1, m2})
	if ic.Status != IntegrationClean {
		t.Fatalf("expected clean (one worker changed files, no overlap), got %s", ic.Status)
	}
	if len(ic.ChangedFiles()) != 1 {
		t.Fatalf("expected 1 changed file, got %v", ic.ChangedFiles())
	}
}

func TestIntegrationCandidateConflictFiles(t *testing.T) {
	m1 := PatchManifest{Leg: "grok", ChangedFiles: []string{"a.txt", "shared.go"}}
	m2 := PatchManifest{Leg: "codex", ChangedFiles: []string{"shared.go", "b.txt"}}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{m1, m2})
	cf := ic.ConflictFiles()
	if len(cf) != 1 || cf[0] != "shared.go" {
		t.Fatalf("expected [shared.go], got %v", cf)
	}
}

func TestIntegrationCandidateSummary(t *testing.T) {
	clean := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{
		{Leg: "grok", ChangedFiles: []string{"a.txt"}},
		{Leg: "codex", ChangedFiles: []string{"b.txt"}},
	})
	if !strings.Contains(clean.Summary(), "no conflicts") {
		t.Fatalf("clean summary: %s", clean.Summary())
	}
	conflicted := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{
		{Leg: "grok", ChangedFiles: []string{"a.txt"}},
		{Leg: "codex", ChangedFiles: []string{"a.txt"}},
	})
	if !strings.Contains(conflicted.Summary(), "conflict") {
		t.Fatalf("conflicted summary: %s", conflicted.Summary())
	}
	empty := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{
		{Leg: "grok"},
		{Leg: "codex"},
	})
	if !strings.Contains(empty.Summary(), "no file changes") {
		t.Fatalf("empty summary: %s", empty.Summary())
	}
}

func TestCaptureManifestNonGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := CaptureManifest(context.Background(), dir, "", strings.Repeat("0", 40), "t1", "s1", "a1", "grok")
	if err == nil {
		t.Fatal("expected error for non-git repo")
	}
}

func TestBuildIntegrationCandidateThreeWayConflict(t *testing.T) {
	m1 := PatchManifest{Leg: "grok", ChangedFiles: []string{"queue.ts"}}
	m2 := PatchManifest{Leg: "codex", ChangedFiles: []string{"queue.ts"}}
	m3 := PatchManifest{Leg: "claude", ChangedFiles: []string{"queue.ts"}}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{m1, m2, m3})
	if !ic.HasConflicts() {
		t.Fatal("expected conflicts")
	}
	if len(ic.Conflicts[0].Workers) != 3 {
		t.Fatalf("expected 3 workers, got %d", len(ic.Conflicts[0].Workers))
	}
}

func TestApplyIntegrationCandidateClean(t *testing.T) {
	repo, rev := artifactFixtureRepo(t)
	// Worker A modifies a.txt in its own worktree
	wtA, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree A: %v", err)
	}
	defer wtA.Close()
	if err := os.WriteFile(filepath.Join(wtA.Dir, "a.txt"), []byte("changed by A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Worker B creates a new file in its own worktree
	wtB, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree B: %v", err)
	}
	defer wtB.Close()
	if err := os.WriteFile(filepath.Join(wtB.Dir, "new.txt"), []byte("created by B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffDir := t.TempDir()
	mA, err := CaptureManifest(context.Background(), wtA.Dir, diffDir, rev, "t1", "s1", "a1", "grok")
	if err != nil {
		t.Fatalf("CaptureManifest A: %v", err)
	}
	mB, err := CaptureManifest(context.Background(), wtB.Dir, diffDir, rev, "t1", "s1", "a2", "codex")
	if err != nil {
		t.Fatalf("CaptureManifest B: %v", err)
	}
	ic := BuildIntegrationCandidate("t1", "s1", rev, []PatchManifest{mA, mB})
	if ic.Status != IntegrationClean {
		t.Fatalf("expected clean, got %s", ic.Status)
	}
	// Apply to a fresh clone of the repo
	target, _ := artifactFixtureRepo(t)
	if err := ApplyIntegrationCandidate(context.Background(), target, ic); err != nil {
		t.Fatalf("ApplyIntegrationCandidate: %v", err)
	}
	gotA, err := os.ReadFile(filepath.Join(target, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	if string(gotA) != "changed by A\n" {
		t.Fatalf("a.txt = %q, want %q", gotA, "changed by A\n")
	}
	gotB, err := os.ReadFile(filepath.Join(target, "new.txt"))
	if err != nil {
		t.Fatalf("read new.txt: %v", err)
	}
	if string(gotB) != "created by B\n" {
		t.Fatalf("new.txt = %q, want %q", gotB, "created by B\n")
	}
}

func TestApplyIntegrationCandidateRejectsConflicted(t *testing.T) {
	m1 := PatchManifest{Leg: "grok", ChangedFiles: []string{"a.txt"}, DiffPath: "/dev/null"}
	m2 := PatchManifest{Leg: "codex", ChangedFiles: []string{"a.txt"}, DiffPath: "/dev/null"}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{m1, m2})
	if ic.Status != IntegrationConflicted {
		t.Fatalf("expected conflicted, got %s", ic.Status)
	}
	err := ApplyIntegrationCandidate(context.Background(), t.TempDir(), ic)
	if err == nil {
		t.Fatal("expected error applying conflicted candidate")
	}
}

func TestApplyIntegrationCandidateRejectsEmpty(t *testing.T) {
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{
		{Leg: "grok"},
		{Leg: "codex"},
	})
	if ic.Status != IntegrationEmpty {
		t.Fatalf("expected empty, got %s", ic.Status)
	}
	err := ApplyIntegrationCandidate(context.Background(), t.TempDir(), ic)
	if err == nil {
		t.Fatal("expected error applying empty candidate")
	}
}

func TestDiffWorktreeIncludesUntracked(t *testing.T) {
	repo, rev := artifactFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()
	if err := os.WriteFile(filepath.Join(wt.Dir, "untracked.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := CaptureManifest(context.Background(), wt.Dir, t.TempDir(), rev, "t1", "s1", "a1", "grok")
	if err != nil {
		t.Fatalf("CaptureManifest: %v", err)
	}
	if !m.HasChanges() {
		t.Fatal("expected changes")
	}
	// The diff file should include the untracked file content
	diff, err := os.ReadFile(m.DiffPath)
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	if !strings.Contains(string(diff), "untracked.go") {
		t.Fatalf("diff does not include untracked file: %s", diff)
	}
}

// outsideEvidenceRun clears the marker a parent test-evidence run leaves in
// the environment. Captain's own verify step runs this suite under that
// marker, so a capture test that inherits it gets nil back and fails.
func outsideEvidenceRun(t *testing.T) {
	t.Helper()
	t.Setenv(testEvidenceDepthEnv, "")
}

// TestANestedCaptureReturnsNoSignal verifies the recursion guard: inside a
// test-evidence run, a capture of a project that has tests returns nil
// instead of forking the suite again.
func TestANestedCaptureReturnsNoSignal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testEvidenceDepthEnv, "1")
	te, err := CaptureTestEvidence(context.Background(), dir)
	if err != nil {
		t.Fatalf("CaptureTestEvidence: %v", err)
	}
	if te != nil {
		t.Fatalf("expected no evidence inside a test-evidence run, got %+v", te)
	}
}

// TestCaptureTestEvidenceGoModule verifies that the project's own test suite
// is run in the worker's worktree and the result is captured as evidence
// (M3.2: test evidence beyond the gate command).
func TestCaptureTestEvidenceGoModule(t *testing.T) {
	outsideEvidenceRun(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(`package test

import "testing"

func TestPass(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("math broken")
	}
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	te, err := CaptureTestEvidence(context.Background(), dir)
	if err != nil {
		t.Fatalf("CaptureTestEvidence: %v", err)
	}
	if te == nil {
		t.Fatal("expected test evidence for go module project")
	}
	if !te.Passed {
		t.Fatalf("expected tests to pass, got exit %d: %s", te.ExitCode, te.Output)
	}
	if len(te.Command) == 0 || te.Command[1] != "-c" {
		t.Fatalf("unexpected command: %v", te.Command)
	}
}

// TestCaptureTestEvidenceNoProject verifies that a directory with no
// recognizable project manifest returns nil (no test suite to run, not a
// failure).
func TestCaptureTestEvidenceNoProject(t *testing.T) {
	outsideEvidenceRun(t)
	dir := t.TempDir()
	te, err := CaptureTestEvidence(context.Background(), dir)
	if err != nil {
		t.Fatalf("CaptureTestEvidence: %v", err)
	}
	if te != nil {
		t.Fatalf("expected nil for no project, got %+v", te)
	}
}

// TestCaptureTestEvidenceFailingTests verifies that a failing test suite
// is captured as evidence with Passed=false and a non-zero exit code.
func TestCaptureTestEvidenceFailingTests(t *testing.T) {
	outsideEvidenceRun(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(`package test

import "testing"

func TestFail(t *testing.T) {
	t.Fatal("intentional failure")
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	te, err := CaptureTestEvidence(context.Background(), dir)
	if err != nil {
		t.Fatalf("CaptureTestEvidence: %v", err)
	}
	if te == nil {
		t.Fatal("expected test evidence")
	}
	if te.Passed {
		t.Fatal("expected tests to fail")
	}
	if te.ExitCode == 0 {
		t.Fatal("expected non-zero exit code")
	}
}

// TestIntegrationCandidateTestCounts verifies that the test evidence from
// multiple workers is aggregated correctly in the integration candidate.
func TestIntegrationCandidateTestCounts(t *testing.T) {
	manifests := []PatchManifest{
		{Leg: "grok", ChangedFiles: []string{"a.go"}, TestEvidence: &CheckEvidence{Passed: true, ExitCode: 0}},
		{Leg: "codex", ChangedFiles: []string{"b.go"}, TestEvidence: &CheckEvidence{Passed: false, ExitCode: 1}},
		{Leg: "claude", ChangedFiles: []string{"c.go"}, TestEvidence: nil},
	}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", manifests)
	passed, total := ic.TestCounts()
	if total != 2 {
		t.Fatalf("expected 2 tests run, got %d", total)
	}
	if passed != 1 {
		t.Fatalf("expected 1 passed, got %d", passed)
	}
}

// semanticFixtureRepo creates a Go repo with two packages where handler.go
// imports types.go, so semantic conflict detection can find cross-file
// dependencies. Returns the repo dir and the seed revision.
func semanticFixtureRepo(t *testing.T) (string, string) {
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
	if err := os.MkdirAll(filepath.Join(dir, "types"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "handler"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.21\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "types", "types.go"), []byte("package types\n\ntype Config struct {\n\tName string\n}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "handler", "handler.go"), []byte("package handler\n\nimport \"example.com/proj/types\"\n\nfunc Process(c types.Config) {}\n"), 0o644)
	run("git", "add", ".")
	run("git", "commit", "--quiet", "-m", "seed")
	rev, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(rev))
}

func TestSemanticConflictGoImport(t *testing.T) {
	repo, rev := semanticFixtureRepo(t)
	// Worker A changes types/types.go
	wtA, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree A: %v", err)
	}
	defer wtA.Close()
	os.WriteFile(filepath.Join(wtA.Dir, "types", "types.go"), []byte("package types\n\ntype Config struct {\n\tName string\n\tValue int\n}\n"), 0o644)
	mA, err := CaptureManifest(context.Background(), wtA.Dir, t.TempDir(), rev, "t1", "s1", "a1", "grok")
	if err != nil {
		t.Fatalf("CaptureManifest A: %v", err)
	}
	// Worker B changes handler/handler.go (which imports types)
	wtB, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree B: %v", err)
	}
	defer wtB.Close()
	os.WriteFile(filepath.Join(wtB.Dir, "handler", "handler.go"), []byte("package handler\n\nimport \"example.com/proj/types\"\n\nfunc Process(c types.Config) int { return c.Value }\n"), 0o644)
	mB, err := CaptureManifest(context.Background(), wtB.Dir, t.TempDir(), rev, "t1", "s1", "a2", "codex")
	if err != nil {
		t.Fatalf("CaptureManifest B: %v", err)
	}
	ic := BuildIntegrationCandidate("t1", "s1", rev, []PatchManifest{mA, mB})
	if ic.Status != IntegrationClean {
		t.Fatalf("expected clean (no file overlap), got %s", ic.Status)
	}
	if !ic.HasSemanticConflicts() {
		t.Fatalf("expected semantic conflicts, got none. Summary: %s", ic.Summary())
	}
	if len(ic.SemanticConflicts) < 1 {
		t.Fatalf("expected at least 1 semantic conflict, got %d", len(ic.SemanticConflicts))
	}
	sc := ic.SemanticConflicts[0]
	if sc.Reason != "go import" {
		t.Fatalf("expected reason 'go import', got %s", sc.Reason)
	}
	if !strings.HasSuffix(sc.DependsOn, "types.go") {
		t.Fatalf("expected dependsOn to be types.go, got %s", sc.DependsOn)
	}
	if !strings.HasSuffix(sc.File, "handler.go") {
		t.Fatalf("expected file to be handler.go, got %s", sc.File)
	}
}

func TestSemanticConflictNoneForUnrelatedFiles(t *testing.T) {
	repo, rev := semanticFixtureRepo(t)
	// Worker A changes go.mod (not imported by anything)
	wtA, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree A: %v", err)
	}
	defer wtA.Close()
	os.WriteFile(filepath.Join(wtA.Dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.22\n"), 0o644)
	mA, err := CaptureManifest(context.Background(), wtA.Dir, t.TempDir(), rev, "t1", "s1", "a1", "grok")
	if err != nil {
		t.Fatalf("CaptureManifest A: %v", err)
	}
	// Worker B changes types/types.go
	wtB, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree B: %v", err)
	}
	defer wtB.Close()
	os.WriteFile(filepath.Join(wtB.Dir, "types", "types.go"), []byte("package types\n\ntype Config struct {\n\tName string\n\tValue int\n}\n"), 0o644)
	mB, err := CaptureManifest(context.Background(), wtB.Dir, t.TempDir(), rev, "t1", "s1", "a2", "codex")
	if err != nil {
		t.Fatalf("CaptureManifest B: %v", err)
	}
	ic := BuildIntegrationCandidate("t1", "s1", rev, []PatchManifest{mA, mB})
	if ic.HasSemanticConflicts() {
		t.Fatalf("expected no semantic conflicts for unrelated files, got %v", ic.SemanticConflicts)
	}
}

func TestSemanticConflictJSTSImport(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", "utils"), 0o755)
	os.MkdirAll(filepath.Join(dir, "src", "api"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "utils", "types.ts"), []byte("export interface Config { name: string }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "api", "handler.ts"), []byte("import { Config } from '../utils/types'\nexport function process(c: Config) {}\n"), 0o644)
	mA := PatchManifest{
		Leg:          "grok",
		ChangedFiles: []string{"src/utils/types.ts"},
		WorktreeDir:  dir,
	}
	mB := PatchManifest{
		Leg:          "codex",
		ChangedFiles: []string{"src/api/handler.ts"},
		WorktreeDir:  dir,
	}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{mA, mB})
	if !ic.HasSemanticConflicts() {
		t.Fatalf("expected semantic conflicts for JS/TS cross-file import, got none")
	}
	sc := ic.SemanticConflicts[0]
	if sc.Reason != "js/ts import" {
		t.Fatalf("expected reason 'js/ts import', got %s", sc.Reason)
	}
}

func TestSemanticConflictSkipsSyntheticManifests(t *testing.T) {
	// Manifests without WorktreeDir should not produce semantic conflicts
	// (can't read the files to parse imports)
	m1 := PatchManifest{Leg: "grok", ChangedFiles: []string{"a.go"}}
	m2 := PatchManifest{Leg: "codex", ChangedFiles: []string{"b.go"}}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{m1, m2})
	if ic.HasSemanticConflicts() {
		t.Fatalf("expected no semantic conflicts without worktree dirs, got %v", ic.SemanticConflicts)
	}
}

func TestSemanticConflictSummaryMentionsSemantic(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", "utils"), 0o755)
	os.MkdirAll(filepath.Join(dir, "src", "api"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "utils", "types.ts"), []byte("export interface Config { name: string }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "api", "handler.ts"), []byte("import { Config } from '../utils/types'\nexport function process(c: Config) {}\n"), 0o644)
	mA := PatchManifest{Leg: "grok", ChangedFiles: []string{"src/utils/types.ts"}, WorktreeDir: dir}
	mB := PatchManifest{Leg: "codex", ChangedFiles: []string{"src/api/handler.ts"}, WorktreeDir: dir}
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{mA, mB})
	if !strings.Contains(ic.Summary(), "semantic") {
		t.Fatalf("expected summary to mention semantic, got: %s", ic.Summary())
	}
}

func TestSemanticConflictDoesNotBlockApply(t *testing.T) {
	// A clean candidate with semantic conflicts should still be applicable:
	// semantic conflicts are advisory, not blocking
	repo, rev := semanticFixtureRepo(t)
	wtA, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree A: %v", err)
	}
	defer wtA.Close()
	os.WriteFile(filepath.Join(wtA.Dir, "types", "types.go"), []byte("package types\n\ntype Config struct {\n\tName string\n\tValue int\n}\n"), 0o644)
	diffDir := t.TempDir()
	mA, err := CaptureManifest(context.Background(), wtA.Dir, diffDir, rev, "t1", "s1", "a1", "grok")
	if err != nil {
		t.Fatalf("CaptureManifest A: %v", err)
	}
	wtB, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("NewWorktree B: %v", err)
	}
	defer wtB.Close()
	os.WriteFile(filepath.Join(wtB.Dir, "handler", "handler.go"), []byte("package handler\n\nimport \"example.com/proj/types\"\n\nfunc Process(c types.Config) int { return c.Value }\n"), 0o644)
	mB, err := CaptureManifest(context.Background(), wtB.Dir, diffDir, rev, "t1", "s1", "a2", "codex")
	if err != nil {
		t.Fatalf("CaptureManifest B: %v", err)
	}
	ic := BuildIntegrationCandidate("t1", "s1", rev, []PatchManifest{mA, mB})
	if ic.Status != IntegrationClean {
		t.Fatalf("expected clean status despite semantic conflicts, got %s", ic.Status)
	}
	if !ic.HasSemanticConflicts() {
		t.Fatal("expected semantic conflicts")
	}
	target, _ := semanticFixtureRepo(t)
	if err := ApplyIntegrationCandidate(context.Background(), target, ic); err != nil {
		t.Fatalf("expected apply to succeed (semantic conflicts are advisory), got: %v", err)
	}
}

func TestCaptureSoloArtifactNoChanges(t *testing.T) {
	repo, _ := artifactFixtureRepo(t)
	files, digest, diffPath, err := CaptureSoloArtifact(context.Background(), repo, t.TempDir(), "glm")
	if err != nil {
		t.Fatalf("CaptureSoloArtifact: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no changed files, got %v", files)
	}
	if digest != "" {
		t.Fatalf("expected empty digest, got %s", digest)
	}
	if diffPath != "" {
		t.Fatalf("expected empty diff path, got %s", diffPath)
	}
}

func TestCaptureSoloArtifactWithChanges(t *testing.T) {
	repo, _ := artifactFixtureRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffDir := t.TempDir()
	files, digest, diffPath, err := CaptureSoloArtifact(context.Background(), repo, diffDir, "claude")
	if err != nil {
		t.Fatalf("CaptureSoloArtifact: %v", err)
	}
	sort.Strings(files)
	if len(files) != 2 {
		t.Fatalf("expected 2 changed files, got %v", files)
	}
	if digest == "" {
		t.Fatal("expected non-empty diff digest")
	}
	if diffPath == "" {
		t.Fatal("expected diff path")
	}
	if _, err := os.Stat(diffPath); err != nil {
		t.Fatalf("diff file not written: %v", err)
	}
}

func TestCaptureSoloArtifactNoDiffDir(t *testing.T) {
	repo, _ := artifactFixtureRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, digest, diffPath, err := CaptureSoloArtifact(context.Background(), repo, "", "codex")
	if err != nil {
		t.Fatalf("CaptureSoloArtifact: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 changed file, got %v", files)
	}
	if digest == "" {
		t.Fatal("expected non-empty digest")
	}
	if diffPath != "" {
		t.Fatalf("expected no diff path when diffDir is empty, got %s", diffPath)
	}
}

func TestRecordSoloArtifact(t *testing.T) {
	l := &Ledger{}
	as := AttemptState{TaskID: "t1", AttemptID: "a1", Leg: LegGLM}
	l.AttemptStates = append(l.AttemptStates, as)
	l.RecordSoloArtifact("a1", []string{"foo.go", "bar.go"}, "abc123", "/tmp/diff.patch")
	stored := l.AttemptStateFor("a1")
	if stored == nil {
		t.Fatal("expected attempt state")
	}
	if len(stored.ChangedFiles) != 2 {
		t.Fatalf("expected 2 changed files, got %v", stored.ChangedFiles)
	}
	if stored.DiffDigest != "abc123" {
		t.Fatalf("expected digest abc123, got %s", stored.DiffDigest)
	}
	if stored.DiffPath != "/tmp/diff.patch" {
		t.Fatalf("expected diff path /tmp/diff.patch, got %s", stored.DiffPath)
	}
}
