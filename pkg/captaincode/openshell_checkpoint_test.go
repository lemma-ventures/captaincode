package captaincode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkpointForRun(t *testing.T, r *OpenShellRunner) OpenShellCheckpoint {
	t.Helper()
	var run OpenShellRun
	_, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &run)
	require.NoError(t, err)
	verified := 0
	for _, stage := range run.Stages {
		if stage.Verdict != "pass" {
			break
		}
		verified++
	}
	checkpoint, err := openShellCheckpoint(r.RunDir, &run, verified)
	require.NoError(t, err)
	return checkpoint
}

func TestOpenShellCheckpointPersistedBeforeSandboxDispatch(t *testing.T) {
	r := newFakeOpenShell(t)
	t.Setenv("HOME", t.TempDir())
	ledger, err := LoadLedger()
	require.NoError(t, err)
	ledger.RecordTaskState(TaskState{TaskID: "task", State: StateRunning})
	ledger.RecordAttemptState(AttemptState{TaskID: "task", AttemptID: "attempt", Leg: LegOpenShell, State: StateRunning})
	calls := 0
	ctx := WithOpenShellCheckpoint(context.Background(), func(checkpoint OpenShellCheckpoint) error {
		calls++
		assert.Equal(t, checkpointForRun(t, r), checkpoint)
		assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-1"))
		assert.FileExists(t, filepath.Join(r.RunDir, "run.json"))
		require.NoError(t, ledger.RecordOpenShellCheckpoint("attempt", checkpoint))
		require.NoError(t, ledger.Save())
		return errors.New("simulated persistence interruption")
	})
	res, err := runOpenShellSequence(ctx, r, resumeSequenceTeams(), nil)
	require.ErrorContains(t, err, "persist task checkpoint before dispatch")
	assert.Nil(t, res.Export)
	assert.Equal(t, 1, calls)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-1"))
	ledger, err = LoadLedger()
	require.NoError(t, err)
	ledger.ReconcileOnStartup()
	as := ledger.AttemptStateFor("attempt")
	assert.Equal(t, StateInterrupted, as.State)
	require.NotNil(t, as.OpenShell)
	assert.Equal(t, checkpointForRun(t, r), *as.OpenShell)
	recovery, err := PrepareOpenShellRecovery(context.Background(), *as.OpenShell, nil)
	require.Error(t, err, "a persisted plan without a completed stage cannot resume")
	assert.Nil(t, recovery)
}

func TestOpenShellTaskBoundRecoveryHoldsLockUntilClosed(t *testing.T) {
	r := pauseOpenShellSequence(t)
	checkpoint := checkpointForRun(t, r)
	recovery, err := PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.NoError(t, err)
	defer recovery.Close()
	_, err = PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.ErrorContains(t, err, "already active")
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
	result, err := recovery.Run(context.Background())
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	_, err = recovery.Run(context.Background())
	require.ErrorContains(t, err, "already used")
	require.NoError(t, recovery.Close())
	_, err = recovery.Run(context.Background())
	require.ErrorContains(t, err, "closed")
	second, err := PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.ErrorContains(t, err, "not saved to the task checkpoint")
	assert.Nil(t, second)
	second, err = PrepareOpenShellRecovery(context.Background(), checkpointForRun(t, r), nil)
	require.NoError(t, err)
	require.NoError(t, second.Close())
}

func TestOpenShellTaskBoundRecoveryRejectsSubstitutedPlan(t *testing.T) {
	r := pauseOpenShellSequence(t)
	checkpoint := checkpointForRun(t, r)
	checkpoint.SequenceSHA256 = strings.Repeat("0", 64)
	_, err := PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.ErrorContains(t, err, "does not match the task checkpoint")
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
	recovery, err := PrepareOpenShellRecovery(context.Background(), checkpointForRun(t, r), nil)
	require.NoError(t, err, "failed preparation releases the lock")
	defer recovery.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := recovery.Run(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result.Export)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
}

func TestOpenShellCheckpointCannotRebindAttempt(t *testing.T) {
	ledger := &Ledger{}
	ref := OpenShellCheckpoint{RunDir: "/tmp/run", SequenceSHA256: strings.Repeat("a", 64), EvidenceSHA256: strings.Repeat("b", 64)}
	require.Error(t, ledger.RecordOpenShellCheckpoint("missing", ref))
	ledger.RecordAttemptState(AttemptState{AttemptID: "host", Leg: LegClaude, State: StateRunning})
	require.Error(t, ledger.RecordOpenShellCheckpoint("host", ref))
	ledger.RecordAttemptState(AttemptState{AttemptID: "sandbox", Leg: LegOpenShell, State: StateRunning})
	require.NoError(t, ledger.RecordOpenShellCheckpoint("sandbox", ref))
	ref.SequenceSHA256 = strings.Repeat("b", 64)
	require.ErrorContains(t, ledger.RecordOpenShellCheckpoint("sandbox", ref), "different sequence")
	assert.Equal(t, strings.Repeat("a", 64), ledger.AttemptStateFor("sandbox").OpenShell.SequenceSHA256)
	ref.RunDir = "relative"
	require.Error(t, ref.Validate())
}

func TestOpenShellCheckpointFailureStopsAtVerifiedStage(t *testing.T) {
	r := newFakeOpenShell(t)
	calls := 0
	ctx := WithOpenShellCheckpoint(context.Background(), func(checkpoint OpenShellCheckpoint) error {
		calls++
		assert.Equal(t, checkpointForRun(t, r), checkpoint)
		if calls == 1 {
			return nil
		}
		var run OpenShellRun
		_, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &run)
		require.NoError(t, err)
		require.Len(t, run.Stages, 1)
		assert.Equal(t, "pass", run.Stages[0].Verdict)
		assert.NotEmpty(t, run.Stages[0].NextRevision)
		return errors.New("ledger unavailable")
	})
	res, err := runOpenShellSequence(ctx, r, resumeSequenceTeams(), nil)
	require.ErrorContains(t, err, "persist task checkpoint after stage 1: ledger unavailable")
	assert.Nil(t, res.Export)
	assert.Equal(t, 2, calls)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
	res, err = ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err)
	require.NotNil(t, res.Export)
}

func TestOpenShellTaskCheckpointRejectsConsistentEvidenceRewrite(t *testing.T) {
	r := pauseOpenShellSequence(t)
	checkpoint := checkpointForRun(t, r)
	var run, stage OpenShellRun
	_, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &run)
	require.NoError(t, err)
	stageDir := filepath.Join(r.RunDir, "stage-1")
	_, err = decodeOpenShellSequence(filepath.Join(stageDir, "run.json"), &stage)
	require.NoError(t, err)
	require.Equal(t, 1, *stage.Tasks[0].Report.WorkerAttempts)
	*stage.Tasks[0].Report.WorkerAttempts = 0
	stage.AttemptUsage.Workers = 0
	run.Tasks = stage.Tasks
	run.AttemptUsage.Workers = 0
	require.NoError(t, saveOpenShellSequence(stageDir, &stage))
	require.NoError(t, saveOpenShellSequence(r.RunDir, &run))
	recovery, err := PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	if recovery != nil {
		require.NoError(t, recovery.Close())
	}
	require.ErrorContains(t, err, "evidence")
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
}

func TestOpenShellCheckpointEvidenceSurvivesLedgerReloadAndResume(t *testing.T) {
	r := newFakeOpenShell(t)
	t.Setenv("HOME", t.TempDir())
	ledger, err := LoadLedger()
	require.NoError(t, err)
	ledger.RecordAttemptState(AttemptState{TaskID: "task", AttemptID: "attempt", Leg: LegOpenShell, State: StateRunning})
	var checkpoints []OpenShellCheckpoint
	save := func(checkpoint OpenShellCheckpoint) error {
		checkpoints = append(checkpoints, checkpoint)
		if err := ledger.RecordOpenShellCheckpoint("attempt", checkpoint); err != nil {
			return err
		}
		return ledger.Save()
	}
	ctx, cancel := context.WithCancel(WithOpenShellCheckpoint(context.Background(), save))
	defer cancel()
	r.Log = func(format string, args ...any) {
		if fmt.Sprintf(format, args...) == "stage 2/3: checkpoint verified" {
			cancel()
		}
	}
	teams := resumeSequenceTeams()
	teams = append(teams, OpenShellTeam{Schema: 1, ID: "s3", Tasks: []OpenShellTask{
		fakeOpenShellTask("s3-w1", "nim", `{"expect":{"a.txt":"second\n"},"write":{"a.txt":"third\n"}}`, "all", "a.txt"),
	}})
	result, err := runOpenShellSequence(ctx, r, teams, nil)
	require.ErrorIs(t, err, ErrInterrupted)
	assert.Nil(t, result.Export)
	require.Len(t, checkpoints, 3)
	for i, checkpoint := range checkpoints {
		assert.Equal(t, i, checkpoint.VerifiedStages)
		assert.Len(t, checkpoint.EvidenceSHA256, 64)
		if i > 0 {
			assert.NotEqual(t, checkpoints[i-1].EvidenceSHA256, checkpoint.EvidenceSHA256)
		}
	}
	ledger, err = LoadLedger()
	require.NoError(t, err)
	checkpoint := *ledger.AttemptStateFor("attempt").OpenShell
	assert.Equal(t, checkpoints[2], checkpoint)
	before, err := os.ReadFile(filepath.Join(r.RunDir, "stage-1", "run.json"))
	require.NoError(t, err)
	recovery, err := PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.NoError(t, err)
	defer recovery.Close()
	result, err = recovery.Run(WithOpenShellCheckpoint(context.Background(), save))
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	require.Len(t, checkpoints, 4, "reused stages do not roll back the ledger checkpoint")
	assert.Equal(t, 3, checkpoints[3].VerifiedStages)
	after, err := os.ReadFile(filepath.Join(r.RunDir, "stage-1", "run.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	ledger, err = LoadLedger()
	require.NoError(t, err)
	assert.Equal(t, checkpoints[3], *ledger.AttemptStateFor("attempt").OpenShell)
}

func TestOpenShellCheckpointProgressCannotChangeOrRegress(t *testing.T) {
	ledger := &Ledger{}
	ledger.RecordAttemptState(AttemptState{AttemptID: "attempt", Leg: LegOpenShell, State: StateRunning})
	checkpoint := OpenShellCheckpoint{RunDir: "/tmp/run", SequenceSHA256: strings.Repeat("a", 64), EvidenceSHA256: strings.Repeat("b", 64)}
	require.NoError(t, ledger.RecordOpenShellCheckpoint("attempt", checkpoint))
	checkpoint.VerifiedStages, checkpoint.EvidenceSHA256 = 1, strings.Repeat("c", 64)
	require.NoError(t, ledger.RecordOpenShellCheckpoint("attempt", checkpoint))
	require.NoError(t, ledger.RecordOpenShellCheckpoint("attempt", checkpoint))
	for _, name := range []string{"digest", "rollback", "skip", "legacy", "negative", "too many", "malformed"} {
		t.Run(name, func(t *testing.T) {
			changed := checkpoint
			switch name {
			case "digest":
				changed.EvidenceSHA256 = strings.Repeat("d", 64)
			case "rollback":
				changed.VerifiedStages = 0
			case "skip":
				changed.VerifiedStages = 3
			case "legacy":
				changed.VerifiedStages, changed.EvidenceSHA256 = 0, ""
			case "negative":
				changed.VerifiedStages = -1
			case "too many":
				changed.VerifiedStages = MaxWorkflowStages + 1
			case "malformed":
				changed.EvidenceSHA256 = strings.Repeat("z", 64)
			}
			require.Error(t, ledger.RecordOpenShellCheckpoint("attempt", changed))
			assert.Equal(t, checkpoint, *ledger.AttemptStateFor("attempt").OpenShell)
		})
	}
}

func TestOpenShellTaskCheckpointRefusesUnanchoredCompletedStage(t *testing.T) {
	r := newFakeOpenShell(t)
	var checkpoint OpenShellCheckpoint
	ctx := WithOpenShellCheckpoint(context.Background(), func(next OpenShellCheckpoint) error {
		if next.VerifiedStages == 0 {
			checkpoint = next
			return nil
		}
		return errors.New("ledger unavailable")
	})
	_, err := runOpenShellSequence(ctx, r, resumeSequenceTeams(), nil)
	require.ErrorContains(t, err, "ledger unavailable")
	recovery, err := PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.ErrorContains(t, err, "not saved to the task checkpoint")
	assert.Nil(t, recovery)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
}

func TestOpenShellTaskCheckpointRefusesLegacyUnboundEvidence(t *testing.T) {
	r := pauseOpenShellSequence(t)
	checkpoint := checkpointForRun(t, r)
	checkpoint.VerifiedStages, checkpoint.EvidenceSHA256 = 0, ""
	recovery, err := PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.ErrorContains(t, err, "lacks valid verified-stage evidence")
	assert.Nil(t, recovery)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
}

func TestOpenShellTaskCheckpointRejectsEvidenceChangedAfterAdmission(t *testing.T) {
	r := pauseOpenShellSequence(t)
	recovery, err := PrepareOpenShellRecovery(context.Background(), checkpointForRun(t, r), nil)
	require.NoError(t, err)
	defer recovery.Close()
	file := filepath.Join(r.RunDir, "stage-1", "run.json")
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, append(data, '\n'), 0o600))
	result, err := recovery.Run(context.Background())
	require.ErrorContains(t, err, "evidence changed after recovery admission")
	assert.Nil(t, result.Export)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
}

func TestOpenShellTaskCheckpointRetainsBindingWhenInterruptedStageIsRerun(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_MAX_WALLTIME", "")
	teams := resumeSequenceTeams()
	teams[1].Tasks[0].Prompt = fmt.Sprintf(`{"hang_once":%q,"expect":{"a.txt":"first\n"},"write":{"a.txt":"second\n"}}`,
		filepath.Join(t.TempDir(), "hung"))
	r := interruptOpenShellStage2(t, teams, true)
	checkpoint := checkpointForRun(t, r)
	require.Equal(t, 1, checkpoint.VerifiedStages)
	recovery, err := PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.NoError(t, err)
	defer recovery.Close()
	ctx := WithOpenShellCheckpoint(context.Background(), func(next OpenShellCheckpoint) error {
		checkpoint = next
		return nil
	})
	result, err := recovery.Run(ctx)
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	require.NoError(t, recovery.Close())
	require.Equal(t, 2, checkpoint.VerifiedStages)
	run := savedOpenShellSequence(t, r)
	require.Len(t, run.SetAside, 1)
	again, err := PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.NoError(t, err)
	require.NoError(t, again.Close())
	run.SetAside[0].Attempts.Workers++
	require.NoError(t, saveOpenShellSequence(r.RunDir, &run))
	_, err = PrepareOpenShellRecovery(context.Background(), checkpoint, nil)
	require.ErrorContains(t, err, "evidence does not match")
}
