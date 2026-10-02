package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/require"
)

const openShellRecoveryFixture = "/openshell Fix roman.py to implement standard subtractive Roman numerals for integers 1 through 3999 and raise ValueError outside that range. Read and run test_roman.py. Change only roman.py. > /openshell --review Review roman.py against test_roman.py. Run the tests and report findings. Do not modify any repository file."

func TestOpenShellTaskCrashHelper(t *testing.T) {
	root := os.Getenv("CAPTAIN_TEST_OPENSHELL_TASK_CRASH_ROOT")
	if root == "" {
		t.Skip("subprocess only")
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	ledger, err := captaincode.LoadLedger()
	require.NoError(t, err)
	_, attempt, err := beginOpenShellSolo(ledger, openShellRecoveryFixture, captaincode.CurrentProcessID())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	calls := 0
	ctx = captaincode.WithOpenShellCheckpoint(ctx, func(checkpoint captaincode.OpenShellCheckpoint) error {
		if err := ledger.RecordOpenShellCheckpoint(attempt, checkpoint); err != nil {
			return err
		}
		if err := ledger.Save(); err != nil {
			return err
		}
		calls++
		if calls == 2 {
			os.Exit(87)
		}
		return nil
	})
	wf, err := captaincode.ParseOpenShellWorkflow(openShellRecoveryFixture)
	require.NoError(t, err)
	_, err = (captaincode.Workspace{Dir: filepath.Join(root, "repo")}).RunOpenShellWorkflow(ctx, wf, "")
	require.NoError(t, err)
	t.Fatal("coordinator did not exit after the first verified stage")
}

func TestOpenShellTaskResumeLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_TASK_RESUME_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_TASK_RESUME_LIVE=1 with a prepared VM runtime and provider key")
	}
	qualifyOpenShellTaskRecovery(t, false)
}

func TestOpenShellStartupLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_STARTUP_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_STARTUP_LIVE=1 with a prepared VM runtime and provider key")
	}
	qualifyOpenShellTaskRecovery(t, true)
}

func qualifyOpenShellTaskRecovery(t *testing.T, automatic bool) {
	t.Helper()
	t.Setenv("CAPTAIN_OPENSHELL_AUTO_RESUME", "0")
	if automatic {
		t.Setenv("CAPTAIN_OPENSHELL_AUTO_RESUME", "1")
	}
	prepared := os.Getenv("CAPTAIN_OPENSHELL_PREPARED")
	require.NotEmpty(t, prepared)
	pinOpenShellLiveDocker(t)
	pilot, err := filepath.Abs("../../examples/openshell-pilot")
	require.NoError(t, err)
	root, err := os.MkdirTemp("/tmp", "cc-task-resume-")
	require.NoError(t, err)
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	t.Logf("isolated evidence: %s", root)
	b := openShellHTTPBrain(t)
	home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
	require.NoError(t, os.Mkdir(home, 0o700))
	require.NoError(t, os.Mkdir(repo, 0o700))
	for key, value := range map[string]string{
		"HOME": home, "CAPTAIN_OPENSHELL_PREPARED": prepared, "CAPTAIN_OPENSHELL_PILOT": pilot,
		"CAPTAIN_OPENSHELL_REPO": "", "CAPTAIN_OPENSHELL_REVISION": "HEAD", "CAPTAIN_OPENSHELL_RUNTIME": "vm",
		"CAPTAIN_OPENSHELL_PROFILE": "cerebras", "CAPTAIN_OPENSHELL_ALLOWED": "roman.py",
		"CAPTAIN_OPENSHELL_VERIFY":    `["python3","-m","unittest","-v"]`,
		"CAPTAIN_OPENSHELL_PROTECTED": "test_roman.py,operator.txt", "CAPTAIN_OPENSHELL_BASELINE": "fail",
		"CAPTAIN_OPENSHELL_DIRECTOR": "none", "CAPTAIN_OPENSHELL_CONCURRENCY": "1", "CAPTAIN_TASK_TOKEN": "",
	} {
		t.Setenv(key, value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	git := func(dir string, args ...string) string {
		gitCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		out, err := exec.CommandContext(gitCtx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	for _, name := range []string{"roman.py", "test_roman.py"} {
		data, err := os.ReadFile(filepath.Join(pilot, "team", "repo", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), data, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("baseline\n"), 0o600))
	git(repo, "init", "-q")
	git(repo, "add", ".")
	git(repo, "-c", "user.name=captain-test", "-c", "user.email=test@example.com", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "Public task recovery fixture")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("staged\n"), 0o600))
	git(repo, "add", "operator.txt")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("unstaged\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("local\n"), 0o600))
	before := map[string][]byte{}
	for _, name := range []string{"roman.py", "test_roman.py", "operator.txt", "untracked.txt", ".git/index"} {
		before[name], err = os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
	}
	self, err := os.Executable()
	require.NoError(t, err)
	log, err := os.OpenFile(filepath.Join(root, "initial-controller.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer log.Close()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestOpenShellTaskCrashHelper$")
	cmd.Env = append(os.Environ(), "CAPTAIN_TEST_OPENSHELL_TASK_CRASH_ROOT="+root)
	cmd.Stdout, cmd.Stderr = log, log
	started := time.Now()
	err = cmd.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 87, exit.ExitCode(), "inspect initial-controller.log")
	initialSeconds := time.Since(started).Seconds()
	b.ledger, err = captaincode.LoadLedger()
	require.NoError(t, err)
	require.Len(t, b.ledger.AttemptStates, 1)
	parent := b.ledger.AttemptStates[0]
	require.Equal(t, captaincode.StateRunning, parent.State)
	require.NotNil(t, parent.OpenShell)
	require.Nil(t, parent.Export)
	require.Equal(t, 1, b.ledger.ReconcileOnStartup())
	require.NoError(t, b.ledger.Save())
	runDir := parent.OpenShell.RunDir
	stageFile := filepath.Join(runDir, "stage-1", "run.json")
	firstStage, err := os.ReadFile(stageFile)
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(runDir, "stage-2"))
	server := httptest.NewServer(http.HandlerFunc(b.taskAPIHTTP))
	defer server.Close()
	defer func() {
		b.cancelTree.Cancel(parent.TaskID)
		require.Eventually(t, func() bool { return b.inflightRuns.Load() == 0 }, 3*time.Minute, 100*time.Millisecond)
	}()
	call := func(op captaincode.Operation, body any, status int, into any) {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		data, err := json.Marshal(captaincode.TaskRequest{Version: captaincode.TaskAPIVersion, Op: op, TaskID: parent.TaskID, Body: payload})
		require.NoError(t, err)
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(data))
		require.NoError(t, err)
		resp, err := server.Client().Do(r)
		require.NoError(t, err)
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, status, resp.StatusCode, string(data))
		if into != nil {
			var envelope captaincode.TaskResponse
			require.NoError(t, json.Unmarshal(data, &envelope))
			require.Equal(t, "ok", envelope.Status, string(data))
			require.NoError(t, json.Unmarshal(envelope.Body, into))
		}
	}
	sequenceFile := filepath.Join(runDir, "sequence.json")
	plan, err := os.ReadFile(sequenceFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sequenceFile, append(append([]byte{}, plan...), ' '), 0o600))
	request := captaincode.ResumeRequest{AttemptID: parent.AttemptID}
	if automatic {
		b.recoverOpenShellOnStartup(ctx)
	} else {
		call(captaincode.OpResume, request, http.StatusConflict, nil)
	}
	require.NoError(t, os.WriteFile(sequenceFile, plan, 0o600))
	require.Len(t, b.ledger.AttemptStates, 1)
	var resumed captaincode.ResumeResponse
	started = time.Now()
	if automatic {
		b.recoverOpenShellOnStartup(ctx)
		for _, as := range b.ledger.AttemptStatesFor(parent.TaskID) {
			if as.ParentAttempt == parent.AttemptID {
				resumed = captaincode.ResumeResponse{TaskID: parent.TaskID, AttemptID: as.AttemptID, ParentAttempt: as.ParentAttempt}
			}
		}
		require.NotEmpty(t, resumed.AttemptID)
		b.recoverOpenShellOnStartup(ctx)
	} else {
		call(captaincode.OpResume, request, http.StatusOK, &resumed)
	}
	require.NotEqual(t, parent.AttemptID, resumed.AttemptID)
	require.Equal(t, parent.AttemptID, resumed.ParentAttempt)
	call(captaincode.OpResume, request, http.StatusConflict, nil)
	require.Eventually(t, func() bool { return b.inflightRuns.Load() == 0 }, 8*time.Minute, 100*time.Millisecond)
	resumeSeconds := time.Since(started).Seconds()
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	require.Len(t, stored.AttemptStates, 2)
	require.Equal(t, captaincode.StateFailed, stored.AttemptStateFor(parent.AttemptID).State)
	next := stored.AttemptStateFor(resumed.AttemptID)
	require.NotNil(t, next)
	require.Equal(t, captaincode.StateSucceeded, next.State)
	require.Equal(t, captaincode.StateSucceeded, stored.TaskStateFor(parent.TaskID).State)
	require.True(t, next.CheckpointAt.After(next.StartedAt))
	require.NotNil(t, next.Export)
	require.Equal(t, parent.TaskID, next.Export.Manifest.TaskID)
	require.Equal(t, next.AttemptID, next.Export.Manifest.AttemptID)
	after, err := os.ReadFile(stageFile)
	require.NoError(t, err)
	require.Equal(t, firstStage, after)
	runBytes, err := os.ReadFile(next.Export.RunRecord)
	require.NoError(t, err)
	var run captaincode.OpenShellRun
	require.NoError(t, json.Unmarshal(runBytes, &run))
	require.Equal(t, "pass", run.Verdict)
	require.Len(t, run.Resumptions, 1)
	require.Len(t, run.Stages, 2)
	require.Len(t, run.Tasks, 2)
	require.Equal(t, run.Stages[0].NextRevision, run.Stages[1].Revision)
	require.Equal(t, run.Stages[0].Tree, run.Stages[1].Tree)
	requests := 0
	for _, worker := range run.Tasks {
		require.Len(t, worker.Report.Checks, 17)
		for name, check := range worker.Report.Checks {
			require.Equal(t, "pass", check.Verdict, name)
		}
		requests += worker.Report.Shield.Requests
	}
	for _, stage := range run.Stages {
		data, err := os.ReadFile(stage.RunRecord)
		require.NoError(t, err)
		var record captaincode.OpenShellRun
		require.NoError(t, json.Unmarshal(data, &record))
		require.Len(t, record.Integrated.Report.Checks, 8)
		for name, check := range record.Integrated.Report.Checks {
			require.Equal(t, "pass", check.Verdict, name)
		}
	}
	tokens, cost, complete := run.Spend()
	var charges []captaincode.Charge
	for _, charge := range stored.ChargesFor(parent.TaskID) {
		if charge.Kind == captaincode.KindCall {
			charges = append(charges, charge)
		}
	}
	require.Len(t, charges, 1)
	require.Equal(t, next.AttemptID, charges[0].Parent)
	require.Equal(t, tokens, charges[0].Usage.Total)
	if complete {
		require.Equal(t, captaincode.UsageMeasured, charges[0].Usage.CostStatus)
		require.InDelta(t, cost, charges[0].Usage.CostUSD, 1e-9)
	} else {
		require.Equal(t, captaincode.UsageUnknown, charges[0].Usage.CostStatus)
	}
	landing := filepath.Join(root, "landing")
	git(root, "clone", "--quiet", "--no-hardlinks", repo, landing)
	git(landing, "apply", "--index", next.Export.Manifest.DiffPath)
	require.Equal(t, run.Integrated.Tree, git(landing, "write-tree"))
	for name, data := range before {
		after, err := os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
		require.Equal(t, data, after, name)
	}
	b.mu.Lock()
	b.ledger = stored
	b.mu.Unlock()
	var artifacts captaincode.ArtifactsResponse
	call(captaincode.OpArtifacts, captaincode.ArtifactsRequest{}, http.StatusOK, &artifacts)
	require.Len(t, artifacts.Exports, 1)
	require.Equal(t, next.Export.Manifest.DiffDigest, artifacts.Exports[0].Manifest.DiffDigest)
	var inspection captaincode.InspectResponse
	call(captaincode.OpInspect, captaincode.InspectRequest{IncludeHandoff: true, IncludeCharges: true}, http.StatusOK, &inspection)
	require.NotNil(t, inspection.Handoff)
	require.Contains(t, captaincode.FormatHandoffBrief(*inspection.Handoff), "exported (not applied)")
	call(captaincode.OpResume, request, http.StatusConflict, nil)
	require.Empty(t, b.cancelTree.TaskIDs())
	limits := "One public fixture on one host. Explicit task API recovery at a verified stage boundary; no mid-worker or automatic recovery."
	if automatic {
		limits = "One public fixture on one host. Opt-in startup recovery after ledger reload at a verified stage boundary; no mid-worker recovery."
	}
	report := map[string]any{
		"automatic_startup_recovery": automatic,
		"schema":                     1, "verdict": "pass", "fixture": "Public Roman numeral edit followed by a no-change review",
		"runtime": "vm", "profile": "cerebras", "model": "openai/gpt-oss-120b", "controller_exit_code": 87,
		"initial_stage_seconds": initialSeconds, "resume_seconds": resumeSeconds, "wall_seconds": initialSeconds + resumeSeconds,
		"worker_checks_per_stage": 17, "integrated_checks_per_stage": 8, "stage_count": 2,
		"first_stage_reused_unchanged": true, "snapshot_lineage_matches": true, "cumulative_patch_reproduces_verified_tree": true,
		"host_files_and_index_unchanged": true, "altered_plan_refused": true, "duplicate_resume_refused": true,
		"restart_reconciled_attempt": true, "causal_attempts": 2, "usage_charged_once": true,
		"artifacts_and_handoff_survive_reload": true, "model_requests": requests, "provenance": run.Provenance,
		"run_record_sha256": fmt.Sprintf("%x", sha256.Sum256(runBytes)), "patch_sha256": next.Export.Manifest.DiffDigest,
		"spend":  map[string]any{"tokens": tokens, "cost_usd": cost, "complete": complete},
		"limits": limits,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "qualification.json"), append(data, '\n'), 0o600))
	t.Logf("qualification: %s", filepath.Join(root, "qualification.json"))
}
