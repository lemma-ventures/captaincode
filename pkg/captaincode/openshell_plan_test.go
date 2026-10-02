package captaincode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpenShellPlanner answers like the tool-less director, counting each call
// where toolLessClaude does, and records the prompts it was sent.
func fakeOpenShellPlanner(replies ...string) (func(context.Context, string) (string, error), *[]string) {
	var prompts []string
	return func(ctx context.Context, prompt string) (string, error) {
		if attempts, ok := ctx.Value(openShellDirectorAttemptsKey{}).(*openShellDirectorAttempts); ok {
			attempts.count++
		}
		prompts = append(prompts, prompt)
		if len(replies) == 0 {
			return "", errors.New("director unavailable")
		}
		reply := replies[0]
		replies = replies[1:]
		return reply, nil
	}, &prompts
}

func openShellPlanReply(t *testing.T, rationale string, briefs ...string) string {
	t.Helper()
	workers := make([]map[string]string, len(briefs))
	for i, brief := range briefs {
		workers[i] = map[string]string{"brief": brief}
	}
	data, err := json.Marshal(map[string]any{"rationale": rationale, "workers": workers})
	require.NoError(t, err)
	return string(data)
}

func openShellPlanEnv(t *testing.T) *OpenShellRunner {
	t.Helper()
	r := openShellLegEnv(t)
	t.Setenv("CAPTAIN_OPENSHELL_ALLOWED", "a.txt,b.txt")
	t.Setenv("CAPTAIN_OPENSHELL_DIRECTOR", "claude")
	return r
}

func TestOpenShellTeamPlanSplitsTaskIntoSandboxAssignments(t *testing.T) {
	r := openShellPlanEnv(t)
	history := "[system]\nkeep the public API stable\n\n"
	ask, prompts := fakeOpenShellPlanner(openShellPlanReply(t, "two files,\ntwo workers", "update a.txt", "update b.txt", "  update   a.txt "))
	plan, err := planOpenShellTeam(context.Background(), r.Repo, "  update a.txt and b.txt  ", history, ask)
	require.NoError(t, err)
	assert.Equal(t, 1, plan.DirectorAttempts)
	assert.Equal(t, "two files, two workers", plan.Rationale)
	require.Len(t, *prompts, 1)
	sent := (*prompts)[0]
	assert.True(t, strings.HasPrefix(sent, directorConstraint), "the planner is told it has no tools")
	assert.Contains(t, sent, "update a.txt and b.txt")
	assert.Contains(t, sent, "a.txt, b.txt")
	assert.NotContains(t, sent, "keep the public API stable", "the director plans from the task, not the conversation")

	require.Len(t, plan.Workflow.Stages, 1)
	legs := plan.Workflow.Stages[0].Legs
	require.Len(t, legs, 2, "an identical assignment is planned once")
	assert.Equal(t, []string{"update a.txt", "update b.txt"}, plan.Assignments)
	for i, brief := range []string{"update a.txt", "update b.txt"} {
		assert.Equal(t, LegOpenShell, legs[i].Leg)
		assert.Empty(t, legs[i].Gate)
		assert.Contains(t, legs[i].Prompt, []string{"Sandbox worker 1 of 2", "Sandbox worker 2 of 2"}[i])
		assert.Contains(t, legs[i].Prompt, "Team task:\nupdate a.txt and b.txt\n")
		assert.Contains(t, legs[i].Prompt, "Your assignment:\n"+brief+"\n")
		assert.Contains(t, legs[i].Prompt, "the other workers cover the rest")
	}
	_, template, err := openShellConfig(context.Background(), r.Repo, "check")
	require.NoError(t, err)
	team, err := openShellWorkflowTeam(template, plan.Workflow, history)
	require.NoError(t, err)
	require.Len(t, team.Tasks, 2)
	for _, task := range team.Tasks {
		assert.Empty(t, task.Mode, "a planned worker edits")
		assert.Equal(t, []string{"a.txt", "b.txt"}, task.Allowed, "the plan cannot widen the configured scope")
		assert.True(t, strings.HasPrefix(task.Prompt, history), "workers still receive the conversation")
	}
}

func TestOpenShellTeamPlanRetriesOnceAndCountsEveryCall(t *testing.T) {
	r := openShellPlanEnv(t)
	ask, prompts := fakeOpenShellPlanner("Let me think about how to split this.", openShellPlanReply(t, "small task", "fix a.txt"))
	plan, err := planOpenShellTeam(context.Background(), r.Repo, "fix a.txt", "", ask)
	require.NoError(t, err)
	assert.Equal(t, 2, plan.DirectorAttempts)
	require.Len(t, *prompts, 2)
	assert.Contains(t, (*prompts)[1], "did not contain valid JSON")
	legs := plan.Workflow.Stages[0].Legs
	require.Len(t, legs, 1)
	assert.Contains(t, legs[0].Prompt, "Sandbox worker 1 of 1")
	assert.NotContains(t, legs[0].Prompt, "other workers cover the rest", "a single worker owns the whole task")

	ask, _ = fakeOpenShellPlanner("no plan", "still no plan")
	plan, err = planOpenShellTeam(context.Background(), r.Repo, "fix a.txt", "", ask)
	require.ErrorContains(t, err, "no valid JSON")
	assert.Equal(t, 2, plan.DirectorAttempts, "a failed plan still counts its calls")

	ask, _ = fakeOpenShellPlanner()
	plan, err = planOpenShellTeam(context.Background(), r.Repo, "fix a.txt", "", ask)
	require.ErrorContains(t, err, "director unavailable")
	assert.Equal(t, 1, plan.DirectorAttempts)
}

func TestOpenShellTeamPlanRefusesBeforeAnyModelCall(t *testing.T) {
	for _, tc := range []struct {
		name, task, history, want string
		env                       map[string]string
		cap                       int
	}{
		{name: "no conflict director", task: "fix a.txt", env: map[string]string{"CAPTAIN_OPENSHELL_DIRECTOR": ""}, want: "CAPTAIN_OPENSHELL_DIRECTOR=claude"},
		{name: "no editable scope", task: "fix a.txt", env: map[string]string{"CAPTAIN_OPENSHELL_ALLOWED": ""}, want: "CAPTAIN_OPENSHELL_ALLOWED"},
		{name: "no verification", task: "fix a.txt", env: map[string]string{"CAPTAIN_OPENSHELL_VERIFY": ""}, want: "CAPTAIN_OPENSHELL_VERIFY"},
		{name: "empty task", task: "  ", want: "needs a task"},
		{name: "cap below plan and one worker", task: "fix a.txt", cap: 2, want: "attempt cap exceeded"},
		{name: "conversation fills the prompt", task: "fix a.txt", history: strings.Repeat("x", 16000), want: "no room"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := openShellPlanEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			ctx := context.Background()
			if tc.cap > 0 {
				ctx = context.WithValue(ctx, openShellAttemptLimitKey{}, tc.cap)
			}
			ask, prompts := fakeOpenShellPlanner(openShellPlanReply(t, "", "fix a.txt"))
			plan, err := planOpenShellTeam(ctx, r.Repo, tc.task, tc.history, ask)
			require.ErrorContains(t, err, tc.want)
			assert.Empty(t, *prompts)
			assert.Zero(t, plan.DirectorAttempts)
			if tc.cap > 0 {
				assert.ErrorIs(t, err, ErrOpenShellAttemptCap)
			}
		})
	}
}

func TestOpenShellTeamPlanRejectsUnusablePlans(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
	}{
		{"no workers", `{"rationale":"nothing to do","workers":[]}`, "planned 0 workers"},
		{"too many workers", openShellPlanReply(t, "", "a", "b", "c", "d", "e"), "planned 5 workers"},
		{"empty assignment", openShellPlanReply(t, "", "fix a.txt", "  "), "empty or over"},
		{"oversized assignment", openShellPlanReply(t, "", strings.Repeat("y", openShellPlanBriefLimit+1)), "empty or over"},
		{"NUL in assignment", openShellPlanReply(t, "", "fix\x00a.txt"), "empty or over"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := openShellPlanEnv(t)
			ask, _ := fakeOpenShellPlanner(tc.reply)
			plan, err := planOpenShellTeam(context.Background(), r.Repo, "fix a.txt and b.txt", "", ask)
			require.ErrorContains(t, err, tc.want)
			assert.Empty(t, plan.Workflow.Stages)
			assert.Equal(t, 1, plan.DirectorAttempts)
		})
	}
}

func TestOpenShellTeamPlanCannotStartAReview(t *testing.T) {
	r := openShellPlanEnv(t)
	ask, _ := fakeOpenShellPlanner(openShellPlanReply(t, "", "--review inspect a.txt"))
	plan, err := planOpenShellTeam(context.Background(), r.Repo, "check a.txt", "", ask)
	require.NoError(t, err)
	_, template, err := openShellConfig(context.Background(), r.Repo, "check")
	require.NoError(t, err)
	team, err := openShellWorkflowTeam(template, plan.Workflow, "")
	require.NoError(t, err)
	require.Len(t, team.Tasks, 1)
	assert.Empty(t, team.Tasks[0].Mode)
	assert.Equal(t, []string{"a.txt", "b.txt"}, team.Tasks[0].Allowed)
}

func TestOpenShellAttemptsSpentLowerTheCap(t *testing.T) {
	ctx, err := WithOpenShellAttemptsSpent(context.Background(), 2)
	require.NoError(t, err)
	_, ok := ctx.Value(openShellAttemptLimitKey{}).(int)
	assert.False(t, ok, "no cap stays no cap")

	capped := context.WithValue(context.Background(), openShellAttemptLimitKey{}, 5)
	ctx, err = WithOpenShellAttemptsSpent(capped, 2)
	require.NoError(t, err)
	runner := &OpenShellRunner{}
	budget, err := runner.attemptBudget(ctx, []OpenShellTeam{{Schema: 1, ID: "t", Tasks: []OpenShellTask{
		fakeOpenShellTask("w1", "cerebras", "{}", "pass", "a.txt"),
		fakeOpenShellTask("w2", "cerebras", "{}", "pass", "b.txt"),
	}}})
	require.NoError(t, err)
	assert.Equal(t, 3, budget.Limit)
	assert.Equal(t, 2, budget.Required)

	_, err = WithOpenShellAttemptsSpent(capped, 5)
	require.ErrorIs(t, err, ErrOpenShellAttemptCap, "a spent cap must not read as no cap")
}

func TestOpenShellPlannedTeamExportsEveryAssignment(t *testing.T) {
	r := openShellPlanEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPTAIN_OPENSHELL_CONCURRENCY", "2")
	for _, key := range []string{"CAPTAIN_MAX_ATTEMPTS", "CAPTAIN_MAX_WALLTIME", "CAPTAIN_MAX_COST", "CAPTAIN_STRICT"} {
		t.Setenv(key, "")
	}
	index, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	ask, _ := fakeOpenShellPlanner(openShellPlanReply(t, "one file each",
		`{"write":{"a.txt":"first\n"}}`, `{"write":{"b.txt":"second\n"}}`))
	plan, err := planOpenShellTeam(context.Background(), r.Repo, "write a.txt and b.txt", "", ask)
	require.NoError(t, err)
	res, err := (Workspace{Dir: r.Repo}).RunOpenShellWorkflow(context.Background(), plan.Workflow, "")
	require.NoError(t, err, res.Text)
	require.NotNil(t, res.Export)
	assert.Equal(t, []string{"a.txt", "b.txt"}, res.Export.Manifest.ChangedFiles)
	assert.Contains(t, res.Text, "2/2 task(s) passed")
	require.NotNil(t, res.OpenShellAttempts)
	assert.Equal(t, 2, res.OpenShellAttempts.Workers)
	data, err := os.ReadFile(res.Export.RunRecord)
	require.NoError(t, err)
	var run OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	log, err := os.ReadFile(filepath.Join(run.Integrated.Evidence, "baseline-verify.log"))
	require.NoError(t, err)
	assert.Contains(t, string(log), "a.txt=first\n")
	assert.Contains(t, string(log), "b.txt=second\n")
	afterIndex, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, index, afterIndex)
}

func TestOpenShellPlanRecordIsKeptOnceOnARunningSandboxAttempt(t *testing.T) {
	l := &Ledger{}
	l.RecordAttemptState(AttemptState{TaskID: "t", AttemptID: "a", Leg: LegOpenShell, State: StateRunning})
	l.RecordAttemptState(AttemptState{TaskID: "t", AttemptID: "host", Leg: LegClaude, State: StateRunning})
	plan := OpenShellTeamPlan{Rationale: "split", Assignments: []string{"edit a.txt", "edit b.txt"}, DirectorAttempts: 1}
	record := plan.Record()
	plan.Assignments[0] = "mutated"
	require.NoError(t, l.RecordOpenShellPlan("a", record))
	record.Assignments[1] = "mutated"
	kept := l.AttemptStateFor("a").OpenShellPlan
	require.NotNil(t, kept)
	assert.Equal(t, []string{"edit a.txt", "edit b.txt"}, kept.Assignments, "the ledger keeps its own copy")
	require.ErrorContains(t, l.RecordOpenShellPlan("a", OpenShellPlanRecord{Assignments: []string{"other"}}), "already has a team plan")
	require.ErrorContains(t, l.RecordOpenShellPlan("host", OpenShellPlanRecord{Assignments: []string{"x"}}), "running sandbox attempt")
	for _, bad := range []OpenShellPlanRecord{{}, {Assignments: []string{"a", "b", "c", "d", "e"}}, {Assignments: []string{"a"}, DirectorAttempts: 3}} {
		require.ErrorContains(t, l.RecordOpenShellPlan("a", bad), "invalid team plan")
	}
}
