package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenShellStartupRecoveryRequiresOptIn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes"} {
		t.Run(value, func(t *testing.T) {
			b, _, _ := interruptedOpenShellTask(t)
			t.Setenv("CAPTAIN_OPENSHELL_AUTO_RESUME", value)
			b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
				t.Error("startup recovery ran without explicit opt-in")
				return nil, errors.New("unexpected preparation")
			}
			b.recoverOpenShellOnStartup(context.Background())
			assert.Len(t, b.ledger.AttemptStates, 1)
		})
	}
}

func TestOpenShellStartupRecoveryCreatesOneDurableContinuation(t *testing.T) {
	b, task, parent := interruptedOpenShellTask(t)
	t.Setenv("CAPTAIN_OPENSHELL_AUTO_RESUME", "1")
	var calls atomic.Int32
	recovery := &testOpenShellRecovery{run: func(context.Context) (captaincode.Result, error) {
		calls.Add(1)
		stored, err := captaincode.LoadLedger()
		require.NoError(t, err)
		require.Len(t, stored.AttemptStates, 2)
		assert.Equal(t, captaincode.StateFailed, stored.AttemptStateFor(parent).State)
		return openShellHTTPResult("/fixture"), nil
	}}
	b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
		return recovery, nil
	}
	b.recoverOpenShellOnStartup(context.Background())
	b.recoverOpenShellOnStartup(context.Background())
	assert.True(t, recovery.closed.Load())
	assert.Equal(t, int32(1), calls.Load())
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	require.Len(t, stored.AttemptStates, 2)
	assert.Equal(t, captaincode.StateSucceeded, stored.TaskStateFor(task).State)
	var charges int
	for _, charge := range stored.ChargesFor(task) {
		if charge.Kind == captaincode.KindCall {
			charges++
		}
	}
	assert.Equal(t, 1, charges)
	for _, as := range stored.AttemptStatesFor(task) {
		if as.AttemptID != parent {
			assert.Equal(t, parent, as.ParentAttempt)
			require.NotNil(t, as.Export)
			assert.Equal(t, as.AttemptID, as.Export.Manifest.AttemptID)
		}
	}
	assert.Contains(t, captaincode.FormatHandoffBrief(*stored.HandoffFor(task)), "exported (not applied)")
	assert.Empty(t, b.cancelTree.TaskIDs())
}

func TestOpenShellStartupRecoveryRefusesUnsafeCandidates(t *testing.T) {
	for _, name := range []string{"stale checkpoint", "missing timestamp", "future checkpoint", "waiting for input", "operator interrupt", "no checkpoint", "host worker", "unsupported budget", "settled usage", "cancelled task", "cancel intent", "changed runtime", "save failure", "shutdown"} {
		t.Run(name, func(t *testing.T) {
			b, task, attempt := interruptedOpenShellTask(t)
			t.Setenv("CAPTAIN_OPENSHELL_AUTO_RESUME", "1")
			as := b.ledger.AttemptStateFor(attempt)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			switch name {
			case "stale checkpoint":
				as.CheckpointAt = time.Now().Add(-25 * time.Hour)
				as.UpdatedAt = time.Now()
			case "missing timestamp":
				as.CheckpointAt = time.Time{}
			case "future checkpoint":
				as.CheckpointAt = time.Now().Add(time.Hour)
			case "waiting for input":
				as.InterruptReason = "brain restarted while sandbox was waiting_for_input"
			case "operator interrupt":
				as.InterruptReason = "operator interrupt"
			case "no checkpoint":
				as.OpenShell = nil
			case "host worker":
				as.Leg = captaincode.LegClaude
			case "unsupported budget":
				t.Setenv("CAPTAIN_MAX_COST", "1")
				t.Setenv("CAPTAIN_STRICT", "1")
			case "settled usage":
				b.ledger.RecordCharge(captaincode.Charge{TaskID: task, Parent: attempt, Kind: captaincode.KindCall})
			case "cancelled task":
				b.ledger.TaskStateFor(task).State = captaincode.StateCancelled
			case "cancel intent":
				as.InterruptReason = captaincode.InterruptCancelled
			case "save failure":
				require.NoError(t, os.Mkdir(filepath.Join(os.Getenv("HOME"), ".captaincode", "state.json.tmp"), 0o700))
			case "shutdown":
				stop()
			}
			before := *as
			var calls int
			b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
				calls++
				return nil, errors.New("runtime or pilot changed")
			}
			b.recoverOpenShellOnStartup(ctx)
			assert.Equal(t, before, *b.ledger.AttemptStateFor(attempt))
			assert.Len(t, b.ledger.AttemptStates, 1)
			assert.Zero(t, b.inflightRuns.Load())
			if name == "changed runtime" {
				assert.Equal(t, 1, calls)
			} else {
				assert.Zero(t, calls)
			}
		})
	}
}

func TestOpenShellStartupRecoverySerializesAndStopsOnShutdown(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "serial", true: "shutdown"}[cancel], func(t *testing.T) {
			b, first, parent := interruptedOpenShellTask(t)
			t.Setenv("CAPTAIN_OPENSHELL_AUTO_RESUME", "1")
			second, attempt, err := beginOpenShellSolo(b.ledger, "second sequence", "dead-process")
			require.NoError(t, err)
			checkpoint := *b.ledger.AttemptStateFor(parent).OpenShell
			checkpoint.RunDir += "-second"
			require.NoError(t, b.ledger.RecordOpenShellCheckpoint(attempt, checkpoint))
			b.ledger.ReconcileOnStartup()
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			ctx, stop := context.WithCancel(context.Background())
			t.Cleanup(func() {
				stop()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("startup recovery did not stop")
				}
			})
			var prepared atomic.Int32
			b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
				n := prepared.Add(1)
				return &testOpenShellRecovery{run: func(ctx context.Context) (captaincode.Result, error) {
					if n == 1 {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
						}
					}
					return openShellHTTPResult("/fixture"), nil
				}}, nil
			}
			go func() { defer close(finished); b.recoverOpenShellOnStartup(ctx) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first controller did not start")
			}
			assert.Equal(t, int32(1), prepared.Load())
			if cancel {
				stop()
			} else {
				close(release)
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("startup recovery did not finish")
			}
			if cancel {
				assert.Equal(t, int32(1), prepared.Load())
				assert.Equal(t, captaincode.StateCancelled, b.ledger.TaskStateFor(first).State)
				assert.Equal(t, captaincode.StateInterrupted, b.ledger.TaskStateFor(second).State)
				for _, as := range b.ledger.AttemptStates {
					assert.Nil(t, as.Export)
				}
			} else {
				assert.Equal(t, int32(2), prepared.Load())
				assert.Equal(t, captaincode.StateSucceeded, b.ledger.TaskStateFor(first).State)
				assert.Equal(t, captaincode.StateSucceeded, b.ledger.TaskStateFor(second).State)
			}
			assert.Empty(t, b.cancelTree.TaskIDs())
		})
	}
}

func TestOpenShellRestartPreservesCancellationIntent(t *testing.T) {
	for _, taskCancel := range []bool{false, true} {
		b, task, attempt := interruptedOpenShellTask(t)
		as := b.ledger.AttemptStateFor(attempt)
		as.State = captaincode.StateRunning
		b.ledger.TaskStateFor(task).State = captaincode.StateRunning
		if taskCancel {
			b.ledger.TaskStateFor(task).State = captaincode.StateCancelRequested
		} else {
			as.State = captaincode.StateCancelRequested
		}
		b.ledger.ReconcileOnStartup()
		require.NoError(t, b.ledger.Save())
		stored, err := captaincode.LoadLedger()
		require.NoError(t, err)
		b.ledger = stored
		b.ledger.ReconcileOnStartup()
		assert.Equal(t, captaincode.InterruptCancelled, b.ledger.AttemptStateFor(attempt).InterruptReason)
		rec := resumeOpenShellRequest(t, b, context.Background(), task, attempt)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), "cancellation was requested")
		assert.Len(t, b.ledger.AttemptStates, 1)
	}
}

func TestOpenShellLegacyResumeCannotBypassValidation(t *testing.T) {
	b, task, attempt := interruptedOpenShellTask(t)
	before := *b.ledger.AttemptStateFor(attempt)
	rec := httptest.NewRecorder()
	b.resumeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/resume?task="+task, nil))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "captain task resume")
	assert.Equal(t, before, *b.ledger.AttemptStateFor(attempt))
	assert.Len(t, b.ledger.AttemptStates, 1)
}

func TestOpenShellStartupRecoveryDoesNotResumeWaitingTasks(t *testing.T) {
	for _, taskWaiting := range []bool{false, true} {
		b, task, attempt := interruptedOpenShellTask(t)
		t.Setenv("CAPTAIN_OPENSHELL_AUTO_RESUME", "1")
		b.ledger.AttemptStateFor(attempt).State = captaincode.StateRunning
		b.ledger.TaskStateFor(task).State = captaincode.StateRunning
		if taskWaiting {
			b.ledger.TaskStateFor(task).State = captaincode.StateWaitingForInput
		} else {
			b.ledger.AttemptStateFor(attempt).State = captaincode.StateWaitingForInput
		}
		b.ledger.ReconcileOnStartup()
		b.prepareOpenShellRecoveryFn = func(context.Context, captaincode.OpenShellCheckpoint) (openShellRecovery, error) {
			t.Error("waiting task resumed automatically")
			return nil, errors.New("unexpected preparation")
		}
		b.recoverOpenShellOnStartup(context.Background())
		assert.Len(t, b.ledger.AttemptStates, 1)
		assert.Equal(t, captaincode.StateInterrupted, b.ledger.TaskStateFor(task).State)
	}
}
