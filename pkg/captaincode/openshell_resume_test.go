package captaincode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resumeSequenceTeams() []OpenShellTeam {
	return []OpenShellTeam{
		{Schema: 1, ID: "s1", Tasks: []OpenShellTask{fakeOpenShellTask("s1-w1", "nim", `{"write":{"a.txt":"first\n"}}`, "all", "a.txt")}},
		{Schema: 1, ID: "s2", Tasks: []OpenShellTask{fakeOpenShellTask("s2-w1", "nim", `{"expect":{"a.txt":"first\n"},"write":{"a.txt":"second\n"}}`, "all", "a.txt")}},
	}
}

func pauseOpenShellSequence(t *testing.T) *OpenShellRunner {
	t.Helper()
	r := newFakeOpenShell(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Log = func(format string, args ...any) {
		if fmt.Sprintf(format, args...) == "stage 1/2: checkpoint verified" {
			cancel()
		}
	}
	result, err := runOpenShellSequence(ctx, r, resumeSequenceTeams(), nil)
	require.ErrorIs(t, err, ErrInterrupted)
	require.Nil(t, result.Export)
	require.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
	return r
}

func TestOpenShellSequenceStageRefusesAChangedPilot(t *testing.T) {
	r := newFakeOpenShell(t)
	r.Log = func(format string, args ...any) {
		if fmt.Sprintf(format, args...) == "stage 1/2: checkpoint verified" {
			require.NoError(t, os.WriteFile(filepath.Join(r.Pilot, "task.py"), []byte("changed after the plan\n"), 0o700))
		}
	}
	result, err := runOpenShellSequence(context.Background(), r, resumeSequenceTeams(), nil)
	require.ErrorContains(t, err, "changed since the sequence plan")
	assert.Nil(t, result.Export)
	assert.DirExists(t, filepath.Join(r.RunDir, "stage-1", "tasks", "s1-w1"))
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2", "tasks"), "stage 2 dispatched no worker")
	assert.NoFileExists(t, filepath.Join(r.RunDir, "integrated.patch"))
}

func TestOpenShellSequenceResumeContinuesOnce(t *testing.T) {
	r := pauseOpenShellSequence(t)
	before, err := os.ReadFile(filepath.Join(r.RunDir, "stage-1", "run.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(r.Repo, "a.txt"), []byte("operator staged\n"), 0o600))
	require.NoError(t, exec.Command("git", "-C", r.Repo, "add", "a.txt").Run())
	index, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(r.Repo, "a.txt"), []byte("operator unstaged\n"), 0o600))

	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	assert.Equal(t, r.Repo, result.Export.Repository)
	assert.Equal(t, r.Revision, result.Export.Manifest.BaseRevision)
	patch, err := os.ReadFile(result.Export.Manifest.DiffPath)
	require.NoError(t, err)
	assert.Contains(t, string(patch), "+second")
	assert.NotContains(t, string(patch), "+first")
	assert.NotContains(t, string(patch), "operator")
	after, err := os.ReadFile(filepath.Join(r.RunDir, "stage-1", "run.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "the verified stage was reused without another worker or director call")
	after, err = os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, index, after)
	after, err = os.ReadFile(filepath.Join(r.Repo, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "operator unstaged\n", string(after))
	var run OpenShellRun
	runData, err := decodeOpenShellSequence(result.Export.RunRecord, &run)
	require.NoError(t, err)
	require.Len(t, run.Resumptions, 1)
	require.Len(t, run.Stages, 2)
	assert.Equal(t, run.Stages[0].NextRevision, run.Stages[1].Revision)
	assert.Equal(t, run.Stages[1].Tree, run.Integrated.Tree)
	replayed, err := r.treeWith(context.Background(), patch)
	require.NoError(t, err)
	assert.Equal(t, run.Integrated.Tree, replayed)

	second, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err)
	require.NotNil(t, second.Export)
	assert.Equal(t, result.Export.Manifest.DiffDigest, second.Export.Manifest.DiffDigest)
	after, err = os.ReadFile(result.Export.RunRecord)
	require.NoError(t, err)
	assert.Equal(t, runData, after, "re-reading a completed sequence must not rewrite history")
}

func TestOpenShellSequenceResumeRejectsAlteredState(t *testing.T) {
	for _, name := range []string{"plan", "pilot", "policy", "python", "stage patch", "worker patch", "snapshot lineage",
		"worker gate", "integrated gate", "verify argv", "worker tree", "missing stage", "in-flight stage",
		"symlinked stage", "symlinked evidence", "filters"} {
		t.Run(name, func(t *testing.T) {
			r := pauseOpenShellSequence(t)
			stageDir := filepath.Join(r.RunDir, "stage-1")
			var stage OpenShellRun
			_, err := decodeOpenShellSequence(filepath.Join(stageDir, "run.json"), &stage)
			require.NoError(t, err)
			saveStage := false
			switch name {
			case "plan":
				file := filepath.Join(r.RunDir, "sequence.json")
				data, err := os.ReadFile(file)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(file, append(data, ' '), 0o600))
			case "pilot":
				require.NoError(t, os.WriteFile(filepath.Join(r.Pilot, "task.py"), []byte("changed"), 0o600))
			case "policy":
				require.NoError(t, os.WriteFile(filepath.Join(r.Pilot, "policy.yaml"), []byte("changed"), 0o600))
			case "python":
				require.NoError(t, os.WriteFile(filepath.Join(r.Prepared, "venv", "bin", "python"), []byte("changed"), 0o700))
			case "stage patch":
				require.NoError(t, os.WriteFile(stage.Integrated.Patch, []byte("changed"), 0o600))
			case "worker patch":
				require.NoError(t, os.WriteFile(filepath.Join(stage.Tasks[0].Evidence, "result.patch"), []byte("changed"), 0o600))
			case "snapshot lineage":
				stage.Revision = strings.Repeat("a", 40)
				saveStage = true
			case "worker gate":
				stage.Tasks[0].Report.Checks["filesystem_denied"] = OpenShellCheck{Verdict: "fail"}
				saveStage = true
			case "integrated gate":
				stage.Integrated.Report.Checks["network_denied"] = OpenShellCheck{Verdict: "fail"}
				saveStage = true
			case "verify argv":
				stage.Integrated.Verify = []string{"true"}
				saveStage = true
			case "worker tree":
				stage.Tasks[0].Report.Export.Tree = strings.Repeat("a", 40)
				saveStage = true
			case "missing stage":
				require.NoError(t, os.Remove(filepath.Join(stageDir, "run.json")))
			case "in-flight stage":
				require.NoError(t, os.Mkdir(filepath.Join(r.RunDir, "stage-2"), 0o700))
				var run OpenShellRun
				_, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &run)
				require.NoError(t, err)
				run.Stages = append(run.Stages, OpenShellStageRecord{Stage: 2, Revision: run.Stages[0].NextRevision,
					RunRecord: filepath.Join(r.RunDir, "stage-2", "run.json"), Verdict: "running"})
				require.NoError(t, saveOpenShellSequence(r.RunDir, &run))
			case "symlinked stage":
				target := filepath.Join(t.TempDir(), "stage")
				require.NoError(t, os.Rename(stageDir, target))
				require.NoError(t, os.Symlink(target, stageDir))
			case "symlinked evidence":
				target := filepath.Join(t.TempDir(), "evidence")
				require.NoError(t, os.Rename(stage.Tasks[0].Evidence, target))
				require.NoError(t, os.Symlink(target, stage.Tasks[0].Evidence))
			case "filters":
				require.NoError(t, os.WriteFile(filepath.Join(r.RunDir, "snapshot", ".git", "info", "attributes"), nil, 0o600))
			}
			if saveStage {
				require.NoError(t, saveOpenShellSequence(stageDir, &stage))
			}
			before, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
			require.NoError(t, err)
			result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
			require.Error(t, err)
			assert.Nil(t, result.Export)
			assert.NotContains(t, result.Text, "apply with:")
			after, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
			require.NoError(t, err)
			assert.Equal(t, before, after, "refusal must preserve the checkpoint")
			assert.NoFileExists(t, filepath.Join(r.RunDir, "stage-2", "run.json"))
		})
	}
}

func TestOpenShellSequenceResumeRefusesFailedStage(t *testing.T) {
	r := newFakeOpenShell(t)
	teams := resumeSequenceTeams()
	teams[1].Tasks[0].Prompt = `{"fail":true}`
	_, err := runOpenShellSequence(context.Background(), r, teams, nil)
	require.Error(t, err)
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.ErrorContains(t, err, "in-flight work is not replayed")
	assert.Nil(t, result.Export)
}

func TestOpenShellSequenceResumeReview(t *testing.T) {
	r := newFakeOpenShell(t)
	teams := resumeSequenceTeams()
	teams[0].Tasks[0].Mode = "review"
	teams[0].Tasks[0].Allowed = nil
	teams[0].Tasks[0].Prompt = `{}`
	teams[0].Tasks[0].Baseline = "pass"
	teams[1].Tasks[0].Prompt = `{"expect":{"a.txt":"a\n"},"write":{"a.txt":"edited\n"}}`
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Log = func(format string, args ...any) {
		if fmt.Sprintf(format, args...) == "stage 1/2: checkpoint verified" {
			cancel()
		}
	}
	_, err := runOpenShellSequence(ctx, r, teams, nil)
	require.ErrorIs(t, err, ErrInterrupted)
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	assert.Equal(t, []string{"a.txt"}, result.Export.Manifest.ChangedFiles)
}

func TestOpenShellSequenceLockRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "public", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "sequence.lock")
			target := filepath.Join(t.TempDir(), "target")
			require.NoError(t, os.WriteFile(target, []byte("unchanged"), 0o600))
			switch kind {
			case "symlink":
				require.NoError(t, os.Symlink(target, file))
			case "hardlink":
				require.NoError(t, os.Link(target, file))
			case "public":
				require.NoError(t, os.WriteFile(file, nil, 0o600))
				require.NoError(t, os.Chmod(file, 0o666))
			case "fifo":
				require.NoError(t, syscall.Mkfifo(file, 0o600))
			}
			lock, err := lockOpenShellSequence(dir, true)
			require.Error(t, err)
			assert.Nil(t, lock)
			data, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.Equal(t, "unchanged", string(data))
		})
	}
}

func TestOpenShellResumeLeavesNoLockInAnOrdinaryDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a run\n"), 0o600))
	_, err := ResumeOpenShellSequence(context.Background(), dir, nil)
	require.ErrorContains(t, err, "not a sandbox sequence run directory")
	assert.NoFileExists(t, filepath.Join(dir, "sequence.lock"))
}

func TestOpenShellSequenceCrashHelper(t *testing.T) {
	input := os.Getenv("CAPTAIN_OPENSHELL_SEQUENCE_HELPER")
	if input == "" {
		t.Skip("subprocess only")
	}
	var p openShellSequencePlan
	_, err := decodeOpenShellSequence(input, &p)
	require.NoError(t, err)
	r := &OpenShellRunner{Pilot: p.Pilot, Prepared: p.Prepared, StateRoot: p.StateRoot, RunDir: p.RunDir,
		Repo: p.Repo, Revision: p.Revision, Runtime: p.Runtime, Concurrency: p.Concurrency, DirectorName: p.Director}
	r.Log = func(format string, args ...any) {
		if fmt.Sprintf(format, args...) == "stage 1/2: checkpoint verified" {
			if err := os.WriteFile(filepath.Join(r.RunDir, "ready"), nil, 0o600); err != nil {
				os.Exit(2)
			}
			if os.Getenv("CAPTAIN_OPENSHELL_SEQUENCE_CRASH_EXIT") == "1" {
				os.Exit(87)
			}
			time.Sleep(time.Minute)
			os.Exit(3)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, err = runOpenShellSequence(ctx, r, p.Teams, nil)
	require.NoError(t, err)
}

func TestOpenShellSequenceResumeAfterCoordinatorDeath(t *testing.T) {
	r := newFakeOpenShell(t)
	p := openShellSequencePlan{Pilot: r.Pilot, Prepared: r.Prepared, StateRoot: r.StateRoot,
		RunDir: r.RunDir, Repo: r.Repo, Revision: r.Revision, Runtime: r.Runtime,
		Concurrency: r.Concurrency, Teams: resumeSequenceTeams()}
	data, err := json.Marshal(p)
	require.NoError(t, err)
	input := filepath.Join(t.TempDir(), "input.json")
	require.NoError(t, os.WriteFile(input, data, 0o600))
	self, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestOpenShellSequenceCrashHelper$")
	cmd.Env = append(os.Environ(), "CAPTAIN_OPENSHELL_SEQUENCE_HELPER="+input)
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(filepath.Join(r.RunDir, "ready")); err == nil {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, ready, "coordinator did not reach the stage boundary")
	blocked, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.ErrorContains(t, err, "already active")
	assert.Nil(t, blocked.Export)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	assert.Equal(t, []string{"a.txt"}, result.Export.Manifest.ChangedFiles)
}

func TestOpenShellSequenceResumeCanRecoverRecovery(t *testing.T) {
	r := newFakeOpenShell(t)
	result, err := runOpenShellSequence(context.Background(), r, resumeSequenceTeams(), nil)
	require.NoError(t, err)
	var run OpenShellRun
	_, err = decodeOpenShellSequence(result.Export.RunRecord, &run)
	require.NoError(t, err)
	stageBytes, err := os.ReadFile(run.Stages[1].RunRecord)
	require.NoError(t, err)
	run.Verdict, run.Integrated = "fail", nil
	require.NoError(t, saveOpenShellSequence(r.RunDir, &run))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err = ResumeOpenShellSequence(ctx, r.RunDir, func(format string, args ...any) {
		if fmt.Sprintf(format, args...) == "stage 1/2: checkpoint verified" {
			cancel()
		}
	})
	require.ErrorIs(t, err, ErrInterrupted)
	assert.Nil(t, result.Export)
	result, err = ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	after, err := os.ReadFile(run.Stages[1].RunRecord)
	require.NoError(t, err)
	assert.Equal(t, stageBytes, after)
}

func TestOpenShellSequenceResumeReconcilesStageAheadOfCheckpoint(t *testing.T) {
	r := pauseOpenShellSequence(t)
	var run OpenShellRun
	_, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &run)
	require.NoError(t, err)
	run.Stages[0].Verdict = "running"
	run.Stages[0].Tree, run.Stages[0].NextRevision = "", ""
	require.NoError(t, saveOpenShellSequence(r.RunDir, &run))
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
}

func TestOpenShellSequenceNewRunCannotOverwriteCheckpoint(t *testing.T) {
	r := pauseOpenShellSequence(t)
	before, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
	require.NoError(t, err)
	result, err := runOpenShellSequence(context.Background(), r, resumeSequenceTeams(), nil)
	require.ErrorContains(t, err, "already has state")
	assert.Nil(t, result.Export)
	after, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestOpenShellSequenceResumeRejectsAlteredCompletedAggregate(t *testing.T) {
	for _, part := range []string{"tree", "spend", "patch"} {
		t.Run(part, func(t *testing.T) {
			r := newFakeOpenShell(t)
			result, err := runOpenShellSequence(context.Background(), r, resumeSequenceTeams(), nil)
			require.NoError(t, err)
			var run OpenShellRun
			_, err = decodeOpenShellSequence(result.Export.RunRecord, &run)
			require.NoError(t, err)
			switch part {
			case "tree":
				run.Integrated.Tree = strings.Repeat("a", 40)
			case "spend":
				run.Tasks[0].Report.Shield.Requests++
			case "patch":
				require.NoError(t, os.WriteFile(run.Integrated.Patch, nil, 0o600))
			}
			require.NoError(t, saveOpenShellSequence(r.RunDir, &run))
			result, err = ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
			require.Error(t, err)
			assert.Nil(t, result.Export)
		})
	}
}
