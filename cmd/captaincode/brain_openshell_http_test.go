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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openShellHTTPBrain(t *testing.T) *brain {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPTAIN_OPENSHELL_PREPARED", "")
	for _, key := range []string{"CAPTAIN_MAX_ATTEMPTS", "CAPTAIN_MAX_WALLTIME", "CAPTAIN_MAX_COST", "CAPTAIN_STRICT"} {
		t.Setenv(key, "")
	}
	b := teamBrain()
	b.processID = "http-test"
	b.cancelTree = captaincode.NewCancelTree()
	var err error
	b.ledger, err = captaincode.LoadLedger()
	require.NoError(t, err)
	b.captureTestFn = func(context.Context, string) (*captaincode.CheckEvidence, error) {
		t.Error("sandbox HTTP path ran host verification")
		return nil, errors.New("host verification forbidden")
	}
	return b
}

func openShellHTTPRequest(t *testing.T, dir, model, task string, stream bool) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": model, "stream": stream,
		"messages": []map[string]string{{"role": "user", "content": task}},
	})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set(workspaceHeader, dir)
	return r
}

func openShellHTTPResult(dir string) captaincode.Result {
	return captaincode.Result{OpenShellAttempts: &captaincode.OpenShellAttemptUsage{Workers: 1}, Text: "OpenShell pass: exported (not applied)", Export: &captaincode.VerifiedExport{
		Repository: dir, Runtime: "vm", RunRecord: filepath.Join(dir, "run.json"),
		Manifest: captaincode.PatchManifest{
			BaseRevision: strings.Repeat("a", 40), ChangedFiles: []string{"parser.py"},
			DiffPath: filepath.Join(dir, "integrated.patch"), DiffDigest: strings.Repeat("b", 64),
			Check: &captaincode.CheckEvidence{Command: []string{"python3", "-m", "unittest"}, Passed: true},
		},
	}}
}

func TestOpenShellHTTPPersistsBeforeResponding(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, model := range []string{"openshell", "auto"} {
			t.Run(model+"/"+map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
				b := openShellHTTPBrain(t)
				dir := gitRepoWithChange(t)
				index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
				require.NoError(t, err)
				before, err := os.ReadFile(filepath.Join(dir, "a.go"))
				require.NoError(t, err)
				b.storeTeamPlan("fix parser", captaincode.Plan{Workers: []captaincode.Worker{{Leg: captaincode.LegCursor}}})
				var calls int
				b.runWorkerFn = func(leg captaincode.Leg, prompt string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
					calls++
					assert.Equal(t, captaincode.LegOpenShell, leg)
					assert.Equal(t, "[user]\nfix parser\n\n", prompt)
					stored, err := captaincode.LoadLedger()
					require.NoError(t, err)
					require.Len(t, stored.AttemptStates, 1)
					assert.Equal(t, captaincode.StateRunning, stored.AttemptStates[0].State)
					assert.Equal(t, "http-test", stored.AttemptStates[0].ProcessID)
					assert.NoDirExists(t, filepath.Join(dir, ".agents"))
					return leg, openShellHTTPResult(dir), nil
				}
				rec := httptest.NewRecorder()
				b.chatCompletions(rec, openShellHTTPRequest(t, dir, model, "/openshell fix parser", stream))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "exported (not applied)")
				assert.Equal(t, 1, calls)
				stored, err := captaincode.LoadLedger()
				require.NoError(t, err)
				taskID, attemptID := rec.Header().Get("X-Captain-Task-ID"), rec.Header().Get("X-Captain-Attempt-ID")
				require.NotEmpty(t, taskID)
				require.NotEmpty(t, attemptID)
				attempt := stored.AttemptStateFor(attemptID)
				require.NotNil(t, attempt)
				assert.Equal(t, captaincode.StateSucceeded, attempt.State)
				require.NotNil(t, attempt.Export)
				assert.Equal(t, taskID, attempt.Export.Manifest.TaskID)
				assert.Empty(t, attempt.ChangedFiles)
				assert.Empty(t, attempt.DiffPath)
				assert.Contains(t, captaincode.FormatHandoffBrief(*stored.HandoffFor(taskID)), "exported (not applied)")
				after, err := os.ReadFile(filepath.Join(dir, "a.go"))
				require.NoError(t, err)
				assert.Equal(t, before, after)
				afterIndex, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
				require.NoError(t, err)
				assert.Equal(t, index, afterIndex)
				assert.Empty(t, b.cancelTree.TaskIDs())
			})
		}
	}
}

func TestOpenShellHTTPRejectsUnsafePrelude(t *testing.T) {
	for _, tc := range []struct{ name, task, dir, want string }{
		{"oversize", strings.Repeat("x", 16385), "", "host compaction is disabled"},
		{"directive only", "/openshell", "", "provide a user task"},
		{"directives only", " /openshell  /quality ", "", "provide a user task"},
		{"gate", "/openshell fix parser gate: touch host-marker", "", "host gates"},
		{"sequence", "/openshell fix parser > /cursor review", "", "host workers"},
		{"mixed parallel", "/openshell fix parser + /cursor review", "", "host workers"},
		{"relative workspace", "fix parser", "relative", "existing absolute directory"},
		{"missing workspace", "fix parser", "/nonexistent-captain-test-repository", "existing absolute directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := openShellHTTPBrain(t)
			dir := tc.dir
			if dir == "" {
				dir = t.TempDir()
			}
			b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
				t.Error("invalid request dispatched a worker")
				return leg, captaincode.Result{}, nil
			}
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, openShellHTTPRequest(t, dir, "openshell", tc.task, false))
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.want)
			assert.Empty(t, b.ledger.AttemptStates)
		})
	}
}

func TestOpenShellHTTPFailureCannotSucceed(t *testing.T) {
	for _, mode := range []string{"configuration", "no export", "verification", "reroute"} {
		t.Run(mode, func(t *testing.T) {
			b := openShellHTTPBrain(t)
			dir := t.TempDir()
			if mode != "configuration" {
				b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
					res := openShellHTTPResult(dir)
					switch mode {
					case "no export":
						res.Export = nil
					case "verification":
						return leg, res, errors.New("sandbox check failed")
					case "reroute":
						return captaincode.LegCursor, res, nil
					}
					return leg, res, nil
				}
			}
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, openShellHTTPRequest(t, dir, "openshell", "fix parser", false))
			assert.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "chat.completion")
			assert.NotContains(t, rec.Body.String(), "exported (not applied)")
			stored, err := captaincode.LoadLedger()
			require.NoError(t, err)
			require.Len(t, stored.AttemptStates, 1)
			assert.Equal(t, captaincode.StateFailed, stored.AttemptStates[0].State)
			assert.Nil(t, stored.AttemptStates[0].Export)
		})
	}
}

func TestOpenShellHTTPCancellationWithholdsEvenCompletedExport(t *testing.T) {
	for _, mode := range []string{"disconnect", "task cancel", "interrupt"} {
		t.Run(mode, func(t *testing.T) {
			b := openShellHTTPBrain(t)
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
				switch mode {
				case "disconnect":
					cancel()
				case "task cancel":
					b.cancelTask(b.ledger.AttemptStates[0].TaskID)
				case "interrupt":
					b.steers.mu.Lock()
					for steer := range b.steers.live {
						steer.Interrupt("test interrupt")
					}
					b.steers.mu.Unlock()
				}
				return leg, openShellHTTPResult(dir), nil
			}
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, openShellHTTPRequest(t, dir, "openshell", "fix parser", false).WithContext(ctx))
			assert.NotContains(t, rec.Body.String(), "exported (not applied)")
			stored, err := captaincode.LoadLedger()
			require.NoError(t, err)
			require.Len(t, stored.AttemptStates, 1)
			assert.Equal(t, captaincode.StateCancelled, stored.AttemptStates[0].State)
			assert.Nil(t, stored.AttemptStates[0].Export)
			assert.Empty(t, b.cancelTree.TaskIDs())
		})
	}
}

func TestOpenShellHTTPDedupeIsWorkspaceScopedAndNeverReplaysCompletedExport(t *testing.T) {
	b := openShellHTTPBrain(t)
	first, second := t.TempDir(), t.TempDir()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return leg, openShellHTTPResult(first), nil
	}
	r := openShellHTTPRequest(t, first, "openshell", "fix parser", false)
	go func() { defer close(done); b.chatCompletions(httptest.NewRecorder(), r) }()
	defer func() { close(release); <-done }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, openShellHTTPRequest(t, second, "openshell", "fix parser", false))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int32(2), calls.Load())
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, openShellHTTPRequest(t, second, "openshell", "fix parser", false))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int32(3), calls.Load())
}

func TestOpenShellHTTPRefusesDispatchWhenLedgerCannotBeSaved(t *testing.T) {
	b := openShellHTTPBrain(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	state := filepath.Join(home, ".captaincode")
	require.NoError(t, os.MkdirAll(state, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(state, "state.json"), 0o700))
	b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Error("worker ran without a durable dispatch record")
		return leg, captaincode.Result{}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, openShellHTTPRequest(t, t.TempDir(), "openshell", "fix parser", false))
	assert.Contains(t, rec.Body.String(), "save dispatch")
	assert.NotEqual(t, http.StatusOK, rec.Code)
	for _, ts := range b.ledger.TaskStates {
		assert.True(t, ts.State.IsTerminal(), "an undispatched task stays %s in memory", ts.State)
	}
	for _, as := range b.ledger.AttemptStates {
		assert.True(t, as.State.IsTerminal(), "an undispatched attempt stays %s in memory", as.State)
	}
}

func TestOpenShellHTTPConcurrentRetryWaitsForOneExport(t *testing.T) {
	b := openShellHTTPBrain(t)
	dir := t.TempDir()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return leg, openShellHTTPResult(dir), nil
	}
	r := openShellHTTPRequest(t, dir, "openshell", "fix parser", false)
	go func() { defer close(done); b.chatCompletions(httptest.NewRecorder(), r) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	b.chatCompletions(httptest.NewRecorder(), openShellHTTPRequest(t, dir, "openshell", "fix parser", false).WithContext(ctx))
	assert.Equal(t, int32(1), calls.Load())
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("original request did not finish")
	}
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	require.Len(t, stored.AttemptStates, 1)
	assert.Equal(t, captaincode.StateSucceeded, stored.AttemptStates[0].State)
}

func TestOpenShellHTTPDoesNotBypassConfiguredBudgets(t *testing.T) {
	for _, mode := range []string{"attempts", "cost"} {
		t.Run(mode, func(t *testing.T) {
			b := openShellHTTPBrain(t)
			switch mode {
			case "attempts":
				t.Setenv("CAPTAIN_MAX_ATTEMPTS", "-1")
			case "cost":
				t.Setenv("CAPTAIN_MAX_COST", "NaN")
				t.Setenv("CAPTAIN_STRICT", "1")
			}
			b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
				t.Error("worker bypassed configured budget")
				return leg, captaincode.Result{}, nil
			}
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, openShellHTTPRequest(t, t.TempDir(), "openshell", "fix parser", false))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			if mode == "cost" {
				assert.Contains(t, rec.Body.String(), "CAPTAIN_MAX_COST")
			} else {
				assert.Contains(t, rec.Body.String(), "CAPTAIN_MAX_ATTEMPTS")
			}
			assert.Empty(t, b.ledger.AttemptStates)
		})
	}
}

func TestOpenShellHTTPCarriesAStrictCostCapToTheSandboxRun(t *testing.T) {
	b := openShellHTTPBrain(t)
	t.Setenv("CAPTAIN_MAX_COST", "1")
	t.Setenv("CAPTAIN_STRICT", "1")
	var limit float64
	b.runOpenShellWorkflowFn = func(ctx context.Context, _ captaincode.Workspace, _ captaincode.Workflow, _ string) (captaincode.Result, error) {
		limit = captaincode.OpenShellCostLimit(ctx)
		return openShellHTTPResult("/fixture"), nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, openShellHTTPRequest(t, t.TempDir(), "openshell", "/openshell fix a + /openshell fix b", false))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1.0, limit, "Shield enforces the cap; the brain must hand it to the run")
}

func TestOpenShellHTTPWorkflowPersistsAndCancels(t *testing.T) {
	for _, connector := range []string{" + ", " > "} {

		for _, mode := range []string{"success", "failure", "disconnect", "task cancel", "no export"} {
			t.Run(connector+mode, func(t *testing.T) {
				b := openShellHTTPBrain(t)
				dir := t.TempDir()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
					t.Fatal("parallel workflow entered solo dispatch")
					return leg, captaincode.Result{}, nil
				}
				b.runOpenShellWorkflowFn = func(runCtx context.Context, ws captaincode.Workspace, wf captaincode.Workflow, history string) (captaincode.Result, error) {
					assert.Equal(t, dir, ws.Dir)
					assert.Equal(t, "edit a", wf.Stages[0].Legs[0].Prompt)
					if connector == " + " {
						require.Len(t, wf.Stages, 1)
						require.Len(t, wf.Stages[0].Legs, 2)
						assert.Equal(t, "edit b", wf.Stages[0].Legs[1].Prompt)
					} else {
						require.Len(t, wf.Stages, 2)
						assert.Equal(t, "edit b", wf.Stages[1].Legs[0].Prompt)
					}
					assert.Equal(t, "[system]\nkeep public APIs stable\n\n", history)
					stored, err := captaincode.LoadLedger()
					require.NoError(t, err)
					require.Len(t, stored.AttemptStates, 1)
					assert.Equal(t, captaincode.StateRunning, stored.AttemptStates[0].State)
					assert.NoDirExists(t, filepath.Join(dir, ".agents"))
					res := openShellHTTPResult(dir)
					switch mode {
					case "failure":
						return res, errors.New("one required worker failed")
					case "disconnect":
						cancel()
					case "task cancel":
						b.cancelTask(stored.AttemptStates[0].TaskID)
					case "no export":
						res.Export = nil
					}
					if mode == "disconnect" || mode == "task cancel" {
						assert.ErrorIs(t, runCtx.Err(), context.Canceled)
					}
					return res, nil
				}
				body, err := json.Marshal(map[string]any{"model": "auto", "messages": []map[string]string{
					{"role": "system", "content": "keep public APIs stable"},
					{"role": "user", "content": "/openshell edit a" + connector + "/openshell edit b"},
				}})
				require.NoError(t, err)
				r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
				r.Header.Set(workspaceHeader, dir)
				rec := httptest.NewRecorder()
				b.chatCompletions(rec, r)
				stored, err := captaincode.LoadLedger()
				require.NoError(t, err)
				require.Len(t, stored.AttemptStates, 1)
				attempt := stored.AttemptStates[0]
				if mode == "success" {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					assert.Equal(t, captaincode.StateSucceeded, attempt.State)
					require.NotNil(t, attempt.Export)
					assert.Contains(t, captaincode.FormatHandoffBrief(*stored.HandoffFor(attempt.TaskID)), "exported (not applied)")
				} else {
					assert.NotEqual(t, captaincode.StateSucceeded, attempt.State)
					assert.Nil(t, attempt.Export)
					assert.NotContains(t, rec.Body.String(), "exported (not applied)")
				}
				assert.Empty(t, b.cancelTree.TaskIDs())
			})
		}
	}
}

func TestOpenShellHTTPSingleReviewUsesWorkflowBoundary(t *testing.T) {
	b := openShellHTTPBrain(t)
	dir := t.TempDir()
	b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Fatal("review entered edit dispatch")
		return leg, captaincode.Result{}, nil
	}
	calls := 0
	b.runOpenShellWorkflowFn = func(_ context.Context, _ captaincode.Workspace, wf captaincode.Workflow, history string) (captaincode.Result, error) {
		calls++
		require.Len(t, wf.Stages, 1)
		require.Len(t, wf.Stages[0].Legs, 1)
		assert.Equal(t, "--review inspect parser", wf.Stages[0].Legs[0].Prompt)
		assert.Empty(t, history)
		res := openShellHTTPResult(dir)
		res.Export.Manifest.ChangedFiles = nil
		res.Text = "verified unchanged snapshot; nothing to apply"
		return res, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, openShellHTTPRequest(t, dir, "auto", "/openshell --review inspect parser", false))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, calls)
	assert.Contains(t, rec.Body.String(), "nothing to apply")
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	attempt := stored.AttemptStateFor(rec.Header().Get("X-Captain-Attempt-ID"))
	require.NotNil(t, attempt)
	assert.Equal(t, captaincode.StateSucceeded, attempt.State)
	require.NotNil(t, attempt.Export)
	assert.Empty(t, attempt.Export.Manifest.ChangedFiles)
}

func TestOpenShellHTTPWallTimeWithholdsLateExport(t *testing.T) {
	b := openShellHTTPBrain(t)
	t.Setenv("CAPTAIN_MAX_WALLTIME", "100ms")
	var called bool
	b.runOpenShellWorkflowFn = func(ctx context.Context, ws captaincode.Workspace, _ captaincode.Workflow, _ string) (captaincode.Result, error) {
		called = true
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), 100*time.Millisecond)
		<-ctx.Done()
		return openShellHTTPResult(ws.Dir), nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, openShellHTTPRequest(t, t.TempDir(), "openshell", "/openshell edit a > /openshell edit b", false))
	require.True(t, called)
	assert.NotEqual(t, http.StatusOK, rec.Code)
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	require.Len(t, stored.AttemptStates, 1)
	attempt := stored.AttemptStates[0]
	assert.Equal(t, captaincode.StateFailed, attempt.State)
	assert.Nil(t, attempt.Export)
	budget := stored.BudgetFor(attempt.TaskID)
	require.NotNil(t, budget)
	assert.Equal(t, int64(100), budget.MaxWallMs)
	assert.Equal(t, captaincode.StopTimeExhausted, budget.StopReason)
}

func TestOpenShellHTTPPersistsAttemptCapAndAdmissionFailure(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "admitted", true: "refused"}[refused], func(t *testing.T) {
			b := openShellHTTPBrain(t)
			t.Setenv("CAPTAIN_MAX_ATTEMPTS", "2")
			dir := t.TempDir()
			b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
				stored, err := captaincode.LoadLedger()
				require.NoError(t, err)
				require.Len(t, stored.TaskStates, 1)
				budget := stored.BudgetFor(stored.TaskStates[0].TaskID)
				require.NotNil(t, budget)
				assert.Equal(t, 2, budget.MaxAttempts)
				if refused {
					return leg, captaincode.Result{OpenShellAttempts: &captaincode.OpenShellAttemptUsage{}}, captaincode.ErrOpenShellAttemptCap
				}
				return leg, openShellHTTPResult(dir), nil
			}
			rec := httptest.NewRecorder()
			b.chatCompletions(rec, openShellHTTPRequest(t, dir, "openshell", "fix parser", false))
			stored, err := captaincode.LoadLedger()
			require.NoError(t, err)
			require.Len(t, stored.TaskStates, 1)
			task := stored.TaskStates[0]
			budget := stored.BudgetFor(task.TaskID)
			require.NotNil(t, budget)
			if refused {
				assert.Equal(t, captaincode.StateFailed, task.State)
				assert.Equal(t, captaincode.StopAttemptsExhausted, budget.StopReason)
				assert.Zero(t, budget.UnmeasuredExecutions, "an admission refusal ran nothing; its count is known")
				assert.Zero(t, budget.SettledAttempts)
				assert.Nil(t, stored.AttemptStates[0].Export)
			} else {
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, captaincode.StateSucceeded, task.State)
				assert.Empty(t, budget.StopReason)
			}
		})
	}
}

func TestOpenShellHTTPAttachedStreamingRetryKeepsTheStreamAlive(t *testing.T) {
	old := sseKeepaliveEvery
	sseKeepaliveEvery = 10 * time.Millisecond
	defer func() { sseKeepaliveEvery = old }()
	for _, outcome := range []string{"export", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			b := openShellHTTPBrain(t)
			dir := t.TempDir()
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
				if calls.Add(1) == 1 {
					close(started)
				}
				<-release
				if outcome == "failure" {
					return leg, captaincode.Result{OpenShellAttempts: &captaincode.OpenShellAttemptUsage{Workers: 1}}, errors.New("verification failed")
				}
				return leg, openShellHTTPResult(dir), nil
			}
			go func() {
				defer close(done)
				b.chatCompletions(httptest.NewRecorder(), openShellHTTPRequest(t, dir, "openshell", "fix parser", false))
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not start")
			}
			rec := httptest.NewRecorder()
			retried := make(chan struct{})
			go func() {
				defer close(retried)
				b.chatCompletions(rec, openShellHTTPRequest(t, dir, "openshell", "fix parser", true))
			}()
			time.Sleep(150 * time.Millisecond)
			close(release)
			for _, ch := range []chan struct{}{done, retried} {
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatal("a request did not finish")
				}
			}
			assert.Equal(t, int32(1), calls.Load(), "the retry attached instead of starting a second sandbox")
			body := rec.Body.String()
			assert.Contains(t, body, "already running")
			assert.Contains(t, body, ": keepalive")
			assert.Contains(t, body, "[DONE]")
			if outcome == "export" {
				assert.Contains(t, body, "exported (not applied)")
			} else {
				assert.Contains(t, body, "verification failed")
				assert.NotContains(t, body, "exported (not applied)")
			}
		})
	}
}
