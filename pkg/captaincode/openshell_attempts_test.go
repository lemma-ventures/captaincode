package captaincode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenShellAttemptCapAdmitsBoundedWorker(t *testing.T) {
	r := newFakeOpenShell(t)
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "2")
	task := fakeOpenShellTask("worker", "nim", `{"write":{"a.txt":"changed\n"}}`, "all", "a.txt")
	task.RepairAttempts = 1
	run, err := r.RunTeam(context.Background(), OpenShellTeam{Schema: 1, ID: "bounded", Tasks: []OpenShellTask{task}})
	require.NoError(t, err)
	assert.Equal(t, "pass", run.Verdict)
	data, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
	require.NoError(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(data, &record))
	assert.Equal(t, map[string]any{"limit": float64(2), "required": float64(2)}, record["attempt_budget"])
}

func TestOpenShellAttemptCapRefusesWholeSequenceBeforeDispatch(t *testing.T) {
	r := newFakeOpenShell(t)
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "1")
	result, err := runOpenShellSequence(context.Background(), r, resumeSequenceTeams(), nil)
	require.ErrorContains(t, err, "plan requires 2 attempt slots; cap is 1")
	assert.Nil(t, result.Export)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-1"))
	assert.NoFileExists(t, filepath.Join(r.RunDir, "sequence.json"))
	var run OpenShellRun
	_, readErr := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &run)
	require.NoError(t, readErr)
	assert.Equal(t, &OpenShellAttemptBudget{Limit: 1, Required: 2}, run.AttemptBudget)
	assert.Contains(t, result.Text, "2 worst-case slots / 1 cap")
}

func TestOpenShellAttemptCapIncludesDirectorRetry(t *testing.T) {
	r := newFakeOpenShell(t)
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "3")
	logs := fakeClaude(t, "retry")
	r.Director, r.DirectorName = ToolLessClaudeDirector, "claude"
	team := OpenShellTeam{Schema: 1, ID: "overlap", Tasks: []OpenShellTask{
		fakeOpenShellTask("t2", "nim", `{"write":{"a.txt":"one\n"}}`, "all", "a.txt"),
		fakeOpenShellTask("t3", "nim", `{"write":{"a.txt":"two\n"}}`, "all", "a.txt"),
	}}
	_, err := r.RunTeam(context.Background(), team)
	require.ErrorContains(t, err, "plan requires 4 attempt slots; cap is 3")
	assert.NoDirExists(t, filepath.Join(r.RunDir, "tasks"))
	assert.NoFileExists(t, filepath.Join(logs, "call1"))
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "4")
	r.RunDir = filepath.Join(filepath.Dir(r.RunDir), "accepted")
	require.NoError(t, os.Mkdir(r.RunDir, 0o700))
	run, err := r.RunTeam(context.Background(), team)
	require.NoError(t, err)
	assert.Equal(t, "pass", run.Verdict)
	assert.FileExists(t, filepath.Join(logs, "call2"))
	assert.NoFileExists(t, filepath.Join(logs, "call3"))
}

func TestOpenShellAttemptCapSurvivesRecovery(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "2")
	r := pauseOpenShellSequence(t)
	before, err := os.ReadFile(filepath.Join(r.RunDir, "stage-1", "run.json"))
	require.NoError(t, err)
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "1")
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.ErrorContains(t, err, "plan requires 2 attempt slots; cap is 1")
	assert.Nil(t, result.Export)
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "")
	result, err = ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Export)
	after, err := os.ReadFile(filepath.Join(r.RunDir, "stage-1", "run.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	data, err := os.ReadFile(result.Export.RunRecord)
	require.NoError(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(data, &record))
	assert.Equal(t, map[string]any{"limit": float64(2), "required": float64(2)}, record["attempt_budget"])
}

func TestOpenShellAttemptAdmissionCountsEveryStageAndRepair(t *testing.T) {
	teams := resumeSequenceTeams()
	teams[0].Tasks[0].RepairAttempts = 1
	teams[1].Tasks[0].Mode, teams[1].Tasks[0].Allowed = "review", nil
	r := &OpenShellRunner{Director: ToolLessClaudeDirector}
	budget, err := r.attemptBudget(context.Background(), teams)
	require.NoError(t, err)
	assert.Equal(t, 3, budget.Required)
	teams[0].Tasks = append(teams[0].Tasks, fakeOpenShellTask("other", "nim", "fix b", "all", "b.txt"))
	budget, err = r.attemptBudget(context.Background(), teams)
	require.NoError(t, err)
	assert.Equal(t, 6, budget.Required)
	teams[0].Tasks = append(teams[0].Tasks,
		fakeOpenShellTask("third", "nim", "fix c", "all", "c.txt"),
		fakeOpenShellTask("fourth", "nim", "fix d", "all", "d.txt"))
	budget, err = r.attemptBudget(context.Background(), teams)
	require.NoError(t, err)
	assert.Equal(t, 10, budget.Required)
}

func TestOpenShellAttemptAdmissionValidatesRootAndEnvironment(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "2")
	root := NewBudget("task", "test", BudgetOpts{MaxAttempts: 1})
	ctx, stop, err := OpenShellBudgetContext(context.Background(), root)
	require.NoError(t, err)
	defer stop()
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "0")
	ctx, stopNested, err := OpenShellBudgetContext(ctx, nil)
	require.NoError(t, err)
	defer stopNested()
	_, err = (&OpenShellRunner{}).attemptBudget(ctx, resumeSequenceTeams())
	require.ErrorIs(t, err, ErrOpenShellAttemptCap)
	root.ReservedAttempts = 1
	_, _, err = OpenShellBudgetContext(context.Background(), root)
	require.ErrorContains(t, err, "unused root attempt budget")
	for _, raw := range []string{"-1", "bad", "1.5", "9999999999999999999999999"} {
		t.Setenv("CAPTAIN_MAX_ATTEMPTS", raw)
		_, _, err := OpenShellBudgetContext(context.Background(), nil)
		require.ErrorContains(t, err, "CAPTAIN_MAX_ATTEMPTS", raw)
	}
}

func TestOpenShellAttemptRecoveryRejectsAlteredAdmission(t *testing.T) {
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "2")
	r := pauseOpenShellSequence(t)
	var run OpenShellRun
	_, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &run)
	require.NoError(t, err)
	require.NotNil(t, run.AttemptBudget)
	run.AttemptBudget.Limit = 20
	require.NoError(t, saveOpenShellSequence(r.RunDir, &run))
	_, err = ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.ErrorContains(t, err, "attempt admission does not match saved plan")
	assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
}
