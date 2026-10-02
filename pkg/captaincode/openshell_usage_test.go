package captaincode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenShellMeasuredAttemptCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, report string
		repairs      int
		want         OpenShellAttemptUsage
	}{
		{"before model", `{"worker_attempts":0}`, 1, OpenShellAttemptUsage{}},
		{"first try", `{"worker_attempts":1}`, 1, OpenShellAttemptUsage{Workers: 1}},
		{"repair", `{"worker_attempts":2}`, 1, OpenShellAttemptUsage{Workers: 1, Repairs: 1}},
		{"missing", `{}`, 1, OpenShellAttemptUsage{Unmeasured: 1}},
		{"null", `{"worker_attempts":null}`, 1, OpenShellAttemptUsage{Unmeasured: 1}},
		{"negative", `{"worker_attempts":-1}`, 1, OpenShellAttemptUsage{Unmeasured: 1}},
		{"too many", `{"worker_attempts":3}`, 1, OpenShellAttemptUsage{Unmeasured: 1}},
		{"unadmitted repair", `{"worker_attempts":2}`, 0, OpenShellAttemptUsage{Unmeasured: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var report OpenShellReport
			require.NoError(t, json.Unmarshal([]byte(tc.report), &report))
			run := OpenShellRun{Tasks: []*OpenShellResult{{Report: &report, task: OpenShellTask{RepairAttempts: tc.repairs}}}}
			assert.Equal(t, tc.want, *run.measuredAttempts())
		})
	}
	run := OpenShellRun{Tasks: []*OpenShellResult{nil, {NotStarted: true}, {}}}
	assert.Equal(t, &OpenShellAttemptUsage{Unmeasured: 2}, run.measuredAttempts())
	zero, two, invalid := 0, 2, 3
	run = OpenShellRun{Rulings: []OpenShellRuling{{Attempts: &zero}, {Attempts: &two}, {}, {Attempts: &invalid}}}
	assert.Equal(t, &OpenShellAttemptUsage{Directors: 2, Unmeasured: 2}, run.measuredAttempts())
}

func TestOpenShellWorkerAttemptReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name, prompt string
		want         OpenShellAttemptUsage
		failed       bool
	}{
		{"unused repair", `{"write":{"a.txt":"changed\n"}}`, OpenShellAttemptUsage{Workers: 1}, false},
		{"successful repair", `{"write":{"a.txt":"changed\n"},"attempts":2}`, OpenShellAttemptUsage{Workers: 1, Repairs: 1}, false},
		{"failed repair", `{"fail":true,"attempts":2}`, OpenShellAttemptUsage{Workers: 1, Repairs: 1}, true},
		{"missing report", `not json`, OpenShellAttemptUsage{Unmeasured: 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeOpenShell(t)
			r.RequireAll = true
			task := fakeOpenShellTask("worker", "nim", tc.prompt, "all", "a.txt")
			task.RepairAttempts = 1
			result, err := runOpenShellTeam(context.Background(), r, OpenShellTeam{Schema: 1, ID: "usage", Tasks: []OpenShellTask{task}}, nil)
			if tc.failed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, &tc.want, result.OpenShellAttempts)
			var saved OpenShellRun
			_, err = decodeOpenShellSequence(filepath.Join(r.RunDir, "run.json"), &saved)
			require.NoError(t, err)
			assert.Equal(t, &tc.want, saved.AttemptUsage)
			assert.Equal(t, 2, saved.AttemptBudget.Required)
		})
	}
}

func TestOpenShellDirectorAttemptReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		calls  int
		failed bool
	}{
		{"ok", 1, false}, {"retry", 2, false}, {"error", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeOpenShell(t)
			r.RequireAll = true
			logs := fakeClaude(t, tc.name)
			r.Director, r.DirectorName = ToolLessClaudeDirector, "claude"
			team := OpenShellTeam{Schema: 1, ID: "usage", Tasks: []OpenShellTask{
				fakeOpenShellTask("t2", "nim", `{"write":{"a.txt":"one\n"}}`, "all", "a.txt"),
				fakeOpenShellTask("t3", "nim", `{"write":{"a.txt":"two\n"}}`, "all", "a.txt"),
			}}
			result, err := runOpenShellTeam(context.Background(), r, team, nil)
			if tc.failed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, &OpenShellAttemptUsage{Workers: 2, Directors: tc.calls}, result.OpenShellAttempts)
			calls, err := filepath.Glob(filepath.Join(logs, "call*"))
			require.NoError(t, err)
			assert.Len(t, calls, tc.calls)
		})
	}
}

func TestOpenShellAttemptsSurviveResumeWithoutRepeatingCompletedUsage(t *testing.T) {
	r := pauseOpenShellSequence(t)
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err)
	assert.Equal(t, &OpenShellAttemptUsage{Workers: 2}, result.OpenShellAttempts)
	again, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err)
	assert.Equal(t, result.OpenShellAttempts, again.OpenShellAttempts)
}

func TestOpenShellRecoveryRejectsAlteredAttemptUsage(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "stage", true: "cumulative"}[complete], func(t *testing.T) {
			r := pauseOpenShellSequence(t)
			dir := filepath.Join(r.RunDir, "stage-1")
			if complete {
				_, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
				require.NoError(t, err)
				dir = r.RunDir
			}
			var run OpenShellRun
			_, err := decodeOpenShellSequence(filepath.Join(dir, "run.json"), &run)
			require.NoError(t, err)
			run.AttemptUsage.Workers++
			require.NoError(t, saveOpenShellSequence(dir, &run))
			result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
			require.ErrorContains(t, err, "attempt usage does not match")
			assert.Nil(t, result.Export)
			if !complete {
				assert.NoDirExists(t, filepath.Join(r.RunDir, "stage-2"))
			}
		})
	}
}

func TestOpenShellLedgerAttemptUsageIsIdempotentAndUnknownIsNotZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ledger, err := LoadLedger()
	require.NoError(t, err)
	ledger.RecordAttemptState(AttemptState{TaskID: "task", AttemptID: "attempt", Leg: LegOpenShell, State: StateRunning})
	usage := &OpenShellAttemptUsage{Workers: 1, Repairs: 1, Directors: 2, Unmeasured: 1}
	require.NoError(t, ledger.ReconcileOpenShellAttempts("attempt", usage))
	require.NoError(t, ledger.Save())
	ledger, err = LoadLedger()
	require.NoError(t, err)
	require.NoError(t, ledger.ReconcileOpenShellAttempts("attempt", usage))
	budget := ledger.BudgetFor("task")
	require.NotNil(t, budget)
	assert.Equal(t, 4, budget.SettledAttempts)
	assert.Equal(t, 1, budget.UnmeasuredExecutions)
	assert.Equal(t, StopAttemptUsageUnknown, budget.StopReason)
	assert.Contains(t, FormatBudget(*budget), "settled is a lower bound")
	assert.False(t, budget.Reserve(1))
	require.ErrorContains(t, ledger.ReconcileOpenShellAttempts("attempt", &OpenShellAttemptUsage{Workers: 2}), "settled attempt usage changed")
	assert.Equal(t, 4, budget.SettledAttempts)
	usage.Workers = 42
	assert.Equal(t, 1, ledger.AttemptStateFor("attempt").OpenShellAttempts.Workers)
	_, _, err = OpenShellBudgetContext(context.Background(), budget)
	require.Error(t, err)
	assert.False(t, errors.Is(err, context.Canceled))
	assert.FileExists(t, filepath.Join(os.Getenv("HOME"), ".captaincode", "state.json"))
}

func TestOpenShellAttemptUsageRejectsInvalidCountersWithoutMutation(t *testing.T) {
	for _, usage := range []OpenShellAttemptUsage{
		{Workers: -1}, {Workers: 1, Repairs: 2}, {Directors: -1},
		{Workers: 1000000}, {Directors: 1000000}, {Unmeasured: -1}, {Unmeasured: 1000000},
	} {
		ledger := &Ledger{}
		ledger.RecordAttemptState(AttemptState{TaskID: "task", AttemptID: "attempt", Leg: LegOpenShell, State: StateRunning})
		require.ErrorContains(t, ledger.ReconcileOpenShellAttempts("attempt", &usage), "invalid attempt usage")
		assert.Nil(t, ledger.AttemptStateFor("attempt").OpenShellAttempts)
		assert.Empty(t, ledger.Budgets)
	}
	ledger := &Ledger{}
	ledger.RecordAttemptState(AttemptState{TaskID: "task", AttemptID: "attempt", Leg: LegClaude, State: StateRunning})
	require.ErrorContains(t, ledger.ReconcileOpenShellAttempts("attempt", nil), "sandbox attempt")
	assert.Empty(t, ledger.Budgets)
}

func TestOpenShellRecoveryReconstructsLegacyUsage(t *testing.T) {
	r := pauseOpenShellSequence(t)
	dir := filepath.Join(r.RunDir, "stage-1")
	var stage OpenShellRun
	_, err := decodeOpenShellSequence(filepath.Join(dir, "run.json"), &stage)
	require.NoError(t, err)
	stage.AttemptUsage = nil
	require.NoError(t, saveOpenShellSequence(dir, &stage))
	result, err := ResumeOpenShellSequence(context.Background(), r.RunDir, nil)
	require.NoError(t, err)
	assert.Equal(t, &OpenShellAttemptUsage{Workers: 2}, result.OpenShellAttempts)
}
