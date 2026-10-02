package captaincode

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strictOpenShellEnv(t *testing.T, limit string) {
	t.Helper()
	for _, key := range []string{"CAPTAIN_MAX_ATTEMPTS", "CAPTAIN_MAX_WALLTIME"} {
		t.Setenv(key, "")
	}
	t.Setenv("CAPTAIN_STRICT", "1")
	t.Setenv("CAPTAIN_MAX_COST", limit)
	// A kept, priced lane: the fake pilot has no catalog for the default.
	t.Setenv("CAPTAIN_OPENSHELL_PROFILE", "cerebras")
}

func TestOpenShellBudgetContextCarriesAStrictCostCap(t *testing.T) {
	strictOpenShellEnv(t, "0.5")
	t.Setenv("CAPTAIN_STRICT", "")
	ctx, cancel, err := OpenShellBudgetContext(context.Background(), nil)
	require.NoError(t, err)
	cancel()
	assert.Zero(t, OpenShellCostLimit(ctx), "without CAPTAIN_STRICT a dollar cap is a report column, as for host legs")

	t.Setenv("CAPTAIN_STRICT", "1")
	ctx, cancel, err = OpenShellBudgetContext(context.Background(), nil)
	require.NoError(t, err)
	defer cancel()
	assert.Equal(t, 0.5, OpenShellCostLimit(ctx))
	task := &Budget{StartedAt: time.Now().Add(-time.Second), Mode: BudgetStrict, MaxCostUSD: 0.2}
	nested, cancelNested, err := OpenShellBudgetContext(ctx, task)
	require.NoError(t, err)
	defer cancelNested()
	assert.Equal(t, 0.2, OpenShellCostLimit(nested), "the tighter of the environment and the task budget")
	outer, cancelOuter, err := OpenShellBudgetContext(nested, nil)
	require.NoError(t, err)
	defer cancelOuter()
	assert.Equal(t, 0.2, OpenShellCostLimit(outer), "a nested admission never loosens the cap")

	for _, raw := range []string{"NaN", "Inf", "-1", "abc", "1000"} {
		t.Setenv("CAPTAIN_MAX_COST", raw)
		_, _, err := OpenShellBudgetContext(context.Background(), nil)
		assert.ErrorContains(t, err, "CAPTAIN_MAX_COST", raw)
	}
	t.Setenv("CAPTAIN_MAX_COST", "")
	_, _, err = OpenShellBudgetContext(context.Background(), &Budget{StartedAt: time.Now(), Mode: BudgetStrict, MaxCostUSD: math.NaN()})
	assert.ErrorContains(t, err, "invalid strict cost budget")
}

// Admission accepts exactly the lanes the pilot prices.
func TestOpenShellPricedProfilesMatchThePilotCeilings(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "examples", "openshell-pilot", "profiles.py"))
	require.NoError(t, err)
	block := regexp.MustCompile(`(?s)\nCEILINGS = \{(.*?)\}`).FindSubmatch(source)
	require.NotNil(t, block)
	lanes := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z]+)":`).FindAllSubmatch(block[1], -1) {
		lanes[string(m[1])] = true
	}
	assert.Equal(t, lanes, openShellPricedProfiles)
}

func TestOpenShellCostBudgetSplitsTheCapAcrossEveryWorker(t *testing.T) {
	worker := func(id, profile, mode string) OpenShellTask {
		task := validOpenShellTask()
		task.ID, task.Profile, task.Mode = id, profile, mode
		return task
	}
	review := worker("r", "cerebras", "review")
	teams := []OpenShellTeam{{Tasks: []OpenShellTask{worker("a", "cerebras", ""), worker("b", "sambanova", "")}},
		{Tasks: []OpenShellTask{review}}}
	ctx := context.WithValue(context.Background(), openShellCostLimitKey{}, 0.5)
	r := &OpenShellRunner{}
	budget, err := r.costBudget(ctx, teams)
	require.NoError(t, err)
	assert.Equal(t, &OpenShellCostBudget{LimitUSD: 0.5, WorkerUSD: 0.166666, Workers: 3}, budget)
	assert.LessOrEqual(t, float64(budget.Workers)*budget.WorkerUSD, budget.LimitUSD, "the shares never add up to more than the cap")
	none, err := r.costBudget(context.Background(), teams)
	require.NoError(t, err)
	assert.Nil(t, none, "no strict cap, no allocation")

	_, err = r.costBudget(ctx, []OpenShellTeam{{Tasks: []OpenShellTask{worker("n", "nim", "")}}})
	assert.ErrorIs(t, err, ErrOpenShellCostCap)
	assert.ErrorContains(t, err, "profile nim returns no price")

	directed := &OpenShellRunner{Director: func(context.Context, string, map[string]Contender) (Ruling, error) {
		return Ruling{}, nil
	}}
	_, err = directed.costBudget(ctx, teams)
	assert.ErrorIs(t, err, ErrOpenShellCostCap)
	assert.ErrorContains(t, err, "conflict ruling is an unpriced host call")
	_, err = directed.costBudget(ctx, []OpenShellTeam{{Tasks: []OpenShellTask{worker("a", "cerebras", ""), review}}})
	assert.NoError(t, err, "one edit beside a review can never need a ruling")

	stage := &OpenShellRunner{MaxCostUSD: 0.5, WorkerCostUSD: 0.166666}
	kept, err := stage.costBudget(context.Background(), teams[1:])
	require.NoError(t, err)
	assert.Equal(t, &OpenShellCostBudget{LimitUSD: 0.5, WorkerUSD: 0.166666, Workers: 1}, kept, "a stage keeps the plan's share")

	_, err = r.costBudget(context.WithValue(context.Background(), openShellCostLimitKey{}, 1e-7), teams)
	assert.ErrorIs(t, err, ErrOpenShellCostCap, "a share below a micro-dollar admits nothing")
}

func TestOpenShellStrictCapReachesEachWorkersShieldAndTheRunRecord(t *testing.T) {
	r := openShellLegEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPTAIN_OPENSHELL_ALLOWED", "a.txt,b.txt")
	t.Setenv("CAPTAIN_OPENSHELL_CONCURRENCY", "2")
	strictOpenShellEnv(t, "0.5")
	wf, err := ParseOpenShellWorkflow(`/openshell {"write":{"a.txt":"first\n"}} + /openshell {"write":{"b.txt":"second\n"}}`)
	require.NoError(t, err)
	res, err := (Workspace{Dir: r.Repo}).RunOpenShellWorkflow(context.Background(), wf, "")
	require.NoError(t, err, res.Text)
	require.NotNil(t, res.Export)
	assert.Contains(t, res.Text, "strict cost cap: $0.5, $0.25 per worker across 2 worker(s)")
	assert.Contains(t, res.Text, "$0.001000 of $0.25 committed")
	data, err := os.ReadFile(res.Export.RunRecord)
	require.NoError(t, err)
	var run OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	assert.Equal(t, &OpenShellCostBudget{LimitUSD: 0.5, WorkerUSD: 0.25, Workers: 2}, run.CostBudget)
	require.Len(t, run.Tasks, 2)
	for _, worker := range run.Tasks {
		require.NotNil(t, worker.Report.Shield.Budget)
		assert.Equal(t, 0.25, worker.Report.Shield.Budget.LimitUSD)
		answer, err := os.ReadFile(filepath.Join(worker.Evidence, "answer.txt"))
		require.NoError(t, err)
		assert.Contains(t, string(answer), "--max-cost-usd 0.25")
	}
	log, err := os.ReadFile(filepath.Join(run.Integrated.Evidence, "baseline-verify.log"))
	require.NoError(t, err)
	assert.Contains(t, string(log), "max_cost_usd=0\n", "the verify sandbox calls no model, so its Shield may spend nothing")
}

func TestOpenShellStrictCapFailsAWorkerWhoseShieldDidNotHoldIt(t *testing.T) {
	for _, tc := range []struct{ name, instruction, want string }{
		{"breached", `"breach":true`, "a provider bill exceeded its reservation"},
		{"overspent", `"committed":0.75`, "committed $0.75 of a $0.5 share"},
		{"unreported", `"committed":-1`, "committed $-1 of a $0.5 share"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := openShellLegEnv(t)
			t.Setenv("HOME", t.TempDir())
			strictOpenShellEnv(t, "0.5")
			res, err := (Workspace{Dir: r.Repo}).RunOpenShell(context.Background(), fmt.Sprintf(`{"write":{"a.txt":"x\n"},%s}`, tc.instruction))
			require.Error(t, err)
			assert.Nil(t, res.Export)
			assert.Contains(t, err.Error()+res.Text, tc.want)
		})
	}
}

func TestOpenShellStrictCapRefusesThePlannerBeforeItIsAsked(t *testing.T) {
	r := openShellPlanEnv(t)
	strictOpenShellEnv(t, "0.5")
	ask, prompts := fakeOpenShellPlanner(`{"workers":[{"brief":"edit a"}]}`)
	plan, err := planOpenShellTeam(context.Background(), r.Repo, "edit a", "", ask)
	require.ErrorIs(t, err, ErrOpenShellCostCap)
	assert.ErrorContains(t, err, "planner is an unpriced host call")
	assert.Zero(t, plan.DirectorAttempts)
	assert.Empty(t, *prompts, "no model call was made")
}

func TestOpenShellStrictCappedSequenceRecordsItsCapAndIsNotResumed(t *testing.T) {
	strictOpenShellEnv(t, "0.5")
	teams := resumeSequenceTeams()
	teams[1].Tasks[0].Prompt = fmt.Sprintf(`{"hang_once":%q,"expect":{"a.txt":"first\n"},"write":{"a.txt":"second\n"}}`,
		filepath.Join(t.TempDir(), "hung"))
	for i := range teams {
		teams[i].Tasks[0].Profile = "cerebras"
	}
	r := interruptOpenShellStage2(t, teams, true)
	stopped := savedOpenShellSequence(t, r)
	assert.Equal(t, &OpenShellCostBudget{LimitUSD: 0.5, WorkerUSD: 0.25, Workers: 2}, stopped.CostBudget)
	var plan openShellSequencePlan
	_, err := decodeOpenShellSequence(filepath.Join(r.RunDir, "sequence.json"), &plan)
	require.NoError(t, err)
	assert.Equal(t, 0.5, plan.MaxCostUSD, "the cap is part of the checksum-bound plan")
	for _, strict := range []string{"1", ""} {
		t.Setenv("CAPTAIN_STRICT", strict)
		_, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
		require.ErrorIs(t, err, ErrOpenShellCostCap, "strict=%q", strict)
		assert.ErrorContains(t, err, "not supported yet")
	}
}
