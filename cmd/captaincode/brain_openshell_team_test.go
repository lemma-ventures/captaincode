package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openShellTeamRequest(t *testing.T, dir, model, text string, stream bool) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": model, "stream": stream, "messages": []map[string]string{
		{"role": "system", "content": "keep public APIs stable"},
		{"role": "user", "content": text},
	}})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set(workspaceHeader, dir)
	return r
}

// openShellTeamBrain refuses every host path a sandbox team must not take.
func openShellTeamBrain(t *testing.T) *brain {
	t.Helper()
	b := openShellHTTPBrain(t)
	b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Error("a sandbox team dispatched a host worker")
		return leg, captaincode.Result{}, errors.New("host worker forbidden")
	}
	b.planFn = func(string, captaincode.Class, string, []captaincode.Leg, map[captaincode.Leg]captaincode.LegStats, map[string]captaincode.TeamStat, bool) (captaincode.Plan, error) {
		t.Error("a sandbox team asked the host team planner")
		return captaincode.Plan{}, errors.New("host planner forbidden")
	}
	return b
}

func TestOpenShellTeamTurnRunsTheDirectorsPlanInSandboxes(t *testing.T) {
	for _, model := range []string{"team", "auto"} {
		for _, stream := range []bool{false, true} {
			t.Run(model+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				b := openShellTeamBrain(t)
				dir := t.TempDir()
				plan := captaincode.OpenShellTeamPlan{Rationale: "one file each", DirectorAttempts: 1, Assignments: []string{"update a.txt", "update\nb.txt\x1b[2J"},
					Workflow: captaincode.Workflow{Stages: []captaincode.WorkflowStage{{Legs: []captaincode.WorkflowLeg{
						{Leg: captaincode.LegOpenShell, Prompt: "worker 1 assignment"},
						{Leg: captaincode.LegOpenShell, Prompt: "worker 2 assignment"},
					}}}}}
				planned := 0
				b.planOpenShellTeamFn = func(ctx context.Context, d, task, history string) (captaincode.OpenShellTeamPlan, error) {
					planned++
					assert.Equal(t, dir, d)
					assert.Equal(t, "update a and b", task)
					assert.Equal(t, "[system]\nkeep public APIs stable\n\n", history)
					stored, err := captaincode.LoadLedger()
					require.NoError(t, err)
					require.Len(t, stored.AttemptStates, 1, "the task is saved before the director is asked")
					assert.Equal(t, captaincode.StateRunning, stored.AttemptStates[0].State)
					return plan, nil
				}
				b.runOpenShellWorkflowFn = func(_ context.Context, ws captaincode.Workspace, wf captaincode.Workflow, history string) (captaincode.Result, error) {
					assert.Equal(t, dir, ws.Dir)
					assert.Equal(t, plan.Workflow, wf)
					assert.Equal(t, "[system]\nkeep public APIs stable\n\n", history)
					stored, err := captaincode.LoadLedger()
					require.NoError(t, err)
					require.Len(t, stored.AttemptStates, 1)
					require.NotNil(t, stored.AttemptStates[0].OpenShellPlan, "the plan is saved before any sandbox starts")
					assert.Equal(t, plan.Record(), *stored.AttemptStates[0].OpenShellPlan)
					res := openShellHTTPResult(dir)
					res.OpenShellAttempts = &captaincode.OpenShellAttemptUsage{Workers: 2, Directors: 2}
					return res, nil
				}
				rec := httptest.NewRecorder()
				b.chatCompletions(rec, openShellTeamRequest(t, dir, model, "/team /openshell update a and b", stream))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, 1, planned)
				assert.Contains(t, rec.Body.String(), "the director planned 2 sandbox worker(s) in 1 call(s) - one file each")
				assert.Contains(t, rec.Body.String(), "w1: update a.txt")
				assert.Contains(t, rec.Body.String(), "w2: update b.txt")
				assert.NotContains(t, rec.Body.String(), "\x1b", "director text cannot drive the terminal")
				assert.NotContains(t, rec.Body.String(), "\\u001b", "director text cannot drive the terminal")
				assert.Contains(t, rec.Body.String(), "exported (not applied)")
				stored, err := captaincode.LoadLedger()
				require.NoError(t, err)
				attempt := stored.AttemptStateFor(rec.Header().Get("X-Captain-Attempt-ID"))
				require.NotNil(t, attempt)
				assert.Equal(t, captaincode.StateSucceeded, attempt.State)
				require.NotNil(t, attempt.Export)
				require.NotNil(t, attempt.OpenShellAttempts)
				assert.Equal(t, captaincode.OpenShellAttemptUsage{Workers: 2, Directors: 3}, *attempt.OpenShellAttempts,
					"the plan's director call settles with the rulings")
				require.NotNil(t, attempt.OpenShellPlan)
				assert.Equal(t, []string{"update a.txt", "update\nb.txt\x1b[2J"}, attempt.OpenShellPlan.Assignments)
				assert.Empty(t, b.cancelTree.TaskIDs())
			})
		}
	}
}

func TestOpenShellTeamTurnRefusesHostMembersBeforePlanning(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"host worker named", "/team /openshell /claude fix it", "cannot include host workers"},
		{"frontier named", "/team /frontier /openshell fix it", "cannot include host workers"},
		{"no task", "/team /openshell", "provide a user task"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := openShellTeamBrain(t)
			b.planOpenShellTeamFn = func(context.Context, string, string, string) (captaincode.OpenShellTeamPlan, error) {
				t.Error("an unsupported team reached the planner")
				return captaincode.OpenShellTeamPlan{}, errors.New("unexpected plan")
			}
			b.runOpenShellWorkflowFn = func(context.Context, captaincode.Workspace, captaincode.Workflow, string) (captaincode.Result, error) {
				t.Error("an unsupported team dispatched a sandbox")
				return captaincode.Result{}, errors.New("unexpected run")
			}
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, openShellTeamRequest(t, t.TempDir(), "team", tc.text, false))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.want)
			stored, err := captaincode.LoadLedger()
			require.NoError(t, err)
			assert.Empty(t, stored.AttemptStates)
		})
	}
}

func TestOpenShellTeamPlanFailureSettlesTheDirectorsCalls(t *testing.T) {
	for _, tc := range []struct {
		name     string
		maxTries string
		attempts int
		planErr  error
		stop     string
	}{
		{name: "malformed plan", attempts: 2, planErr: errors.New("openshell: no valid JSON in the director's plan after retry")},
		{name: "plan spends the cap", maxTries: "2", attempts: 2, stop: captaincode.StopAttemptsExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := openShellTeamBrain(t)
			t.Setenv("CAPTAIN_MAX_ATTEMPTS", tc.maxTries)
			dir := t.TempDir()
			b.planOpenShellTeamFn = func(context.Context, string, string, string) (captaincode.OpenShellTeamPlan, error) {
				plan := captaincode.OpenShellTeamPlan{DirectorAttempts: tc.attempts}
				if tc.planErr == nil {
					plan.Assignments = []string{"fix it"}
					plan.Workflow = captaincode.Workflow{Stages: []captaincode.WorkflowStage{{Legs: []captaincode.WorkflowLeg{{Leg: captaincode.LegOpenShell, Prompt: "fix it"}}}}}
				}
				return plan, tc.planErr
			}
			b.runOpenShellWorkflowFn = func(context.Context, captaincode.Workspace, captaincode.Workflow, string) (captaincode.Result, error) {
				t.Error("a failed plan dispatched a sandbox")
				return captaincode.Result{}, errors.New("unexpected run")
			}
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, openShellTeamRequest(t, dir, "team", "/team /openshell fix it", false))
			assert.NotEqual(t, http.StatusOK, rec.Code)
			assert.NotContains(t, rec.Body.String(), "exported (not applied)")
			stored, err := captaincode.LoadLedger()
			require.NoError(t, err)
			require.Len(t, stored.AttemptStates, 1)
			attempt := stored.AttemptStates[0]
			assert.Equal(t, captaincode.StateFailed, attempt.State)
			assert.Nil(t, attempt.Export)
			require.NotNil(t, attempt.OpenShellAttempts)
			assert.Equal(t, captaincode.OpenShellAttemptUsage{Directors: tc.attempts}, *attempt.OpenShellAttempts)
			if tc.stop != "" {
				budget := stored.BudgetFor(attempt.TaskID)
				require.NotNil(t, budget)
				assert.Equal(t, tc.stop, budget.StopReason)
			}
			assert.Empty(t, b.cancelTree.TaskIDs())
		})
	}
}

func TestOpenShellTeamCancelledWhilePlanningStartsNoSandbox(t *testing.T) {
	for _, mode := range []string{"disconnect", "task cancel"} {
		t.Run(mode, func(t *testing.T) {
			b := openShellTeamBrain(t)
			dir := t.TempDir()
			ctx, disconnect := context.WithCancel(context.Background())
			defer disconnect()
			b.planOpenShellTeamFn = func(planCtx context.Context, _, _, _ string) (captaincode.OpenShellTeamPlan, error) {
				if mode == "disconnect" {
					disconnect()
				} else {
					stored, err := captaincode.LoadLedger()
					require.NoError(t, err)
					require.Len(t, stored.AttemptStates, 1)
					b.cancelTask(stored.AttemptStates[0].TaskID)
				}
				<-planCtx.Done()
				return captaincode.OpenShellTeamPlan{DirectorAttempts: 1}, planCtx.Err()
			}
			b.runOpenShellWorkflowFn = func(context.Context, captaincode.Workspace, captaincode.Workflow, string) (captaincode.Result, error) {
				t.Error("a cancelled plan dispatched a sandbox")
				return captaincode.Result{}, errors.New("unexpected run")
			}
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, openShellTeamRequest(t, dir, "team", "/team /openshell fix it", false).WithContext(ctx))
			assert.NotContains(t, rec.Body.String(), "exported (not applied)")
			stored, err := captaincode.LoadLedger()
			require.NoError(t, err)
			require.Len(t, stored.AttemptStates, 1)
			attempt := stored.AttemptStates[0]
			assert.Equal(t, captaincode.StateCancelled, attempt.State)
			assert.Nil(t, attempt.Export)
			require.NotNil(t, attempt.OpenShellAttempts)
			assert.Equal(t, captaincode.OpenShellAttemptUsage{Directors: 1}, *attempt.OpenShellAttempts)
			assert.Empty(t, b.cancelTree.TaskIDs())
		})
	}
}

func TestOpenShellTeamPlanThatCannotBeSavedStartsNoSandbox(t *testing.T) {
	b := openShellTeamBrain(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	b.planOpenShellTeamFn = func(context.Context, string, string, string) (captaincode.OpenShellTeamPlan, error) {
		state := filepath.Join(home, ".captaincode", "state.json")
		require.NoError(t, os.Remove(state))
		require.NoError(t, os.Mkdir(state, 0o700))
		return captaincode.OpenShellTeamPlan{DirectorAttempts: 1, Assignments: []string{"fix it"},
			Workflow: captaincode.Workflow{Stages: []captaincode.WorkflowStage{{Legs: []captaincode.WorkflowLeg{{Leg: captaincode.LegOpenShell, Prompt: "fix it"}}}}}}, nil
	}
	b.runOpenShellWorkflowFn = func(context.Context, captaincode.Workspace, captaincode.Workflow, string) (captaincode.Result, error) {
		t.Error("an unsaved plan dispatched a sandbox")
		return captaincode.Result{}, errors.New("unexpected run")
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, openShellTeamRequest(t, t.TempDir(), "team", "/team /openshell fix it", false))
	assert.NotEqual(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "save team plan")
	require.Len(t, b.ledger.AttemptStates, 1)
	attempt := b.ledger.AttemptStates[0]
	assert.True(t, attempt.State.IsTerminal(), "the attempt stays %s", attempt.State)
	require.NotNil(t, attempt.OpenShellAttempts)
	assert.Equal(t, captaincode.OpenShellAttemptUsage{Directors: 1}, *attempt.OpenShellAttempts)
}

func TestOpenShellTeamStagesAreSavedAndDisplayedBeforeDispatch(t *testing.T) {
	b := openShellTeamBrain(t)
	plan := captaincode.OpenShellTeamPlan{DirectorAttempts: 1, Assignments: []string{"edit a", "inspect a"},
		Stages: []captaincode.OpenShellPlanStage{
			{Mode: "edit", Assignments: []string{"edit a"}},
			{Mode: "review", Assignments: []string{"inspect a"}},
		}, Workflow: captaincode.Workflow{Stages: []captaincode.WorkflowStage{
			{Legs: []captaincode.WorkflowLeg{{Leg: captaincode.LegOpenShell, Prompt: "edit a"}}},
			{Legs: []captaincode.WorkflowLeg{{Leg: captaincode.LegOpenShell, Prompt: "--review inspect a"}}},
		}}}
	b.planOpenShellTeamFn = func(context.Context, string, string, string) (captaincode.OpenShellTeamPlan, error) { return plan, nil }
	b.runOpenShellWorkflowFn = func(_ context.Context, _ captaincode.Workspace, wf captaincode.Workflow, _ string) (captaincode.Result, error) {
		assert.Equal(t, plan.Workflow, wf)
		stored, err := captaincode.LoadLedger()
		require.NoError(t, err)
		require.Len(t, stored.AttemptStates, 1)
		assert.Equal(t, plan.Record(), *stored.AttemptStates[0].OpenShellPlan)
		res := openShellHTTPResult("/fixture")
		res.OpenShellAttempts = &captaincode.OpenShellAttemptUsage{Workers: 2}
		return res, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, openShellTeamRequest(t, t.TempDir(), "team", "/team /openshell edit then review a", false))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "across 2 stage(s)")
	assert.Contains(t, rec.Body.String(), "s1-w1 (edit): edit a")
	assert.Contains(t, rec.Body.String(), "s2-w1 (review): inspect a")
	assert.Equal(t, []string{"s1-w1 (edit): edit a", "s2-w1 (review): inspect a"}, openShellPlanLines(plan.Record(), 300))
}
