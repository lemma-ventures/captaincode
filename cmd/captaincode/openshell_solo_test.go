package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenShellRecordsMeasuredAttempts(t *testing.T) {
	for _, runErr := range []error{nil, errors.New("verification failed"), context.Canceled} {
		t.Run(fmt.Sprint(runErr), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CAPTAIN_MAX_ATTEMPTS", "5")
			t.Setenv("CAPTAIN_MAX_COST", "0")
			t.Setenv("CAPTAIN_MAX_WALLTIME", "0s")
			ledger, err := captaincode.LoadLedger()
			require.NoError(t, err)
			task, attempt, err := beginOpenShellSolo(ledger, "fix fixture", "test")
			require.NoError(t, err)
			var result captaincode.Result
			require.NoError(t, json.Unmarshal([]byte(`{"OpenShellAttempts":{"workers":1,"repairs":1,"directors":1,"unmeasured":0}}`), &result))
			require.NoError(t, recordOpenShellSolo(ledger, task, attempt, "fix fixture", result, runErr))
			stored, err := captaincode.LoadLedger()
			require.NoError(t, err)
			require.NotNil(t, stored.BudgetFor(task))
			assert.Equal(t, 3, stored.BudgetFor(task).SettledAttempts)
			assert.Zero(t, stored.BudgetFor(task).ReservedAttempts)
		})
	}
}

func TestOpenShellSoloCLIHelper(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_SOLO_HELPER") != "1" {
		t.Skip("subprocess entry")
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"captain"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("captain", flag.ExitOnError)
	main()
}

func TestOpenShellSoloCLIHasNoHostPrelude(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"explicit check", []string{"--until", "touch host-test-ran", "with", "openshell", "fix parser"}, "CAPTAIN_OPENSHELL_VERIFY"},
		{"missing configuration", []string{"with", "openshell", "fix parser"}, "CAPTAIN_OPENSHELL_PREPARED"},
		{"natural language check", []string{"with", "openshell", "fix parser until tests pass"}, "CAPTAIN_OPENSHELL_PREPARED"},
		{"parallel configuration", []string{"with", "openshell", "/openshell fix a + /openshell fix b"}, "CAPTAIN_OPENSHELL_PREPARED"},
		{"mixed parallel", []string{"with", "openshell", "/openshell fix a + /cursor fix b"}, "host and sandbox"},
		{"sequential", []string{"with", "openshell", "/openshell fix a > /openshell fix b"}, "CAPTAIN_OPENSHELL_PREPARED"},
		{"workflow gate", []string{"with", "openshell", "/openshell fix a gate: touch host-test-ran"}, "host gates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			require.NoError(t, os.Mkdir(bin, 0o700))
			for _, name := range []string{"opencode", "claude", "codex", "cursor-agent", "go"} {
				require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\ntouch \"$CAPTAIN_TEST_HOST_MARKER\"\nexit 99\n"), 0o700))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestOpenShellSoloCLIHelper$", "--"}, tc.args...)...)
			cmd.Dir = home
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "CAPTAIN_TEST_SOLO_HELPER=1", "CAPTAIN_DIRECTOR=claude", "CAPTAIN_TEST_HOST_MARKER=" + filepath.Join(home, "host-tool-ran")}
			output, err := cmd.CombinedOutput()
			require.Error(t, err)
			require.NoError(t, ctx.Err(), string(output))
			assert.Contains(t, string(output), tc.want)
			assert.NotContains(t, string(output), "no leg can run here")
			assert.NoFileExists(t, filepath.Join(home, "host-tool-ran"))
			assert.NoFileExists(t, filepath.Join(home, "host-test-ran"))
		})
	}
}

func TestOpenShellSoloRefusesHostCheckAndPrecancelledRun(t *testing.T) {
	ledger := &captaincode.Ledger{}
	err := runOpenShellSolo(context.Background(), ledger, captaincode.Workspace{}, "fix it", "touch marker", io.Discard)
	require.ErrorContains(t, err, "CAPTAIN_OPENSHELL_VERIFY")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = runOpenShellSolo(ctx, ledger, captaincode.Workspace{}, "fix it", "", io.Discard)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, ledger.Charges)
}

func TestOpenShellSoloRecordsDispatchFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPTAIN_OPENSHELL_PREPARED", "")
	ledger, err := captaincode.LoadLedger()
	require.NoError(t, err)
	var out bytes.Buffer
	err = runOpenShellSolo(context.Background(), ledger, captaincode.Workspace{Dir: t.TempDir()}, "fix it", "", &out)
	require.ErrorContains(t, err, "CAPTAIN_OPENSHELL_PREPARED")
	restored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	require.Len(t, restored.Events, 1)
	event := restored.Events[0]
	assert.Equal(t, "fail", event.Outcome)
	assert.Equal(t, captaincode.StateFailed, restored.AttemptStateFor(event.AttemptID).State)
	assert.Nil(t, restored.AttemptStateFor(event.AttemptID).Export)
	assert.NotContains(t, out.String(), "apply with:")
	assert.Contains(t, out.String(), event.TaskID)
}

func TestOpenShellSoloExportSurvivesReloadWithoutClaimingHostEdits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		state captaincode.LifecycleState
	}{
		{"success", nil, captaincode.StateSucceeded},
		{"failure", errors.New("verification failed"), captaincode.StateFailed},
		{"interrupt", captaincode.ErrInterrupted, captaincode.StateCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			ledger, err := captaincode.LoadLedger()
			require.NoError(t, err)
			ledger.RecordTaskState(captaincode.TaskState{TaskID: "task", State: captaincode.StateRunning})
			ledger.RecordAttemptState(captaincode.AttemptState{TaskID: "task", AttemptID: "attempt", State: captaincode.StateRunning, Leg: captaincode.LegOpenShell})
			res := captaincode.Result{Export: &captaincode.VerifiedExport{Repository: "/fixture", Runtime: "vm", RunRecord: "/evidence/run.json",
				Manifest: captaincode.PatchManifest{TaskID: "controller-task", BaseRevision: strings.Repeat("a", 40),
					ChangedFiles: []string{"parser.py"}, DiffPath: "/evidence/integrated.patch", DiffDigest: strings.Repeat("b", 64),
					Check: &captaincode.CheckEvidence{Command: []string{"python3", "-m", "unittest"}, Passed: true}}}}
			require.NoError(t, recordOpenShellSolo(ledger, "task", "attempt", "fix it", res, tc.err))
			restored, err := captaincode.LoadLedger()
			require.NoError(t, err)
			attempt := restored.AttemptStateFor("attempt")
			require.NotNil(t, attempt)
			assert.Equal(t, tc.state, attempt.State)
			assert.Equal(t, tc.state, restored.TaskStateFor("task").State)
			assert.Empty(t, attempt.ChangedFiles)
			assert.Empty(t, attempt.DiffPath)
			brief := restored.HandoffFor("task")
			require.NotNil(t, brief)
			if tc.err == nil {
				require.NotNil(t, attempt.Export)
				assert.Equal(t, "task", attempt.Export.Manifest.TaskID)
				assert.Equal(t, "attempt", attempt.Export.Manifest.AttemptID)
				assert.Contains(t, captaincode.FormatHandoffBrief(*brief), "exported (not applied)")
			} else {
				assert.Nil(t, attempt.Export)
				assert.Empty(t, brief.Artifacts)
			}
		})
	}
}

func TestOpenShellCancellationThatWinsTheRaceKeepsNoExport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ledger, err := captaincode.LoadLedger()
	require.NoError(t, err)
	taskID, attemptID, err := beginOpenShellSolo(ledger, "fix it", "test")
	require.NoError(t, err)
	require.NoError(t, ledger.TransitionAttempt(attemptID, captaincode.StateCancelRequested))
	res := captaincode.Result{Text: "verified export", Export: &captaincode.VerifiedExport{
		Repository: "/fixture", Runtime: "vm", RunRecord: "/evidence/run.json",
		Manifest: captaincode.PatchManifest{BaseRevision: strings.Repeat("a", 40), ChangedFiles: []string{"a.txt"}},
	}}
	require.Error(t, recordOpenShellSolo(ledger, taskID, attemptID, "fix it", res, nil))
	attempt := ledger.AttemptStateFor(attemptID)
	assert.Nil(t, attempt.Export)
	assert.Equal(t, captaincode.StateCancelRequested, attempt.State)
}

func TestOpenShellUnchangedEvidenceSurvivesReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ledger, err := captaincode.LoadLedger()
	require.NoError(t, err)
	taskID, attemptID, err := beginOpenShellSolo(ledger, "review snapshot", "test")
	require.NoError(t, err)
	res := captaincode.Result{Text: "verified unchanged snapshot; nothing to apply", Export: &captaincode.VerifiedExport{
		Repository: "/fixture", Runtime: "vm", RunRecord: "/evidence/run.json",
		Manifest: captaincode.PatchManifest{BaseRevision: strings.Repeat("a", 40), DiffPath: "/evidence/integrated.patch",
			DiffDigest: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			Check:      &captaincode.CheckEvidence{Command: []string{"python3", "-m", "unittest"}, Passed: true}},
	}}
	require.NoError(t, recordOpenShellSolo(ledger, taskID, attemptID, "review snapshot", res, nil))
	ledger, err = captaincode.LoadLedger()
	require.NoError(t, err)
	attempt := ledger.AttemptStateFor(attemptID)
	require.NotNil(t, attempt.Export)
	assert.Equal(t, captaincode.StateSucceeded, attempt.State)
	brief := captaincode.FormatHandoffBrief(*ledger.HandoffFor(taskID))
	assert.Contains(t, brief, "verified unchanged snapshot")
	assert.Contains(t, brief, "nothing to apply")
	assert.NotContains(t, brief, "before applying")
}
