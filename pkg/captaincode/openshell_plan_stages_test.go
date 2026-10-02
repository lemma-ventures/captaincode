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

func TestOpenShellPlannedStagesVerifyHandoffsAndExportOnePatch(t *testing.T) {
	r := openShellPlanEnv(t)
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{"CAPTAIN_MAX_ATTEMPTS", "CAPTAIN_MAX_WALLTIME", "CAPTAIN_MAX_COST", "CAPTAIN_STRICT"} {
		t.Setenv(key, "")
	}
	before, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	ask, _ := fakeOpenShellPlanner(`{"rationale":"edit, inspect the verified tree, then finish","stages":[
		{"mode":"edit","workers":[{"brief":"{\"write\":{\"a.txt\":\"first\\n\"}}"}]},
		{"mode":"review","workers":[{"brief":"{\"expect\":{\"a.txt\":\"first\\n\"}}"}]},
		{"mode":"edit","workers":[{"brief":"{\"expect\":{\"a.txt\":\"first\\n\"},\"write\":{\"a.txt\":\"final\\n\"}}"}]}
	]}`)
	plan, err := planOpenShellTeam(context.Background(), r.Repo, "improve a.txt in verified stages", "", ask)
	require.NoError(t, err)
	require.Len(t, plan.Workflow.Stages, 3)
	assert.Equal(t, 1, plan.DirectorAttempts)
	assert.True(t, isOpenShellReview(plan.Workflow.Stages[1].Legs[0].Prompt))
	res, err := (Workspace{Dir: r.Repo}).RunOpenShellWorkflow(context.Background(), plan.Workflow, "")
	require.NoError(t, err, res.Text)
	require.NotNil(t, res.Export)
	var run OpenShellRun
	data, err := os.ReadFile(res.Export.RunRecord)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &run))
	require.Len(t, run.Stages, 3)
	assert.Equal(t, run.Stages[0].Tree, run.Stages[1].Tree)
	assert.Equal(t, run.Stages[0].NextRevision, run.Stages[1].Revision)
	assert.Equal(t, run.Stages[1].NextRevision, run.Stages[2].Revision)
	assert.Equal(t, OpenShellUnchanged, run.Tasks[1].Outcome)
	assert.Equal(t, 3, res.OpenShellAttempts.Workers)
	patch, err := os.ReadFile(res.Export.Manifest.DiffPath)
	require.NoError(t, err)
	assert.Contains(t, string(patch), "+final")
	assert.NotContains(t, string(patch), "+first")
	after, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	content, err := os.ReadFile(filepath.Join(r.Repo, "a.txt"))
	require.NoError(t, err)
	assert.NotContains(t, string(content), "final")
}

func TestOpenShellStagePlansRefuseInvalidBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, reply, want string }{
		{"ambiguous", `{"workers":[{"brief":"edit a"}],"stages":[{"workers":[{"brief":"edit b"}]}]}`, "not both"},
		{"no stages", `{"stages":[]}`, "1-4 stages"},
		{"host mode", `{"stages":[{"mode":"host","workers":[{"brief":"edit a"}]}]}`, "mode must be"},
		{"no workers", `{"stages":[{"workers":[]}]}`, "planned 0 workers"},
		{"too many stages", `{"stages":[{},{},{},{},{}]}`, "1-4 stages"},
		{"total cap", `{"stages":[{"workers":[{"brief":"a"},{"brief":"b"},{"brief":"c"},{"brief":"d"}]},{"workers":[{"brief":"e"},{"brief":"f"},{"brief":"g"},{"brief":"h"}]},{"workers":[{"brief":"i"}]}]}`, "at most 8 workers"},
		{"empty brief", `{"stages":[{"workers":[{"brief":" "}]}]}`, "empty or over"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := openShellPlanEnv(t)
			ask, prompts := fakeOpenShellPlanner(tc.reply)
			plan, err := planOpenShellTeam(context.Background(), r.Repo, "edit a and b", "", ask)
			require.ErrorContains(t, err, tc.want)
			assert.Empty(t, plan.Workflow.Stages)
			assert.Equal(t, 1, plan.DirectorAttempts)
			assert.Len(t, *prompts, 1)
		})
	}
}

func TestOpenShellPlannedStagesPreserveScopeAndDedupeWithinEachStage(t *testing.T) {
	r := openShellPlanEnv(t)
	ask, _ := fakeOpenShellPlanner(`{"stages":[
		{"mode":"edit","workers":[{"brief":"edit a"},{"brief":" edit   a "}]},
		{"mode":"review","workers":[{"brief":"inspect a"}]},
		{"workers":[{"brief":"edit a"}]}
	]}`)
	plan, err := planOpenShellTeam(context.Background(), r.Repo, "update a", "[user]\nhistory\n", ask)
	require.NoError(t, err)
	assert.Equal(t, []string{"edit a", "inspect a", "edit a"}, plan.Assignments)
	require.Len(t, plan.Stages, 3)
	_, template, err := openShellConfig(context.Background(), r.Repo, "check")
	require.NoError(t, err)
	for i, stage := range plan.Workflow.Stages {
		team, err := openShellWorkflowTeam(template, Workflow{Stages: []WorkflowStage{stage}}, "[user]\nhistory\n")
		require.NoError(t, err)
		require.Len(t, team.Tasks, 1)
		worker := team.Tasks[0]
		assert.Equal(t, template.Tasks[0].Profile, worker.Profile)
		assert.Equal(t, template.Tasks[0].Verify, worker.Verify)
		assert.Equal(t, template.Tasks[0].Protected, worker.Protected)
		assert.Contains(t, worker.Prompt, "history")
		if i == 1 {
			assert.Equal(t, "review", worker.Mode)
			assert.Empty(t, worker.Allowed)
			assert.Zero(t, worker.RepairAttempts)
			assert.Equal(t, "pass", worker.Baseline)
		} else {
			assert.Empty(t, worker.Mode)
			assert.Equal(t, template.Tasks[0].Allowed, worker.Allowed)
		}
	}
}

func TestOpenShellPlannedStagesRefuseOversizedBudgetBeforeDispatch(t *testing.T) {
	r := openShellPlanEnv(t)
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{"CAPTAIN_MAX_WALLTIME", "CAPTAIN_MAX_COST", "CAPTAIN_STRICT"} {
		t.Setenv(key, "")
	}
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "3")
	ctx, cancel, err := OpenShellBudgetContext(context.Background(), nil)
	require.NoError(t, err)
	defer cancel()
	ask, _ := fakeOpenShellPlanner(`{"stages":[{"workers":[{"brief":"edit a"},{"brief":"edit b"}]},{"mode":"review","workers":[{"brief":"inspect a and b"}]}]}`)
	plan, err := planOpenShellTeam(ctx, r.Repo, "edit then review", "", ask)
	require.NoError(t, err)
	ctx, err = WithOpenShellAttemptsSpent(ctx, plan.DirectorAttempts)
	require.NoError(t, err)
	res, err := (Workspace{Dir: r.Repo}).RunOpenShellWorkflow(ctx, plan.Workflow, "")
	require.ErrorIs(t, err, ErrOpenShellAttemptCap)
	assert.Nil(t, res.Export)
	assert.Empty(t, openShellStates(t, r))
}

func TestOpenShellPlannedReviewFailureWithholdsExportAndLaterStages(t *testing.T) {
	r := openShellPlanEnv(t)
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{"CAPTAIN_MAX_ATTEMPTS", "CAPTAIN_MAX_WALLTIME", "CAPTAIN_MAX_COST", "CAPTAIN_STRICT"} {
		t.Setenv(key, "")
	}
	ask, _ := fakeOpenShellPlanner(`{"stages":[
		{"workers":[{"brief":"{\"write\":{\"a.txt\":\"first\\n\"}}"}]},
		{"mode":"review","workers":[{"brief":"{\"write\":{\"a.txt\":\"forbidden\\n\"}}"}]},
		{"workers":[{"brief":"{\"write\":{\"b.txt\":\"never\\n\"}}"}]}
	]}`)
	plan, err := planOpenShellTeam(context.Background(), r.Repo, "edit, review, edit", "", ask)
	require.NoError(t, err)
	var checkpoints []OpenShellCheckpoint
	ctx := WithOpenShellCheckpoint(context.Background(), func(c OpenShellCheckpoint) error {
		checkpoints = append(checkpoints, c)
		return nil
	})
	res, err := (Workspace{Dir: r.Repo}).RunOpenShellWorkflow(ctx, plan.Workflow, "")
	require.ErrorContains(t, err, "review changed")
	assert.Nil(t, res.Export)
	require.NotEmpty(t, checkpoints)
	assert.NoDirExists(t, filepath.Join(checkpoints[0].RunDir, "stage-3"))
	assert.Equal(t, 2, res.OpenShellAttempts.Workers)
}

func TestOpenShellStagePlanRecordCopiesAndChecksStageAssignments(t *testing.T) {
	plan := OpenShellTeamPlan{Assignments: []string{"edit a", "inspect a"}, DirectorAttempts: 1,
		Stages: []OpenShellPlanStage{{Mode: "edit", Assignments: []string{"edit a"}}, {Mode: "review", Assignments: []string{"inspect a"}}}}
	record := plan.Record()
	plan.Stages[0].Assignments[0] = "mutated"
	l := &Ledger{}
	l.RecordAttemptState(AttemptState{TaskID: "t", AttemptID: "a", Leg: LegOpenShell, State: StateRunning})
	require.NoError(t, l.RecordOpenShellPlan("a", record))
	record.Stages[1].Assignments[0] = "mutated"
	kept := l.AttemptStateFor("a").OpenShellPlan
	require.NotNil(t, kept)
	assert.Equal(t, "edit a", kept.Stages[0].Assignments[0])
	assert.Equal(t, "inspect a", kept.Stages[1].Assignments[0])
	require.NoError(t, kept.Validate())
	require.ErrorContains(t, record.Validate(), "invalid team plan")
	data, err := json.Marshal(kept)
	require.NoError(t, err)
	var reloaded OpenShellPlanRecord
	require.NoError(t, json.Unmarshal(data, &reloaded))
	require.NoError(t, reloaded.Validate())
	assert.Equal(t, *kept, reloaded)
}
