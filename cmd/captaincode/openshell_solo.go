package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdOpenShellSolo(ledger *captaincode.Ledger, task, until string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := os.Getwd()
	if err == nil {
		err = runOpenShellSolo(ctx, ledger, captaincode.Workspace{Dir: dir}, task, until, os.Stdout)
	}
	if err != nil {
		fatal(err)
	}
}

func runOpenShellSolo(ctx context.Context, ledger *captaincode.Ledger, ws captaincode.Workspace, task, until string, out io.Writer) error {
	if until != "" {
		return errors.New("openshell: --until runs on the host; use CAPTAIN_OPENSHELL_VERIFY for sandbox verification")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	wf, err := captaincode.ParseOpenShellWorkflow(task)
	if err != nil {
		return err
	}
	ws.Steer = captaincode.NewSteer(ws.Dir)
	stop := context.AfterFunc(ctx, func() { ws.Steer.Interrupt("CLI cancelled") })
	defer stop()
	taskID, attemptID, err := beginOpenShellSolo(ledger, task, captaincode.CurrentProcessID())
	if err != nil {
		return err
	}
	ctx, budgetCancel, err := captaincode.OpenShellBudgetContext(ctx, ledger.BudgetFor(taskID))
	if err != nil {
		return errors.Join(err, recordOpenShellSolo(ledger, taskID, attemptID, task, openShellNotDispatched(), err))
	}
	defer budgetCancel()
	ctx = captaincode.WithOpenShellCheckpoint(ctx, func(checkpoint captaincode.OpenShellCheckpoint) error {
		if err := ledger.RecordOpenShellCheckpoint(attemptID, checkpoint); err != nil {
			return err
		}
		return ledger.Save()
	})
	var res captaincode.Result
	var runErr error
	if len(wf.Stages) > 0 {
		res, runErr = ws.RunOpenShellWorkflow(ctx, wf, "")
	} else {
		res, runErr = ws.RunOpenShell(ctx, task)
	}
	if ctx.Err() != nil {
		res.Export = nil
		res.Text = "OpenShell interrupted: no verified export delivered."
		runErr = ctx.Err()
	}
	if runErr == nil && res.Export == nil {
		runErr = errors.New("openshell: worker returned no verified export")
	}
	saveErr := recordOpenShellSolo(ledger, taskID, attemptID, task, res, runErr)
	if saveErr != nil {
		return errors.Join(runErr, fmt.Errorf("openshell: save result: %w", saveErr))
	}
	if res.Text != "" {
		fmt.Fprintln(out, res.Text)
	}
	fmt.Fprintf(out, "task: %s\n", taskID)
	return runErr
}

func beginOpenShellSolo(ledger *captaincode.Ledger, task, processID string) (string, string, error) {
	_, cancel, err := captaincode.OpenShellBudgetContext(context.Background(), nil)
	if err != nil {
		return "", "", err
	}
	cancel()
	taskID := captaincode.NewChargeID(captaincode.KindTask)
	attemptID := captaincode.NewChargeID(captaincode.KindAttempt)
	now := time.Now()
	limits := captaincode.DefaultBudgetOpts()
	if limits.MaxWallMs > 0 || limits.MaxAttempts > 0 {
		budget := captaincode.NewBudget(taskID, task, captaincode.BudgetOpts{MaxWallMs: limits.MaxWallMs, MaxAttempts: limits.MaxAttempts})
		budget.StartedAt = now
		ledger.RecordBudget(budget)
	}
	ledger.RecordCharge(captaincode.Charge{ID: taskID, TaskID: taskID, Kind: captaincode.KindTask, Label: task})
	ledger.RecordCharge(captaincode.Charge{ID: attemptID, Parent: taskID, TaskID: taskID, Kind: captaincode.KindAttempt, Leg: captaincode.LegOpenShell, Label: "worker"})
	ledger.RecordTaskState(captaincode.TaskState{TaskID: taskID, Label: task, State: captaincode.StateRunning, StartedAt: now})
	ledger.RecordAttemptState(captaincode.AttemptState{TaskID: taskID, AttemptID: attemptID, Leg: captaincode.LegOpenShell,
		State: captaincode.StateRunning, ProcessID: processID, OwnerGen: 1, StartedAt: now})
	if err := ledger.Save(); err != nil {
		// Nothing was dispatched: a later save must not persist a running task
		// that the next restart would report as interrupted.
		ledger.TransitionAttempt(attemptID, captaincode.StateFailed)
		ledger.TransitionTask(taskID, captaincode.StateFailed)
		return taskID, attemptID, fmt.Errorf("openshell: save dispatch: %w", err)
	}
	return taskID, attemptID, nil
}

// openShellNotDispatched is the result recorded when no sandbox started: zero
// model attempts, known. An empty Result would settle as unknown usage.
func openShellNotDispatched() captaincode.Result {
	return captaincode.Result{OpenShellAttempts: &captaincode.OpenShellAttemptUsage{}}
}

func recordOpenShellSolo(ledger *captaincode.Ledger, taskID, attemptID, task string, res captaincode.Result, runErr error) error {
	if errors.Is(runErr, context.DeadlineExceeded) {
		if budget := ledger.BudgetFor(taskID); budget != nil {
			budget.Stop(captaincode.StopTimeExhausted)
		}
	}
	if errors.Is(runErr, captaincode.ErrOpenShellAttemptCap) {
		if budget := ledger.BudgetFor(taskID); budget != nil {
			budget.Stop(captaincode.StopAttemptsExhausted)
		}
	}
	if err := ledger.ReconcileOpenShellSpend(attemptID, res.OpenShellAttempts, res.BilledUSD()); err != nil {
		return err
	}
	state, outcome := captaincode.StateSucceeded, "ok"
	errText := ""
	if runErr != nil {
		state, outcome, errText = captaincode.StateFailed, "fail", runErr.Error()
		if errors.Is(runErr, captaincode.ErrInterrupted) || errors.Is(runErr, context.Canceled) {
			state, outcome = captaincode.StateCancelled, "cancelled"
		}
	}
	usage := captaincode.OpenShellUsage(res)
	ledger.RecordCharge(captaincode.Charge{ID: attemptID + ":openshell-call", Parent: attemptID, TaskID: taskID, Kind: captaincode.KindCall,
		Leg: captaincode.LegOpenShell, Label: "worker", DurationMs: res.DurationMs, Usage: usage})
	ledger.Record(captaincode.Event{TaskID: taskID, AttemptID: attemptID, Task: truncate(task, 120),
		Class: captaincode.Classify(task), Leg: captaincode.LegOpenShell, Reason: "explicit sandbox",
		Outcome: outcome, Error: errText, Duration: res.DurationMs, Tokens: res.Tokens,
		CostUSD: usage.CostUSD, CostStatus: usage.CostStatus})
	if err := ledger.TransitionAttempt(attemptID, state); err != nil {
		return err
	}
	if err := ledger.TransitionTask(taskID, state); err != nil {
		return err
	}
	// Only once success is recorded: a cancellation that won the race leaves
	// the transitions above failing, and its attempt must carry no export.
	if state == captaincode.StateSucceeded && res.Export != nil {
		ledger.RecordVerifiedExport(attemptID, *res.Export)
	}
	if res.AdvisoryReview != nil {
		ledger.RecordAdvisoryReview(attemptID, *res.AdvisoryReview)
	}
	ledger.RecordHandoff(captaincode.BuildHandoffBrief(ledger, taskID, task, nil))
	return ledger.Save()
}
