package captaincode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenShellSequenceResumeLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_RESUME_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_RESUME_LIVE=1 with a prepared VM runtime and provider key")
	}
	prepared := os.Getenv("CAPTAIN_OPENSHELL_PREPARED")
	require.NotEmpty(t, prepared)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "cc-seq-resume-")
	require.NoError(t, err)
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	t.Logf("isolated evidence: %s", root)
	repo, dir := filepath.Join(root, "repo"), filepath.Join(root, "run")
	for _, path := range []string{repo, dir} {
		require.NoError(t, os.Mkdir(path, 0o700))
	}
	pilot, err := filepath.Abs("../../examples/openshell-pilot")
	require.NoError(t, err)
	for _, name := range []string{"roman.py", "test_roman.py"} {
		data, err := os.ReadFile(filepath.Join(pilot, "team", "repo", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), data, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("baseline\n"), 0o600))
	git := func(args ...string) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=captain-test", "-c", "user.email=test@example.com",
		"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "Public sandbox recovery fixture")
	repo, revision, err := ResolveOpenShellRepo(ctx, repo, "HEAD")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("staged\n"), 0o600))
	git("add", "operator.txt")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("unstaged\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("local\n"), 0o600))
	before := map[string][]byte{}
	for _, name := range []string{"roman.py", "test_roman.py", "operator.txt", "untracked.txt", ".git/index"} {
		before[name], err = os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
	}
	edit := OpenShellTask{ID: "s1-edit", Profile: "cerebras", Prompt: "Fix roman.py to implement standard subtractive Roman numerals for integers 1 through 3999 and raise ValueError outside that range. Read and run test_roman.py. Change only roman.py.",
		Allowed: []string{"roman.py"}, Protected: []string{"test_roman.py", "operator.txt"},
		Verify: []string{"python3", "-m", "unittest", "-v"}, Baseline: "fail", DeadlineSeconds: 180, VerifySeconds: 30}
	review := edit
	review.ID, review.Mode, review.Baseline = "s2-review", "review", "pass"
	review.Allowed = nil
	review.Prompt = "Review roman.py against test_roman.py. Run the tests and report findings. Do not modify any repository file."
	plan := openShellSequencePlan{Pilot: pilot, Prepared: prepared, StateRoot: "/tmp", RunDir: dir,
		Repo: repo, Revision: revision, Runtime: "vm", Director: "none", Concurrency: 1,
		Teams: []OpenShellTeam{{Schema: 1, ID: "s1", Tasks: []OpenShellTask{edit}},
			{Schema: 1, ID: "s2", Tasks: []OpenShellTask{review}}}}
	data, err := json.Marshal(plan)
	require.NoError(t, err)
	input := filepath.Join(root, "input.json")
	require.NoError(t, os.WriteFile(input, data, 0o600))
	self, err := os.Executable()
	require.NoError(t, err)
	log, err := os.OpenFile(filepath.Join(root, "initial-controller.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer log.Close()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestOpenShellSequenceCrashHelper$")
	cmd.Env = append(os.Environ(), "CAPTAIN_OPENSHELL_SEQUENCE_HELPER="+input, "CAPTAIN_OPENSHELL_SEQUENCE_CRASH_EXIT=1")
	cmd.Stdout, cmd.Stderr = log, log
	started := time.Now()
	err = cmd.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 87, exit.ExitCode(), "controller must exit at a verified stage boundary; inspect initial-controller.log")
	firstSeconds := time.Since(started).Seconds()
	firstStage, err := os.ReadFile(filepath.Join(dir, "stage-1", "run.json"))
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(dir, "stage-2"))
	started = time.Now()
	result, err := ResumeOpenShellSequence(ctx, dir, t.Logf)
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	resumeSeconds := time.Since(started).Seconds()
	after, err := os.ReadFile(filepath.Join(dir, "stage-1", "run.json"))
	require.NoError(t, err)
	require.Equal(t, firstStage, after)
	var run OpenShellRun
	runBytes, err := decodeOpenShellSequence(filepath.Join(dir, "run.json"), &run)
	require.NoError(t, err)
	require.Len(t, run.Stages, 2)
	require.Len(t, run.Resumptions, 1)
	require.Equal(t, run.Stages[0].Tree, run.Stages[1].Tree)
	require.Equal(t, run.Stages[0].NextRevision, run.Stages[1].Revision)
	for _, stage := range run.Stages {
		var record OpenShellRun
		_, err := decodeOpenShellSequence(stage.RunRecord, &record)
		require.NoError(t, err)
		require.NoError(t, checkOpenShellReport(record.Tasks[0].Report, record.Tasks[0].Task, openShellTaskChecks))
		require.NoError(t, checkOpenShellReport(record.Integrated.Report, "integrated", openShellVerifyChecks))
	}
	patch, err := os.ReadFile(result.Export.Manifest.DiffPath)
	require.NoError(t, err)
	tree, err := (&OpenShellRunner{Repo: repo, Revision: revision}).treeWith(ctx, patch)
	require.NoError(t, err)
	require.Equal(t, run.Integrated.Tree, tree)
	for name, data := range before {
		after, err := os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
		require.Equal(t, data, after, name)
	}
	second, err := ResumeOpenShellSequence(ctx, dir, nil)
	require.NoError(t, err)
	require.NotNil(t, second.Export)
	assert.Equal(t, result.Export.Manifest.DiffDigest, second.Export.Manifest.DiffDigest)
	tokens, cost, complete := run.Spend()
	report := map[string]any{
		"schema": 1, "verdict": "pass", "fixture": "Public Roman numeral edit followed by a no-change review",
		"runtime": "vm", "profile": "cerebras", "model": "openai/gpt-oss-120b",
		"controller_exit_code": 87, "initial_stage_seconds": firstSeconds, "resume_seconds": resumeSeconds,
		"stage_count": len(run.Stages), "worker_checks_per_stage": len(openShellTaskChecks),
		"integrated_checks_per_stage":  len(openShellVerifyChecks),
		"first_stage_reused_unchanged": true, "snapshot_lineage_matches": true,
		"cumulative_patch_reproduces_verified_tree": true, "completed_resume_idempotent": true,
		"host_files_and_index_unchanged": true, "provenance": run.Provenance,
		"run_record_sha256": fmt.Sprintf("%x", sha256.Sum256(runBytes)), "patch_sha256": result.Export.Manifest.DiffDigest,
		"spend":  map[string]any{"tokens": tokens, "cost_usd": cost, "complete": complete},
		"limits": "One public fixture on one host. Recovery at verified stage boundaries only; no mid-worker or automatic brain recovery.",
	}
	data, err = json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "qualification.json"), append(data, '\n'), 0o600))
	t.Logf("qualification: %s", filepath.Join(root, "qualification.json"))
}
