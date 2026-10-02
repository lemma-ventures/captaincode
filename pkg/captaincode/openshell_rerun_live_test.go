package captaincode

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stopAfterFirstResponse cancels a stage once a worker dispatched after since
// has had a model response through Shield, or after a fallback delay.
func stopAfterFirstResponse(ctx context.Context, stop context.CancelFunc, stateRoot string, since time.Time, fallback time.Duration) {
	deadline := time.Now().Add(fallback)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		audits, _ := filepath.Glob(filepath.Join(stateRoot, "cc-os-*", "shield-audit.jsonl"))
		for _, audit := range audits {
			info, err := os.Stat(audit)
			if err != nil || info.ModTime().Before(since) {
				continue
			}
			f, err := os.Open(audit)
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			for scanner.Scan() {
				var row struct {
					Phase string `json:"phase"`
				}
				if json.Unmarshal(scanner.Bytes(), &row) == nil && row.Phase == "response" {
					f.Close()
					stop()
					return
				}
			}
			f.Close()
		}
		time.Sleep(250 * time.Millisecond)
	}
	stop()
}

func TestOpenShellSequenceRerunLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_RERUN_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_RERUN_LIVE=1 with a prepared VM runtime and provider key")
	}
	prepared := os.Getenv("CAPTAIN_OPENSHELL_PREPARED")
	require.NotEmpty(t, prepared)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "cc-seq-rerun-")
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
	git := func(args ...string) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=romainPellerin", "-c", "user.email=rom@lemma.ventures",
		"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "Public sandbox rerun fixture")
	repo, revision, err := ResolveOpenShellRepo(ctx, repo, "HEAD")
	require.NoError(t, err)
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	require.NoError(t, err)

	edit := OpenShellTask{ID: "s1-edit", Profile: "cerebras", Prompt: "Fix roman.py to implement standard subtractive Roman numerals for integers 1 through 3999 and raise ValueError outside that range. Read and run test_roman.py. Change only roman.py.",
		Allowed: []string{"roman.py"}, Protected: []string{"test_roman.py"},
		Verify: []string{"python3", "-m", "unittest", "-v"}, Baseline: "fail", DeadlineSeconds: 180, VerifySeconds: 30}
	review := edit
	review.ID, review.Mode, review.Baseline = "s2-review", "review", "pass"
	review.Allowed = nil
	review.Prompt = "Review roman.py against test_roman.py. Run the tests and report findings. Do not modify any repository file."
	teams := []OpenShellTeam{{Schema: 1, ID: "s1", Tasks: []OpenShellTask{edit}}, {Schema: 1, ID: "s2", Tasks: []OpenShellTask{review}}}
	runner := &OpenShellRunner{Pilot: pilot, Prepared: prepared, StateRoot: "/tmp", RunDir: dir,
		Repo: repo, Revision: revision, Runtime: "vm", Concurrency: 1, DirectorName: "none"}
	stopCtx, stop := context.WithCancel(ctx)
	defer stop()
	var dispatched time.Time
	runner.Log = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		if strings.HasPrefix(line, "stage 2/2: snapshot ") {
			dispatched = time.Now()
			go stopAfterFirstResponse(stopCtx, stop, runner.StateRoot, dispatched, 45*time.Second)
		}
	}
	started := time.Now()
	_, err = runOpenShellSequence(stopCtx, runner, teams, nil)
	require.ErrorIs(t, err, ErrInterrupted)
	require.False(t, dispatched.IsZero(), "the stop must land in stage 2")
	stoppedSeconds := time.Since(started).Seconds()
	var stopped OpenShellRun
	_, err = decodeOpenShellSequence(filepath.Join(dir, "run.json"), &stopped)
	require.NoError(t, err)
	require.Len(t, stopped.Stages, 2)
	require.Equal(t, "interrupted", stopped.Stages[1].Verdict)
	firstStage, err := os.ReadFile(filepath.Join(dir, "stage-1", "run.json"))
	require.NoError(t, err)

	started = time.Now()
	result, err := ResumeOpenShellSequence(ctx, dir, t.Logf)
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	resumeSeconds := time.Since(started).Seconds()
	after, err := os.ReadFile(filepath.Join(dir, "stage-1", "run.json"))
	require.NoError(t, err)
	require.Equal(t, firstStage, after, "the verified first stage is reused unchanged")
	var run OpenShellRun
	runBytes, err := decodeOpenShellSequence(filepath.Join(dir, "run.json"), &run)
	require.NoError(t, err)
	require.Len(t, run.Stages, 2)
	require.Len(t, run.SetAside, 1)
	aside := run.SetAside[0]
	require.Equal(t, filepath.Join(dir, "stage-2", "run.json"), aside.RunRecord)
	require.Equal(t, filepath.Join(dir, "stage-2-rerun-1", "run.json"), run.Stages[1].RunRecord)
	require.Equal(t, run.Stages[0].NextRevision, run.Stages[1].Revision)
	require.Equal(t, run.Stages[0].NextRevision, aside.Revision)
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
	indexAfter, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	require.NoError(t, err)
	require.Equal(t, index, indexAfter, "the host index is untouched")
	second, err := ResumeOpenShellSequence(ctx, dir, nil)
	require.NoError(t, err)
	require.NotNil(t, second.Export)
	assert.Equal(t, result.Export.Manifest.DiffDigest, second.Export.Manifest.DiffDigest)
	assert.Equal(t, result.OpenShellAttempts, second.OpenShellAttempts)

	tokens, cost, complete := run.Spend()
	report := map[string]any{
		"schema": 1, "verdict": "pass", "date": time.Now().UTC().Format(time.RFC3339),
		"fixture": "Public Roman numeral edit followed by a no-change review; stage 2 stopped after its first model response",
		"runtime": "vm", "profile": "cerebras", "model": "openai/gpt-oss-120b",
		"stopped_after_seconds": stoppedSeconds, "resume_seconds": resumeSeconds,
		"stopped_stage_verdict": stopped.Stages[1].Verdict, "set_aside": aside,
		"rerun_record": "stage-2-rerun-1/run.json", "first_stage_reused_unchanged": true,
		"snapshot_lineage_matches": true, "cumulative_patch_reproduces_verified_tree": true,
		"completed_resume_idempotent": true, "host_index_unchanged": true,
		"attempt_usage": result.OpenShellAttempts, "provenance": run.Provenance,
		"run_record_sha256": fmt.Sprintf("%x", sha256.Sum256(runBytes)), "patch_sha256": result.Export.Manifest.DiffDigest,
		"spend":  map[string]any{"tokens": tokens, "cost_usd": cost, "complete": complete},
		"limits": "One public fixture on one host. The stop is a controller cancellation, as a brain stop delivers it; the brain's own shutdown path has unit tests only.",
	}
	data, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "qualification.json"), append(data, '\n'), 0o600))
	t.Logf("qualification: %s", filepath.Join(root, "qualification.json"))
}
