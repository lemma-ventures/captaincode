package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testOpenShellRecovery struct {
	run    func(context.Context) (captaincode.Result, error)
	closed atomic.Bool
}

func (r *testOpenShellRecovery) Run(ctx context.Context) (captaincode.Result, error) {
	return r.run(ctx)
}
func (r *testOpenShellRecovery) Close() error { r.closed.Store(true); return nil }

func interruptedOpenShellTask(t *testing.T) (*brain, string, string) {
	t.Helper()
	b := openShellHTTPBrain(t)
	task, attempt, err := beginOpenShellSolo(b.ledger, "edit then review", "dead-process")
	require.NoError(t, err)
	require.NoError(t, b.ledger.RecordOpenShellCheckpoint(attempt, captaincode.OpenShellCheckpoint{
		RunDir: "/tmp/fixture-run", SequenceSHA256: strings.Repeat("a", 64), EvidenceSHA256: strings.Repeat("b", 64),
	}))
	require.NoError(t, b.ledger.Save())
	b.ledger, err = captaincode.LoadLedger()
	require.NoError(t, err)
	b.ledger.ReconcileOnStartup()
	require.NoError(t, b.ledger.Save())
	return b, task, attempt
}

func resumeOpenShellRequest(t *testing.T, b *brain, ctx context.Context, task, attempt string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(captaincode.ResumeRequest{AttemptID: attempt})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	b.taskResume(rec, httptest.NewRequest(http.MethodPost, "/v1/task", nil).WithContext(ctx), captaincode.TaskRequest{
		Version: captaincode.TaskAPIVersion, Op: captaincode.OpResume, TaskID: task, Body: body,
	})
	return rec
}

func waitOpenShellRecovery(t *testing.T, b *brain, recovery *testOpenShellRecovery) {
	t.Helper()
	require.Eventually(t, func() bool { return b.inflightRuns.Load() == 0 && recovery.closed.Load() }, 5*time.Second, time.Millisecond)
}

func TestOpenShellTaskResumeCreatesDurableLinkedAttempt(t *testing.T) {
	b, task, parent := interruptedOpenShellTask(t)
	finish := make(chan struct{})
	var calls atomic.Int32
	recovery := &testOpenShellRecovery{run: func(ctx context.Context) (captaincode.Result, error) {
		calls.Add(1)
		select {
		case <-finish:
		case <-ctx.Done():
			return captaincode.Result{}, ctx.Err()
		}
		result := openShellHTTPResult("/fixture")
		result.OpenShellAttempts = &captaincode.OpenShellAttemptUsage{Workers: 2, Repairs: 1}
		return result, nil
	}}
	b.prepareOpenShellRecoveryFn = func(ctx context.Context, checkpoint captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		assert.Equal(t, "/tmp/fixture-run", checkpoint.RunDir)
		return recovery, nil
	}
	ctx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	t.Cleanup(func() { b.cancelTree.Cancel(task) })
	rec := resumeOpenShellRequest(t, b, ctx, task, parent)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var envelope captaincode.TaskResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	var response captaincode.ResumeResponse
	require.NoError(t, json.Unmarshal(envelope.Body, &response))
	assert.NotEqual(t, parent, response.AttemptID)
	assert.Equal(t, parent, response.ParentAttempt)
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	assert.Equal(t, captaincode.StateFailed, stored.AttemptStateFor(parent).State)
	next := stored.AttemptStateFor(response.AttemptID)
	require.NotNil(t, next)
	assert.Equal(t, captaincode.StateRunning, next.State)
	assert.Equal(t, parent, next.ParentAttempt)
	assert.Equal(t, *stored.AttemptStateFor(parent).OpenShell, *next.OpenShell)
	disconnect()
	duplicate := resumeOpenShellRequest(t, b, context.Background(), task, parent)
	assert.Equal(t, http.StatusConflict, duplicate.Code)
	close(finish)
	waitOpenShellRecovery(t, b, recovery)
	stored, err = captaincode.LoadLedger()
	require.NoError(t, err)
	next = stored.AttemptStateFor(response.AttemptID)
	assert.Equal(t, captaincode.StateSucceeded, next.State)
	require.NotNil(t, next.Export)
	assert.Equal(t, response.AttemptID, next.Export.Manifest.AttemptID)
	assert.Equal(t, task, next.Export.Manifest.TaskID)
	assert.Contains(t, captaincode.FormatHandoffBrief(*stored.HandoffFor(task)), "exported (not applied)")
	assert.Equal(t, int32(1), calls.Load())
	assert.Nil(t, stored.AttemptStateFor(parent).OpenShellAttempts)
	assert.Equal(t, &captaincode.OpenShellAttemptUsage{Workers: 2, Repairs: 1}, next.OpenShellAttempts)
	require.NotNil(t, stored.BudgetFor(task))
	assert.Equal(t, 3, stored.BudgetFor(task).SettledAttempts)
	assert.Empty(t, b.cancelTree.TaskIDs())
}

func TestOpenShellTaskResumeRejectsWithoutChangingLifecycle(t *testing.T) {
	for _, name := range []string{"no checkpoint", "legacy checkpoint", "wrong task", "cancelled", "altered evidence", "request cancelled", "settled usage"} {
		t.Run(name, func(t *testing.T) {
			b, task, attempt := interruptedOpenShellTask(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls int
			recovery := &testOpenShellRecovery{run: func(context.Context) (captaincode.Result, error) {
				t.Error("refused recovery dispatched a worker")
				return captaincode.Result{}, nil
			}}
			b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
				calls++
				if name == "altered evidence" {
					return nil, errors.New("changed checksum")
				}
				if name == "request cancelled" {
					cancel()
				}
				return recovery, nil
			}
			switch name {
			case "no checkpoint":
				b.ledger.AttemptStateFor(attempt).OpenShell = nil
			case "legacy checkpoint":
				b.ledger.AttemptStateFor(attempt).OpenShell.EvidenceSHA256 = ""
			case "wrong task":
				task = "unrelated"
			case "cancelled":
				require.NoError(t, b.ledger.TransitionAttempt(attempt, captaincode.StateCancelled))
				require.NoError(t, b.ledger.TransitionTask(task, captaincode.StateCancelled))
			case "settled usage":
				b.ledger.RecordCharge(captaincode.Charge{TaskID: task, Parent: attempt, Kind: captaincode.KindCall})
			}
			before := *b.ledger.AttemptStateFor(attempt)
			rec := resumeOpenShellRequest(t, b, ctx, task, attempt)
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, before, *b.ledger.AttemptStateFor(attempt))
			assert.Len(t, b.ledger.AttemptStates, 1)
			assert.Zero(t, b.inflightRuns.Load())
			if name == "request cancelled" {
				assert.True(t, recovery.closed.Load())
			}
			if name != "request cancelled" && name != "altered evidence" {
				assert.Zero(t, calls)
			}
		})
	}
}

func TestOpenShellTaskResumeCancellationWithholdsExport(t *testing.T) {
	b, task, attempt := interruptedOpenShellTask(t)
	recovery := &testOpenShellRecovery{run: func(ctx context.Context) (captaincode.Result, error) {
		<-ctx.Done()
		return openShellHTTPResult("/fixture"), nil
	}}
	b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		return recovery, nil
	}
	rec := resumeOpenShellRequest(t, b, context.Background(), task, attempt)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	b.cancelTaskWithDeadline(task)
	waitOpenShellRecovery(t, b, recovery)
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	assert.Equal(t, captaincode.StateCancelled, stored.TaskStateFor(task).State)
	for _, as := range stored.AttemptStatesFor(task) {
		assert.Nil(t, as.Export)
	}
	assert.Empty(t, stored.HandoffFor(task).Artifacts)
}

func TestOpenShellBrainShutdownStopsResumedSandboxes(t *testing.T) {
	b, task, attempt := interruptedOpenShellTask(t)
	life, stopLife := context.WithCancel(context.Background())
	defer stopLife()
	b.life = life
	recovery := &testOpenShellRecovery{run: func(ctx context.Context) (captaincode.Result, error) {
		<-ctx.Done()
		return openShellHTTPResult("/fixture"), nil
	}}
	b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		return recovery, nil
	}
	// The resume request ends at once; the controller outlives it.
	rec := resumeOpenShellRequest(t, b, context.Background(), task, attempt)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	stopLife()
	waited := make(chan struct{})
	go func() {
		b.sandboxes.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not stop the resumed sandbox controller")
	}
	waitOpenShellRecovery(t, b, recovery)
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	states := map[captaincode.LifecycleState]int{}
	for _, as := range stored.AttemptStatesFor(task) {
		assert.Nil(t, as.Export)
		states[as.State]++
	}
	assert.Equal(t, map[captaincode.LifecycleState]int{captaincode.StateFailed: 1, captaincode.StateRunning: 1}, states,
		"a brain stop is not a cancellation: the continuation waits for the next start")
	assertOpenShellResumableAfterRestart(t, b, stored, task)

	b2, task2, attempt2 := interruptedOpenShellTask(t)
	b2.life = life
	b2.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		t.Fatal("a resume was admitted after shutdown began")
		return nil, nil
	}
	rec = resumeOpenShellRequest(t, b2, context.Background(), task2, attempt2)
	assert.NotEqual(t, http.StatusOK, rec.Code)
	rec = httptest.NewRecorder()
	b2.chatCompletions(rec, openShellHTTPRequest(t, t.TempDir(), "openshell", "fix parser", false))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

// assertOpenShellResumableAfterRestart checks what the next start makes of a
// sandbox run a brain stop left behind: an interrupted attempt with nothing
// settled yet, which captain task resume accepts.
func assertOpenShellResumableAfterRestart(t *testing.T, b *brain, stored *captaincode.Ledger, task string) {
	t.Helper()
	for _, charge := range stored.Charges {
		assert.False(t, charge.TaskID == task && charge.Kind == captaincode.KindCall, "usage settles when the run finishes")
	}
	stored.ReconcileOnStartup()
	var stopped []captaincode.AttemptState
	for _, as := range stored.AttemptStatesFor(task) {
		if as.State == captaincode.StateInterrupted {
			stopped = append(stopped, as)
		}
	}
	require.Len(t, stopped, 1)
	assert.Equal(t, "brain process restarted", stopped[0].InterruptReason)
	b.ledger = stored
	_, _, err := b.openShellResumeState(task, stopped[0].AttemptID)
	assert.NoError(t, err, "the next start can resume the run")
}

func TestOpenShellHTTPBrainStopLeavesSequenceResumable(t *testing.T) {
	for _, checkpointed := range []bool{true, false} {
		t.Run(map[bool]string{true: "sequence", false: "no checkpoint"}[checkpointed], func(t *testing.T) {
			b := openShellHTTPBrain(t)
			life, stopLife := context.WithCancel(context.Background())
			defer stopLife()
			b.life = life
			started := make(chan string, 1)
			b.runOpenShellWorkflowFn = func(ctx context.Context, _ captaincode.Workspace, _ captaincode.Workflow, _ string) (captaincode.Result, error) {
				b.mu.Lock()
				attempt := b.ledger.AttemptStates[0].AttemptID
				var err error
				if checkpointed {
					err = b.ledger.RecordOpenShellCheckpoint(attempt, captaincode.OpenShellCheckpoint{
						RunDir: "/tmp/fixture-run", SequenceSHA256: strings.Repeat("a", 64), EvidenceSHA256: strings.Repeat("b", 64),
					})
				}
				b.mu.Unlock()
				assert.NoError(t, err)
				started <- attempt
				<-ctx.Done()
				return captaincode.Result{OpenShellAttempts: &captaincode.OpenShellAttemptUsage{Workers: 1}}, captaincode.ErrInterrupted
			}
			req := openShellHTTPRequest(t, t.TempDir(), "openshell", "/openshell edit a > /openshell edit b", false)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rec := httptest.NewRecorder()
				b.chatCompletions(rec, req)
				done <- rec
			}()
			attempt := <-started
			stopLife()
			rec := <-done
			assert.NotEqual(t, http.StatusOK, rec.Code)
			stored, err := captaincode.LoadLedger()
			require.NoError(t, err)
			as := stored.AttemptStateFor(attempt)
			require.NotNil(t, as)
			if !checkpointed {
				assert.Equal(t, captaincode.StateCancelled, as.State, "without a checkpoint there is nothing to resume")
				return
			}
			assert.Equal(t, captaincode.StateRunning, as.State)
			assert.Contains(t, rec.Body.String(), "captain task resume "+as.TaskID+" "+attempt)
			assertOpenShellResumableAfterRestart(t, b, stored, as.TaskID)
		})
	}
}

func TestOpenShellTaskResumeCancelledDuringValidation(t *testing.T) {
	b, task, attempt := interruptedOpenShellTask(t)
	recovery := &testOpenShellRecovery{run: func(context.Context) (captaincode.Result, error) {
		t.Error("cancelled task launched a recovery controller")
		return captaincode.Result{}, nil
	}}
	b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		b.cancelTaskWithDeadline(task)
		return recovery, nil
	}
	rec := resumeOpenShellRequest(t, b, context.Background(), task, attempt)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.True(t, recovery.closed.Load())
	assert.Equal(t, captaincode.StateCancelled, b.ledger.TaskStateFor(task).State)
	assert.Equal(t, captaincode.StateCancelled, b.ledger.AttemptStateFor(attempt).State)
	assert.Len(t, b.ledger.AttemptStates, 1)
	assert.Empty(t, b.cancelTree.TaskIDs())
}

func TestOpenShellTaskResumeMissingExportFails(t *testing.T) {
	b, task, attempt := interruptedOpenShellTask(t)
	recovery := &testOpenShellRecovery{run: func(context.Context) (captaincode.Result, error) {
		return captaincode.Result{Text: "claimed success"}, nil
	}}
	b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		return recovery, nil
	}
	rec := resumeOpenShellRequest(t, b, context.Background(), task, attempt)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	waitOpenShellRecovery(t, b, recovery)
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	assert.Equal(t, captaincode.StateFailed, stored.TaskStateFor(task).State)
	assert.Empty(t, stored.HandoffFor(task).Artifacts)
	assert.Contains(t, stored.Events[len(stored.Events)-1].Error, "no verified export")
}

func TestOpenShellTaskResumePersistenceFailureDoesNotDispatch(t *testing.T) {
	b, task, attempt := interruptedOpenShellTask(t)
	recovery := &testOpenShellRecovery{run: func(context.Context) (captaincode.Result, error) {
		t.Error("unsaved recovery launched a controller")
		return captaincode.Result{}, nil
	}}
	b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		require.NoError(t, os.Mkdir(filepath.Join(os.Getenv("HOME"), ".captaincode", "state.json.tmp"), 0o700))
		return recovery, nil
	}
	before := *b.ledger.AttemptStateFor(attempt)
	rec := resumeOpenShellRequest(t, b, context.Background(), task, attempt)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Equal(t, before, *b.ledger.AttemptStateFor(attempt))
	assert.Equal(t, captaincode.StateInterrupted, b.ledger.TaskStateFor(task).State)
	assert.True(t, recovery.closed.Load())
	assert.Empty(t, b.cancelTree.TaskIDs())
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	assert.Len(t, stored.AttemptStates, 1)
	assert.Equal(t, captaincode.StateInterrupted, stored.AttemptStateFor(attempt).State)
}

func TestOpenShellTaskResumeKeepsPersistedWallTime(t *testing.T) {
	b, task, parent := interruptedOpenShellTask(t)
	started := time.Now().Add(-time.Hour)
	budget := captaincode.NewBudget(task, "wall time", captaincode.BudgetOpts{MaxWallMs: int64(time.Hour/time.Millisecond) + 1000})
	budget.StartedAt = started
	b.ledger.RecordBudget(budget)
	require.NoError(t, b.ledger.Save())
	b.ledger, _ = captaincode.LoadLedger()
	t.Setenv("CAPTAIN_MAX_WALLTIME", "2h")
	expected := started.Add(time.Hour + time.Second)
	recovery := &testOpenShellRecovery{run: func(ctx context.Context) (captaincode.Result, error) {
		deadline, ok := ctx.Deadline()
		assert.True(t, ok)
		assert.True(t, expected.Equal(deadline))
		<-ctx.Done()
		return openShellHTTPResult("/fixture"), nil
	}}
	b.prepareOpenShellRecoveryFn = func(ctx context.Context, _ captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		deadline, ok := ctx.Deadline()
		assert.True(t, ok)
		assert.True(t, expected.Equal(deadline))
		return recovery, nil
	}
	rec := resumeOpenShellRequest(t, b, context.Background(), task, parent)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	waitOpenShellRecovery(t, b, recovery)
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	assert.Equal(t, captaincode.StateFailed, stored.TaskStateFor(task).State)
	assert.Equal(t, captaincode.StopTimeExhausted, stored.BudgetFor(task).StopReason)
	for _, attempt := range stored.AttemptStatesFor(task) {
		assert.Nil(t, attempt.Export)
	}
}

func TestOpenShellTaskResumeRefusesExpiredWallTimeBeforeValidation(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprint(automatic), func(t *testing.T) {
			b, task, parent := interruptedOpenShellTask(t)
			budget := captaincode.NewBudget(task, "expired", captaincode.BudgetOpts{MaxWallMs: 1000})
			budget.StartedAt = time.Now().Add(-time.Minute)
			b.ledger.RecordBudget(budget)
			b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
				t.Fatal("expired task reached recovery validation")
				return nil, nil
			}
			_, _, err := b.startOpenShellRecovery(context.Background(), context.Background(), task, parent, automatic)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Len(t, b.ledger.AttemptStatesFor(task), 1)
			assert.Equal(t, captaincode.StateInterrupted, b.ledger.TaskStateFor(task).State)
		})
	}
}

func TestOpenShellPlannedStagesResumeKeepsPlanAndCountsPlannerOnce(t *testing.T) {
	b, task, parent := interruptedOpenShellTask(t)
	plan := captaincode.OpenShellTeamPlan{DirectorAttempts: 2, Assignments: []string{"edit a", "inspect a"},
		Stages: []captaincode.OpenShellPlanStage{{Mode: "edit", Assignments: []string{"edit a"}}, {Mode: "review", Assignments: []string{"inspect a"}}}}
	record := plan.Record()
	b.ledger.AttemptStateFor(parent).OpenShellPlan = &record
	require.NoError(t, b.ledger.Save())
	b.ledger, _ = captaincode.LoadLedger()
	b.planOpenShellTeamFn = func(context.Context, string, string, string) (captaincode.OpenShellTeamPlan, error) {
		t.Error("recovery must reuse the saved plan")
		return captaincode.OpenShellTeamPlan{}, errors.New("unexpected replanning")
	}
	for i := 0; i < 2; i++ {
		life, stop := context.WithCancel(context.Background())
		b.life = life
		recovery := &testOpenShellRecovery{run: func(context.Context) (captaincode.Result, error) {
			if i == 0 {
				stop()
				return captaincode.Result{OpenShellAttempts: &captaincode.OpenShellAttemptUsage{Workers: 1}}, context.Canceled
			}
			res := openShellHTTPResult("/fixture")
			res.OpenShellAttempts = &captaincode.OpenShellAttemptUsage{Workers: 2, Directors: 1}
			return res, nil
		}}
		b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
			return recovery, nil
		}
		response, done, err := b.startOpenShellRecovery(context.Background(), life, task, parent, false)
		require.NoError(t, err)
		<-done
		stop()
		stored, err := captaincode.LoadLedger()
		require.NoError(t, err)
		next := stored.AttemptStateFor(response.AttemptID)
		require.NotNil(t, next)
		assert.Equal(t, plan.Record(), *next.OpenShellPlan)
		assert.Nil(t, stored.AttemptStateFor(parent).OpenShellAttempts)
		if i == 0 {
			assert.Nil(t, next.OpenShellAttempts)
			stored.ReconcileOnStartup()
			require.NoError(t, stored.Save())
			b.ledger = stored
			parent = response.AttemptID
		} else {
			assert.Equal(t, &captaincode.OpenShellAttemptUsage{Workers: 2, Directors: 3}, next.OpenShellAttempts)
			assert.Equal(t, 5, stored.BudgetFor(task).SettledAttempts)
			assert.Equal(t, captaincode.StateSucceeded, next.State)
		}
	}
}

func TestOpenShellPlannedRecoveryRejectsSpentOrInvalidPlanBudget(t *testing.T) {
	for _, mode := range []string{"spent", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			b, task, parent := interruptedOpenShellTask(t)
			plan := captaincode.OpenShellPlanRecord{Assignments: []string{"edit a"}, DirectorAttempts: 2}
			if mode == "spent" {
				t.Setenv("CAPTAIN_MAX_ATTEMPTS", "2")
			} else {
				plan.Stages = []captaincode.OpenShellPlanStage{{Mode: "host", Assignments: []string{"edit a"}}}
			}
			b.ledger.AttemptStateFor(parent).OpenShellPlan = &plan
			b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
				t.Error("invalid plan reached controller admission")
				return nil, errors.New("unexpected admission")
			}
			rec := resumeOpenShellRequest(t, b, context.Background(), task, parent)
			assert.Equal(t, http.StatusConflict, rec.Code)
			assert.Len(t, b.ledger.AttemptStates, 1)
			assert.Equal(t, captaincode.StateInterrupted, b.ledger.AttemptStateFor(parent).State)
		})
	}
}

func TestOpenShellPlannedRecoveryPreservesTighterAttemptLimit(t *testing.T) {
	b, task, parent := interruptedOpenShellTask(t)
	t.Setenv("CAPTAIN_MAX_ATTEMPTS", "5")
	plan := captaincode.OpenShellPlanRecord{Assignments: []string{"edit a"}, DirectorAttempts: 2}
	b.ledger.AttemptStateFor(parent).OpenShellPlan = &plan
	recovery := &testOpenShellRecovery{run: func(ctx context.Context) (captaincode.Result, error) {
		_, err := captaincode.WithOpenShellAttemptsSpent(ctx, 3)
		assert.ErrorIs(t, err, captaincode.ErrOpenShellAttemptCap, "two planner calls leave three slots")
		res := openShellHTTPResult("/fixture")
		res.OpenShellAttempts = &captaincode.OpenShellAttemptUsage{Workers: 1}
		return res, nil
	}}
	b.prepareOpenShellRecoveryFn = func(ctx context.Context, _ captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		_, err := captaincode.WithOpenShellAttemptsSpent(ctx, 3)
		assert.ErrorIs(t, err, captaincode.ErrOpenShellAttemptCap, "admission includes the earlier planner calls")
		return recovery, nil
	}
	_, done, err := b.startOpenShellRecovery(context.Background(), context.Background(), task, parent, false)
	require.NoError(t, err)
	<-done
}
