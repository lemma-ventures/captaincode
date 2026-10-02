package captaincode

import (
	"context"
	"crypto/sha256"
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
	data, err := os.ReadFile(filepath.Join(r.RunDir, "sequence.json"))
	require.NoError(t, err)
	return OpenShellCheckpoint{RunDir: r.RunDir, SequenceSHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
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
	ref := OpenShellCheckpoint{RunDir: "/tmp/run", SequenceSHA256: strings.Repeat("a", 64)}
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
