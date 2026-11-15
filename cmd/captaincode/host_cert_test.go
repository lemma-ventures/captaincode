package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostCertDynamicGates(t *testing.T) {
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "stub", DurationMs: 1}, nil
	}
	server := httptest.NewServer(http.HandlerFunc(b.taskAPIHTTP))
	defer server.Close()
	restart := func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.ledger.ReconcileOnStartup()
		return b.ledger.Save()
	}
	report := runHostCertDynamic(captaincode.NewTaskClient(server.URL, ""), ".", restart)
	require.Len(t, report.Steps, 5)
	for _, step := range report.Steps {
		assert.Equal(t, "pass", step.Status, step.Name+": "+step.Detail)
	}
	assert.Zero(t, report.Failed)
}

func TestHostCertDynamicGatesReconnect(t *testing.T) {
	b := teamBrain()
	server := httptest.NewServer(http.HandlerFunc(b.taskAPIHTTP))
	defer server.Close()
	report := runHostCertDynamic(captaincode.NewTaskClient(server.URL, ""), ".", nil)
	require.Len(t, report.Steps, 5)
	for _, step := range report.Steps {
		assert.Equal(t, "pass", step.Status, step.Name+": "+step.Detail)
	}
	assert.Zero(t, report.Failed)
}

func TestHostCertRefusesLiveBrainByDefault(t *testing.T) {
	for _, target := range []string{
		"http://127.0.0.1:14097",
		"http://localhost:14097",
		"http://[::1]:14097",
	} {
		assert.Error(t, validateHostCertBrain(target, false), target)
		assert.NoError(t, validateHostCertBrain(target, true), target)
	}
	assert.NoError(t, validateHostCertBrain("http://127.0.0.1:24097", false))
}

func TestHostEditorConfigJSON(t *testing.T) {
	binary := filepath.Join(string(filepath.Separator), "opt", "captain", "bin", "captain")
	tests := []struct {
		editor string
		root   string
	}{
		{editor: "cursor", root: "mcpServers"},
		{editor: "vscode", root: "servers"},
		{editor: "zed", root: "context_servers"},
	}
	for _, tt := range tests {
		t.Run(tt.editor, func(t *testing.T) {
			data, err := hostEditorConfigJSON(tt.editor, binary)
			require.NoError(t, err)
			require.True(t, json.Valid(data))
			var config map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &config))
			require.Contains(t, config, tt.root)
			var servers map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(config[tt.root], &servers))
			require.Len(t, servers, 1)
			require.Contains(t, servers, "captain")
			assert.Contains(t, string(servers["captain"]), binary)
			assert.Contains(t, string(servers["captain"]), "task")
			assert.Contains(t, string(servers["captain"]), "mcp")
		})
	}
	_, err := hostEditorConfigJSON("unknown", binary)
	assert.Error(t, err)
	_, err = hostEditorConfigJSON("cursor", "captain")
	assert.Error(t, err)
}

func BenchmarkHostEditorConfigJSON(b *testing.B) {
	binary := filepath.Join(string(filepath.Separator), "opt", "captain", "bin", "captain")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := hostEditorConfigJSON("vscode", binary); err != nil {
			b.Fatal(err)
		}
	}
}

func TestHostCertSubmissionSurvivesLedgerReload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	b := teamBrain()
	loaded, err := captaincode.LoadLedger()
	require.NoError(t, err)
	b.ledger = loaded
	server := httptest.NewServer(http.HandlerFunc(b.taskAPIHTTP))
	client := captaincode.NewTaskClient(server.URL, "")
	root, err := filepath.Abs(".")
	require.NoError(t, err)
	revision := captaincode.CurrentRevision(root)
	require.NotEmpty(t, revision)
	request := captaincode.SubmitRequest{
		Prompt:       "certify durable retry",
		ProjectRoot:  root,
		BaseRevision: revision,
		Limits:       captaincode.ResourceLimits{MaxAttempts: 2, MaxCostUSD: 0.25, MaxWallMs: 1000},
		Permissions:  captaincode.PermissionPolicy{ReadOnly: true},
	}
	taskID := captaincode.NewTaskID()
	submitted, taskErr := client.SubmitWithID(taskID, request)
	require.Nil(t, taskErr)
	require.NotEmpty(t, submitted.Attempt)
	server.Close()
	reloaded, err := captaincode.LoadLedger()
	require.NoError(t, err)
	b.ledger = reloaded
	b.ledger.ReconcileOnStartup()
	server = httptest.NewServer(http.HandlerFunc(b.taskAPIHTTP))
	defer server.Close()
	client = captaincode.NewTaskClient(server.URL, "")
	inspection, taskErr := client.Inspect(taskID, captaincode.InspectRequest{IncludeBudget: true})
	require.Nil(t, taskErr)
	require.Equal(t, captaincode.StateInterrupted, inspection.Task.State)
	require.Len(t, inspection.Attempts, 1)
	require.Equal(t, captaincode.StateInterrupted, inspection.Attempts[0].State)
	require.NotNil(t, inspection.Budget)
	assert.Equal(t, 2, inspection.Budget.MaxAttempts)
	resumed, taskErr := client.Resume(taskID, submitted.Attempt, "certification restart")
	require.Nil(t, taskErr)
	assert.Equal(t, taskID, resumed.TaskID)
	assert.NotNil(t, resumed.Budget)
	retried, taskErr := client.SubmitWithID(taskID, request)
	require.Nil(t, taskErr)
	assert.Equal(t, taskID, retried.TaskID)
	widened := request
	widened.Permissions.ReadOnly = false
	_, taskErr = client.SubmitWithID(taskID, widened)
	require.NotNil(t, taskErr)
	assert.Equal(t, captaincode.ErrConflict, taskErr.Code)
}

func TestHostCertCancelIsDurable(t *testing.T) {
	b := teamBrain()
	server := httptest.NewServer(http.HandlerFunc(b.taskAPIHTTP))
	defer server.Close()
	client := captaincode.NewTaskClient(server.URL, "")
	checklist := captaincode.NewCertChecklist(captaincode.NewPiAdapter(client))
	require.NoError(t, checklist.CheckCancelDuringExecution("certify cancellation", captaincode.SubmitOptions{}))
	require.Eventually(t, func() bool {
		return len(b.ledger.TaskStates) == 1 && b.ledger.TaskStates[0].State == captaincode.StateCancelled
	}, time.Second, time.Millisecond)
}
