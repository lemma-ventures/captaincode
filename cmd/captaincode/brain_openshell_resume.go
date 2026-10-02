package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type openShellRecovery interface {
	Run(context.Context) (captaincode.Result, error)
	Close() error
}

func (b *brain) openShellResumeState(taskID, attemptID string) (*captaincode.AttemptState, *captaincode.TaskState, error) {
	as, ts := b.ledger.AttemptStateFor(attemptID), b.ledger.TaskStateFor(taskID)
	if as == nil || ts == nil || as.TaskID != taskID || as.Leg != captaincode.LegOpenShell {
		return nil, nil, errors.New("openshell: attempt does not belong to this sandbox task")
	}
	if as.State != captaincode.StateInterrupted || ts.State != captaincode.StateInterrupted {
		return nil, nil, errors.New("openshell: task and attempt must both be interrupted")
	}
	if as.InterruptReason == captaincode.InterruptCancelled {
		return nil, nil, errors.New("openshell: cancellation was requested before restart; start a new workflow")
	}
	if as.OpenShell == nil || as.Export != nil {
		return nil, nil, errors.New("openshell: no undelivered sequence checkpoint; use an explicit run-directory recovery or start a new workflow")
	}
	for _, other := range b.ledger.AttemptStatesFor(taskID) {
		if other.AttemptID != attemptID && !other.State.IsTerminal() {
			return nil, nil, errors.New("openshell: another attempt still owns this task")
		}
	}
	for _, charge := range b.ledger.Charges {
		if charge.TaskID == taskID && charge.Kind == captaincode.KindCall {
			return nil, nil, errors.New("openshell: task already has settled usage; refusing to count recovered usage twice")
		}
	}
	_, cancel, err := b.openShellRecoveryBudget(context.Background(), taskID, as.OpenShellPlan)
	if err != nil {
		return nil, nil, err
	}
	cancel()
	return as, ts, as.OpenShell.Validate()
}

func (b *brain) openShellRecoveryBudget(ctx context.Context, taskID string, plan *captaincode.OpenShellPlanRecord) (context.Context, context.CancelFunc, error) {
	budget := b.ledger.BudgetFor(taskID)
	if budget == nil {
		budget = &captaincode.Budget{StartedAt: b.ledger.TaskStateFor(taskID).StartedAt}
	}
	ctx, cancel, err := captaincode.OpenShellBudgetContext(ctx, budget)
	if err != nil || plan == nil {
		return ctx, cancel, err
	}
	if err = plan.Validate(); err == nil {
		ctx, err = captaincode.WithOpenShellAttemptsSpent(ctx, plan.DirectorAttempts)
	}
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}

// openShellStoppedByRestart reports whether a sandbox run ended only because
// the brain is stopping: nobody asked to cancel it, its deadline had not
// passed, and it saved a sequence checkpoint. Such a run is not recorded as
// cancelled; it stays running in the ledger, the next start marks it
// interrupted (ReconcileOnStartup), and captain task resume or startup
// recovery runs the stage the stop cut short again. The caller holds b.mu.
func (b *brain) openShellStoppedByRestart(attemptID string, runErr error) bool {
	if runErr == nil || errors.Is(runErr, context.DeadlineExceeded) || b.lifeContext().Err() == nil {
		return false
	}
	as := b.ledger.AttemptStateFor(attemptID)
	if as == nil || as.Leg != captaincode.LegOpenShell || as.State != captaincode.StateRunning ||
		as.OpenShell == nil || as.Export != nil {
		return false
	}
	ts := b.ledger.TaskStateFor(as.TaskID)
	return ts != nil && ts.State == captaincode.StateRunning
}

// lifeContext is the brain's lifetime: cancelled when shutdown begins.
func (b *brain) lifeContext() context.Context {
	if b.life == nil {
		return context.Background()
	}
	return b.life
}

func (b *brain) resumeOpenShellTask(w http.ResponseWriter, r *http.Request, req captaincode.TaskRequest, body captaincode.ResumeRequest) {
	response, _, err := b.startOpenShellRecovery(r.Context(), b.lifeContext(), req.TaskID, body.AttemptID, false)
	if err != nil {
		status := http.StatusConflict
		code := captaincode.ErrConflict
		var taskErr *captaincode.TaskError
		if errors.As(err, &taskErr) && taskErr.Code == captaincode.ErrInternal {
			status, code = http.StatusInternalServerError, captaincode.ErrInternal
		}
		writeJSON(w, status, taskAPIError(code, err.Error()))
		return
	}
	b.writeTaskOK(w, req, captaincode.OpResume, req.TaskID, response)
}

func (b *brain) startOpenShellRecovery(ctx, runParent context.Context, taskID, attemptID string, automatic bool) (captaincode.ResumeResponse, <-chan struct{}, error) {
	b.mu.Lock()
	as, _, err := b.openShellResumeState(taskID, attemptID)
	if err == nil && runParent.Err() != nil {
		err = errOpenShellShutdown
	}
	if err == nil && automatic {
		err = automaticOpenShellRecovery(as, time.Now())
	}
	var checkpoint captaincode.OpenShellCheckpoint
	var generation int
	budgetCancel := func() {}
	if err == nil {
		ctx, budgetCancel, err = b.openShellRecoveryBudget(ctx, taskID, as.OpenShellPlan)
	}
	if budgetCancel != nil {
		defer budgetCancel()
	}
	if err == nil {
		checkpoint, generation = *as.OpenShell, as.OwnerGen
	}
	b.mu.Unlock()
	if err != nil {
		return captaincode.ResumeResponse{}, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var recovery openShellRecovery
	if b.prepareOpenShellRecoveryFn != nil {
		recovery, err = b.prepareOpenShellRecoveryFn(ctx, checkpoint)
	} else {
		recovery, err = captaincode.PrepareOpenShellRecovery(ctx, checkpoint, nil)
	}
	if err != nil {
		return captaincode.ResumeResponse{}, nil, err
	}
	started := false
	defer func() {
		if !started {
			recovery.Close()
		}
	}()
	b.mu.Lock()
	defer b.mu.Unlock()
	as, ts, err := b.openShellResumeState(taskID, attemptID)
	if err == nil && automatic {
		err = automaticOpenShellRecovery(as, time.Now())
	}
	if err == nil && (as.OwnerGen != generation || *as.OpenShell != checkpoint) {
		err = errors.New("openshell: ownership or checkpoint changed during recovery validation")
	}
	if err == nil {
		err = errors.Join(ctx.Err(), runParent.Err())
	}
	if err != nil {
		return captaincode.ResumeResponse{}, nil, err
	}
	runParent, runBudgetCancel, err := b.openShellRecoveryBudget(runParent, taskID, as.OpenShellPlan)
	if err != nil {
		return captaincode.ResumeResponse{}, nil, err
	}
	defer func() {
		if !started {
			runBudgetCancel()
		}
	}()
	oldAttempt, oldTask := *as, *ts
	now := time.Now()
	next := captaincode.AttemptState{TaskID: taskID, AttemptID: captaincode.NewChargeID(captaincode.KindAttempt),
		Leg: captaincode.LegOpenShell, State: captaincode.StateRunning, ProcessID: b.processID, OwnerGen: 1,
		ParentAttempt: attemptID, StartedAt: now, OpenShell: &checkpoint}
	if err := b.ledger.TransitionAttempt(attemptID, captaincode.StateFailed); err != nil {
		return captaincode.ResumeResponse{}, nil, err
	}
	b.ledger.RecordAttemptState(next)
	if as.OpenShellPlan != nil {
		if err := b.ledger.RecordOpenShellPlan(next.AttemptID, *as.OpenShellPlan); err != nil {
			b.ledger.RecordAttemptState(oldAttempt)
			b.ledger.TransitionAttempt(next.AttemptID, captaincode.StateFailed)
			return captaincode.ResumeResponse{}, nil, err
		}
		next = *b.ledger.AttemptStateFor(next.AttemptID)
	}
	b.ledger.TransitionTask(taskID, captaincode.StateRunning)
	label := "sandbox recovery"
	if automatic {
		label = "sandbox restart recovery"
	}
	b.ledger.RecordCharge(captaincode.Charge{ID: next.AttemptID, Parent: taskID, TaskID: taskID,
		Kind: captaincode.KindAttempt, Leg: captaincode.LegOpenShell, Label: label})
	if err := b.ledger.Save(); err != nil {
		b.ledger.RecordAttemptState(oldAttempt)
		b.ledger.RecordTaskState(oldTask)
		b.ledger.TransitionAttempt(next.AttemptID, captaincode.StateFailed)
		return captaincode.ResumeResponse{}, nil, captaincode.NewTaskError(captaincode.ErrInternal, "openshell: save recovery dispatch: "+err.Error())
	}
	runCtx, stop := b.cancelTree.Register(taskID, next.AttemptID, "openshell recovery", runParent)
	runCtx = captaincode.WithOpenShellCheckpoint(runCtx, func(checkpoint captaincode.OpenShellCheckpoint) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.ledger.VerifyOwnership(next.AttemptID, next.ProcessID, next.OwnerGen) {
			return errors.New("openshell: recovery ownership changed")
		}
		if err := b.ledger.RecordOpenShellCheckpoint(next.AttemptID, checkpoint); err != nil {
			return err
		}
		return b.ledger.Save()
	})
	b.inflightRuns.Add(1)
	b.sandboxes.Add(1)
	started = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer b.sandboxes.Done()
		defer b.inflightRuns.Add(-1)
		defer stop()
		defer runBudgetCancel()
		defer recovery.Close()
		res, runErr := recovery.Run(runCtx)
		if next.OpenShellPlan != nil {
			res = addOpenShellPlanningUsage(res, next.OpenShellPlan.DirectorAttempts)
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		current := b.ledger.AttemptStateFor(next.AttemptID)
		if current == nil || current.ProcessID != next.ProcessID || current.OwnerGen != next.OwnerGen {
			return
		}
		if runCtx.Err() != nil || current.State == captaincode.StateCancelled || current.State == captaincode.StateCancelRequested {
			res.Export = nil
			res.Text = "OpenShell recovery interrupted: no verified export delivered."
			runErr = captaincode.ErrInterrupted
			if runCtx.Err() != nil {
				runErr = runCtx.Err()
			}
		}
		if runErr == nil && res.Export == nil {
			runErr = errors.New("openshell: recovery returned no verified export")
		}
		if b.openShellStoppedByRestart(next.AttemptID, runErr) {
			if err := b.ledger.Save(); err != nil {
				log.Printf("openshell: save stopped recovery %s: %v", next.AttemptID, err)
			}
			return
		}
		if err := recordOpenShellSolo(b.ledger, taskID, next.AttemptID, oldTask.Label, res, runErr); err != nil {
			log.Printf("openshell: save recovery result for %s: %v", next.AttemptID, err)
		}
	}()
	log.Printf("openshell: resumed task %s as %s from %s (automatic=%t)", taskID, next.AttemptID, attemptID, automatic)
	return captaincode.ResumeResponse{
		TaskID: taskID, AttemptID: next.AttemptID, ParentAttempt: attemptID, State: captaincode.StateRunning,
	}, done, nil
}

var _ openShellRecovery = (*captaincode.OpenShellRecovery)(nil)

func automaticOpenShellRecovery(as *captaincode.AttemptState, now time.Time) error {
	if as.InterruptReason != "brain process restarted" {
		return errors.New("openshell: automatic recovery requires an interrupted running controller")
	}
	if as.CheckpointAt.IsZero() || as.CheckpointAt.After(now) || now.Sub(as.CheckpointAt) > captaincode.DefaultResumePolicy().MaxAge {
		return errors.New("openshell: automatic recovery requires a verified checkpoint from the last 24 hours")
	}
	return nil
}

func (b *brain) recoverOpenShellOnStartup(ctx context.Context) {
	if os.Getenv("CAPTAIN_OPENSHELL_AUTO_RESUME") != "1" {
		return
	}
	b.mu.Lock()
	attempts := b.ledger.InterruptedAttempts()
	saveErr := b.ledger.Save()
	b.mu.Unlock()
	if saveErr != nil {
		log.Printf("openshell: startup recovery withheld: save reconciliation: %v", saveErr)
		return
	}
	for _, as := range attempts {
		if ctx.Err() != nil {
			return
		}
		if as.Leg != captaincode.LegOpenShell || as.OpenShell == nil {
			continue
		}
		_, done, err := b.startOpenShellRecovery(ctx, ctx, as.TaskID, as.AttemptID, true)
		if err != nil {
			log.Printf("openshell: startup recovery withheld for task %s attempt %s: %v", as.TaskID, as.AttemptID, err)
			continue
		}
		select {
		case <-done:
		case <-ctx.Done():
			<-done
			return
		}
	}
}
