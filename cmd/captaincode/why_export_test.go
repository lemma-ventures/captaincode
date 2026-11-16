package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P7 step 1's command surface: the export writes a private, fresh file of
// JSONL rows and never the prompt it read.

func exportLedger(prompt string) *captaincode.Ledger {
	l := &captaincode.Ledger{}
	now := time.Now().UTC()
	l.RecordDecision(captaincode.Decision{
		At: now, TaskID: "t-export", Task: prompt, Class: captaincode.ClassMedium,
		Path: captaincode.PathValue, Chosen: captaincode.LegGemini, TiePolicy: "cheap-capable-v1",
	})
	l.Record(captaincode.Event{TaskID: "t-export", Leg: captaincode.LegGemini, Attempt: 1,
		Outcome: "ok", CostStatus: captaincode.UsageMeasured, Verdict: "good"})
	l.RecordOutcome(captaincode.OutcomeEvidence{TaskID: "t-export",
		Status: captaincode.AcceptanceAccepted, DecidedBy: captaincode.DecidedByReviewer})
	return l
}

func TestExportDatasetWritesAPrivatePromptFreeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dataset.jsonl")
	l := exportLedger("rotate the cafebabe-credentials fixture secret")

	rows, err := exportDataset(l, path)
	require.NoError(t, err)
	assert.Equal(t, 1, rows)

	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "cafebabe-credentials")
	assert.Contains(t, string(data), `"prompt_sha256":`)
	assert.Contains(t, string(data), `"gate_result":"none"`)
	assert.Contains(t, string(data), `"leg":"gemini"`)

	decoded := strings.Count(string(data), "\n")
	assert.Equal(t, rows, decoded)
	var back captaincode.DatasetRow
	require.NoError(t, json.Unmarshal([]byte(strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]), &back))
	assert.Equal(t, "t-export", back.TaskID)

	assert.Contains(t, exportSummary(path, rows), "wrote 1 dataset rows to "+path)
}

func TestExportDatasetRefusesAFileThatExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dataset.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("keep me\n"), 0o600))

	_, err := exportDataset(exportLedger("anything"), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "keep me\n", string(data), "the existing file must be untouched")
}

func TestExportDatasetStreamsToStdoutForAPipeline(t *testing.T) {
	out := captureStdout(t, func() {
		rows, err := exportDataset(exportLedger("plain prompt"), "-")
		require.NoError(t, err)
		require.Equal(t, 1, rows)
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 1, "a stdout export prints rows and nothing else")
	var row captaincode.DatasetRow
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &row))
	assert.Equal(t, "t-export", row.TaskID)
	assert.NotContains(t, out, "plain prompt")
}

func TestWhyRefusesExportCombinedWithShare(t *testing.T) {
	require.Error(t, whyExportFlags(true, "dataset.jsonl"), "the share report and an export are different reads")
	require.NoError(t, whyExportFlags(true, ""))
	require.NoError(t, whyExportFlags(false, "dataset.jsonl"))
	require.NoError(t, whyExportFlags(false, ""))
}
