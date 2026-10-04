package captaincode

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// interruptOpenShellStage2 cancels a two-stage sequence during stage 2: as
// stage 2 is dispatched, or once its worker has started when hang is set.
func interruptOpenShellStage2(t *testing.T, teams []OpenShellTeam, hang bool) *OpenShellRunner {
	t.Helper()
	r := newFakeOpenShell(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if hang {
		go func() {
			for ctx.Err() == nil {
				if started, _ := filepath.Glob(filepath.Join(r.StateRoot, "cc-os-*", "started")); len(started) > 0 {
					cancel()
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
	} else {
		r.Log = func(format string, args ...any) {
			if strings.HasPrefix(fmt.Sprintf(format, args...), "stage 2/2: snapshot ") {
				cancel()
			}
		}
	}
	_, err := runOpenShellSequence(ctx, r, teams, nil)
	require.ErrorIs(t, err, ErrInterrupted)
	return r
}

func savedOpenShellSequence(t *testing.T, r *OpenShellRunner) OpenShellRun {
	t.Helper()
	var run OpenShellRun
	_, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &run)
	require.NoError(t, err)
	return run
}

func TestOpenShellSequenceRerunsAStageACancellationStopped(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_MAX_WALLTIME", "")
	teams := resumeSequenceTeams()
	teams[1].Tasks[0].Prompt = fmt.Sprintf(`{"hang_once":%q,"expect":{"a.txt":"first\n"},"write":{"a.txt":"second\n"}}`,
		filepath.Join(t.TempDir(), "hung"))
	r := interruptOpenShellStage2(t, teams, true)
	stopped := savedOpenShellSequence(t, r)
	require.Len(t, stopped.Stages, 2)
	require.Equal(t, "interrupted", stopped.Stages[1].Verdict)

	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Export)
	assert.Equal(t, &OpenShellAttemptUsage{Workers: 3}, result.OpenShellAttempts, "the stopped worker still counts")

	run := savedOpenShellSequence(t, r)
	require.Len(t, run.Stages, 2)
	require.Len(t, run.SetAside, 1)
	aside := run.SetAside[0]
	assert.Equal(t, 2, aside.Stage)
	assert.Equal(t, filepath.Join(r.RunDir, "stage-2", "run.json"), aside.RunRecord)
	assert.Equal(t, run.Stages[1].Revision, aside.Revision)
	assert.Equal(t, &OpenShellAttemptUsage{Workers: 1}, aside.Attempts)
	assert.Equal(t, filepath.Join(r.RunDir, "stage-2-rerun-1", "run.json"), run.Stages[1].RunRecord)
	assert.Equal(t, "pass", run.Stages[1].Verdict)
	assert.DirExists(t, filepath.Join(r.RunDir, "stage-2"), "the stopped run stays for inspection")

	again, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err, "a finished sequence with a set-aside run still replays its export")
	require.NotNil(t, again.Export)
	assert.Equal(t, result.OpenShellAttempts, again.OpenShellAttempts)

	run.SetAside[0].RunRecord = filepath.Join(r.RunDir, "stage-1", "run.json")
	require.NoError(t, saveOpenShellSequence(r.RunDir, &run))
	_, err = ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.ErrorContains(t, err, "invalid set-aside stage record")
}

func TestOpenShellSequenceStageRefusedBeforeDispatchIsNotSetAside(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_MAX_WALLTIME", "")
	r := interruptOpenShellStage2(t, resumeSequenceTeams(), false)
	stopped := savedOpenShellSequence(t, r)
	require.Len(t, stopped.Stages, 1, "no worker started, so stage 2 never began")
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Export)
	assert.Equal(t, &OpenShellAttemptUsage{Workers: 2}, result.OpenShellAttempts)
	run := savedOpenShellSequence(t, r)
	assert.Empty(t, run.SetAside)
	assert.Equal(t, filepath.Join(r.RunDir, "stage-2", "run.json"), run.Stages[1].RunRecord)
}

func TestOpenShellSequenceRerunsAStageAfterACrash(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	t.Setenv("CAPTAIN_MAX_WALLTIME", "")
	teams := resumeSequenceTeams()
	teams[1].Tasks[0].Prompt = fmt.Sprintf(`{"hang_once":%q,"expect":{"a.txt":"first\n"},"write":{"a.txt":"second\n"}}`,
		filepath.Join(t.TempDir(), "hung"))
	r := interruptOpenShellStage2(t, teams, true)
	run := savedOpenShellSequence(t, r)
	require.Len(t, run.Stages, 2)
	crashed := run.Stages[1].RunRecord
	// What a controller that died mid-stage leaves: the checkpoint still says
	// running, and that record is not a verified export.
	run.Stages[1].Verdict = "running"
	require.NoError(t, saveOpenShellSequence(r.RunDir, &run))
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err, result.Text)
	require.NotNil(t, result.Export)
	again := savedOpenShellSequence(t, r)
	require.NotEmpty(t, again.SetAside)
	assert.Equal(t, crashed, again.SetAside[0].RunRecord, "the crashed run is kept and not replayed as the verified stage")
	assert.NotEqual(t, crashed, again.Stages[len(again.Stages)-1].RunRecord)
}

func TestOpenShellRerunMustFitTheAttemptCap(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "2")
	t.Setenv("CAPTAIN_MAX_WALLTIME", "")
	teams := resumeSequenceTeams()
	teams[1].Tasks[0].Prompt = `{"hang":true}`
	r := interruptOpenShellStage2(t, teams, true)
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.ErrorIs(t, err, ErrOpenShellAttemptCap)
	assert.ErrorContains(t, err, "needs 1 more attempt slots after 2 used")
	assert.Nil(t, result.Export)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2-rerun-1"))
	assert.Empty(t, savedOpenShellSequence(t, r).SetAside, "a refused recovery records nothing")
}

func TestOpenShellRerunFitsCountsEverySetAsideRun(t *testing.T) {
	ctx := context.Background()
	teams := resumeSequenceTeams()
	stage1 := []*OpenShellRun{{AttemptUsage: &OpenShellAttemptUsage{Workers: 1}}}
	one := func() *OpenShellAttemptUsage { return &OpenShellAttemptUsage{Workers: 1} }
	r := &OpenShellRunner{MaxAttempts: 3}
	require.NoError(t, r.rerunFits(ctx, teams, stage1, nil, &OpenShellSetAside{Attempts: one()}))
	err := r.rerunFits(ctx, teams, stage1, []OpenShellSetAside{{Attempts: one()}}, &OpenShellSetAside{Attempts: one()})
	require.ErrorIs(t, err, ErrOpenShellAttemptCap, "every earlier set-aside run counts")
	err = r.rerunFits(ctx, teams, stage1, nil, &OpenShellSetAside{})
	require.ErrorContains(t, err, "(1 unknown)", "an unknown count cannot be shown to fit")
	r.MaxAttempts = 0
	require.NoError(t, r.rerunFits(ctx, teams, stage1, nil, &OpenShellSetAside{}), "without a cap, unknown usage is reported, not refused")
}

func TestOpenShellSpendCountsSetAsideRuns(t *testing.T) {
	run := OpenShellRun{Tasks: []*OpenShellResult{spendWorker(2, 2, 0.5)},
		SetAside: []OpenShellSetAside{{Requests: 1, Tokens: 115, CostUSD: 0.25, Priced: true}}}
	tokens, cost, complete := run.Spend()
	assert.Equal(t, 335, tokens)
	assert.InDelta(t, 0.75, cost, 1e-9)
	assert.True(t, complete)
	run.SetAside[0].Priced = false
	_, _, complete = run.Spend()
	assert.False(t, complete, "an unpriced set-aside run leaves the bill unknown")
}
