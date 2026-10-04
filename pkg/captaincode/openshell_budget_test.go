package captaincode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenShellWallTimeStopsControllersAndQueuedWorkers(t *testing.T) {
	r := newFakeOpenShell(t)
	r.Concurrency = 1
	t.Setenv("CAPTAIN_MAX_WALLTIME", "2s")
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_STRICT", "")
	team := OpenShellTeam{Schema: 1, ID: "wall-time", Tasks: []OpenShellTask{
		fakeOpenShellTask("first", "nim", `{"hang":true}`, "all", "a.txt"),
		fakeOpenShellTask("queued", "nim", `{"write":{"b.txt":"late\n"}}`, "all", "b.txt"),
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	started := time.Now()
	run, err := r.RunTeam(ctx, team)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), 6*time.Second)
	require.Len(t, run.Tasks, 2)
	assert.Nil(t, run.Integrated)
	assert.FileExists(t, filepath.Join(run.Tasks[0].State, "cleaned"))
	assert.Empty(t, run.Tasks[1].State)
	assert.Contains(t, run.Tasks[1].Error, "not started")
}

func TestOpenShellBudgetKeepsRootStartAndTighterLimit(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_STRICT", "")
	started := time.Now().Add(-2 * time.Second)
	budget := &Budget{StartedAt: started, MaxWallMs: 10000}
	for _, tc := range []struct {
		setting string
		want    time.Duration
	}{{"", 10 * time.Second}, {"1h", 10 * time.Second}, {"5s", 5 * time.Second}} {
		t.Run(tc.setting, func(t *testing.T) {
			t.Setenv("CAPTAIN_MAX_WALLTIME", tc.setting)
			ctx, cancel, err := OpenShellBudgetContext(context.Background(), budget)
			require.NoError(t, err)
			defer cancel()
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			assert.Equal(t, started.Add(tc.want), deadline)
		})
	}
	for _, setting := range []string{"invalid", "-1s", "1ns"} {
		t.Setenv("CAPTAIN_MAX_WALLTIME", setting)
		_, _, err := OpenShellBudgetContext(context.Background(), nil)
		require.ErrorContains(t, err, "CAPTAIN_MAX_WALLTIME")
	}
}

func TestOpenShellWorkerTimeoutIsNotASavedDeadline(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_STRICT", "")
	for _, tc := range []struct {
		setting string
		want    time.Duration
	}{{"", 0}, {"2h", 2 * time.Hour}} {
		t.Run("walltime "+tc.setting, func(t *testing.T) {
			t.Setenv("CAPTAIN_MAX_WALLTIME", tc.setting)
			started := time.Now()
			ctx, cancel, err := OpenShellBudgetContext(context.Background(), nil)
			require.NoError(t, err)
			defer cancel()
			// RunOpenShellWorkflow bounds each dispatch by the worker timeout.
			ctx, stop := context.WithTimeout(ctx, time.Minute)
			defer stop()
			r := &OpenShellRunner{}
			_, done := r.deadlineContext(ctx)
			defer done()
			if tc.want == 0 {
				assert.True(t, r.DeadlineAt.IsZero(), "no wall-time limit: the plan saves no deadline")
			} else {
				assert.WithinDuration(t, started.Add(tc.want), r.DeadlineAt, 5*time.Second)
			}
		})
	}
}

func TestOpenShellSequenceDeadlineSurvivesResume(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_WALLTIME", "1m")
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_STRICT", "")
	r := pauseOpenShellSequence(t)
	var before OpenShellRun
	_, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &before)
	require.NoError(t, err)
	require.False(t, before.DeadlineAt.IsZero())
	t.Setenv("CAPTAIN_MAX_WALLTIME", "")
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Export)
	var after, second OpenShellRun
	_, err = decodeOpenShellSequence(result.Export.RunRecord, &after)
	require.NoError(t, err)
	_, err = decodeOpenShellSequence(filepath.Join(r.RunDir, "stage-2", "run.json"), &second)
	require.NoError(t, err)
	assert.True(t, before.DeadlineAt.Equal(after.DeadlineAt))
	assert.True(t, before.DeadlineAt.Equal(second.DeadlineAt))
}

func TestOpenShellSequenceExpiredBudgetRefusesRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	t.Setenv("CAPTAIN_MAX_WALLTIME", "10s")
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_STRICT", "")
	r := pauseOpenShellSequence(t)
	var run OpenShellRun
	before, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &run)
	require.NoError(t, err)
	require.False(t, run.DeadlineAt.IsZero())
	t.Setenv("CAPTAIN_MAX_WALLTIME", "1h")
	<-time.After(time.Until(run.DeadlineAt))
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, result.Export)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
	after, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	lock, err := lockOpenShellSequence(r.RunDir, false)
	require.NoError(t, err)
	require.NoError(t, lock.Close())
}
