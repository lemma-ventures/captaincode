package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P7 step 1: the dataset joins the decision, the runs and the settlement for
// one task, without the prompt. The fixtures below are the join's contract.

var datasetNow = time.Date(2026, time.November, 16, 9, 0, 0, 0, time.UTC)

func datasetSample() RoutingSample {
	frontier := true
	return RoutingSample{
		Decision: Decision{
			At: datasetNow, TaskID: "t-1", Task: "fix the drift sekrit-prompt-xyz in the sync loop",
			Class: ClassMedium, Domain: DomainCode, Path: PathValue, Shape: ShapeSolo,
			Chosen: LegGemini, ChosenFrontier: &frontier, TiePolicy: "cheap-capable-v1",
			Effort: EffortMedium, Attempt: 1,
		},
		Events: []Event{
			{At: datasetNow.Add(3 * time.Second), TaskID: "t-1", Leg: LegGemini, Attempt: 1, Class: ClassMedium,
				Outcome: "ok", CostUSD: 0.01, CostStatus: UsageEstimated, Quality: 7.5, Verdict: "good"},
			{At: datasetNow.Add(5 * time.Second), TaskID: "t-1", Team: "gemini+glm", Outcome: "ok"},
			{At: datasetNow.Add(90 * time.Second), TaskID: "t-1", Leg: LegCodexCLI, Attempt: 2, Class: ClassMedium,
				Outcome: "fail", CostStatus: UsageUnknown},
		},
		Outcome: &OutcomeEvidence{
			TaskID: "t-1", Status: AcceptanceAccepted, DecidedBy: DecidedByReviewer,
			SettledAt: datasetNow.Add(2 * time.Minute), Attempts: 3,
			Checks: []CheckResult{
				{Source: "gate", Passed: false, At: datasetNow.Add(40 * time.Second)},
				{Source: "solo", Passed: true, At: datasetNow.Add(60 * time.Second)},
				{Source: "gate", Passed: true, At: datasetNow.Add(80 * time.Second)},
			},
		},
	}
}

func TestRoutingDatasetJoinsDecisionRunsAndSettlement(t *testing.T) {
	charges := []Charge{
		{ID: "c-1", TaskID: "t-1", Kind: KindCall, Leg: LegGemini, Usage: Usage{CostUSD: 0.05, CostStatus: UsageMeasured}},
	}
	rows := BuildRoutingDataset([]RoutingSample{datasetSample()}, charges)
	require.Len(t, rows, 2, "the team aggregate has no leg and must not become a row")

	first := rows[0]
	assert.Equal(t, "t-1", first.TaskID)
	assert.Equal(t, ClassMedium, first.Class)
	assert.Equal(t, DomainCode, first.Domain)
	assert.Equal(t, PathValue, first.Path)
	assert.Equal(t, LegGemini, first.Chosen, "the decision's pick travels even when a later attempt ran another leg")
	assert.Equal(t, LegGemini, first.Leg)
	assert.Equal(t, 1, first.Attempt)
	assert.Equal(t, 3, first.Attempts, "the outcome's attempt count is the task's total")
	assert.Equal(t, 2, first.Repairs)
	assert.True(t, first.Frontier)
	assert.Equal(t, "cheap-capable-v1", first.TiePolicy)
	assert.Equal(t, DatasetGatePass, first.GateResult, "the last gate check decides")
	assert.Equal(t, "ok", first.RunOutcome)
	assert.Equal(t, "good", first.Verdict)
	assert.InDelta(t, 0.01, first.Cost, 1e-9)
	assert.Equal(t, UsageEstimated, first.CostStatus)
	assert.InDelta(t, 0.05, first.TaskCost, 1e-9)
	assert.True(t, first.TaskBilled, "every call row measured, so the task total is a bill")
	assert.True(t, first.Settled)
	assert.Equal(t, AcceptanceAccepted, first.Label)
	assert.Equal(t, DecidedByReviewer, first.DecidedBy)
	assert.True(t, first.Success)
	assert.Equal(t, datasetNow.Add(3*time.Second), first.At)
	assert.Equal(t, datasetNow, first.DecidedAt)

	second := rows[1]
	assert.Equal(t, LegCodexCLI, second.Leg)
	assert.Equal(t, 2, second.Attempt)
	assert.Equal(t, "fail", second.RunOutcome)
	assert.Equal(t, UsageUnknown, second.CostStatus)
	assert.Zero(t, second.Quality)
	assert.Equal(t, ClassMedium, second.Class)
	assert.Equal(t, "t-1", second.TaskID)
}

func TestRoutingDatasetKeepsThePromptOut(t *testing.T) {
	rows := BuildRoutingDataset([]RoutingSample{datasetSample()}, nil)
	require.NotEmpty(t, rows)
	raw, err := json.Marshal(rows)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sekrit-prompt-xyz", "prompt text reached the dataset")
	assert.NotContains(t, string(raw), "sync loop", "prompt text reached the dataset")

	want := PromptDigest(datasetSample().Decision.Task)
	assert.Len(t, want, 64)
	got := 0
	for _, r := range rows {
		assert.Equal(t, want, r.PromptSHA, "every row of one task joins on one digest")
		if r.PromptSHA != "" {
			got++
		}
	}
	assert.Equal(t, len(rows), got)
}

func TestRoutingDatasetNeverHashesAnEmptyPrompt(t *testing.T) {
	rows := BuildRoutingDataset([]RoutingSample{{Decision: Decision{TaskID: "t-2", At: datasetNow, Chosen: LegGLM}}}, nil)
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].PromptSHA, "an empty text must not hash to the digest of nothing")
}

func TestRoutingDatasetExportsADecisionThatNeverRan(t *testing.T) {
	rows := BuildRoutingDataset([]RoutingSample{
		{Decision: Decision{At: datasetNow, TaskID: "t-3", Task: "plan the swap", Class: ClassHigh, Chosen: LegCodexCLI, Attempt: 1}},
	}, nil)
	require.Len(t, rows, 1)
	r := rows[0]
	assert.Equal(t, 1, r.Attempt)
	assert.Equal(t, LegCodexCLI, r.Leg)
	assert.Empty(t, r.RunOutcome, "an unrun attempt has no run fields to invent")
	assert.False(t, r.Settled)
	assert.Empty(t, r.Label)
	assert.Equal(t, DatasetGateNone, r.GateResult)
	assert.Equal(t, 0, r.Repairs)
	assert.Equal(t, datasetNow, r.At, "the decision's own time stands in for the attempt time")
	assert.Equal(t, PromptDigest("plan the swap"), r.PromptSHA)
}

func TestRoutingDatasetEventOnlySampleKeepsTheRunJoinable(t *testing.T) {
	rows := BuildRoutingDataset([]RoutingSample{
		{Events: []Event{{At: datasetNow, TaskID: "t-4", Leg: LegGLM, Attempt: 1, Outcome: "ok", CostStatus: UsageMeasured}}},
	}, nil)
	require.Len(t, rows, 1)
	assert.Equal(t, "t-4", rows[0].TaskID)
	assert.Equal(t, LegGLM, rows[0].Leg)
	assert.Equal(t, 1, rows[0].Attempts)
	assert.Equal(t, 0, rows[0].Repairs)
	assert.Empty(t, rows[0].PromptSHA, "a run without a decision carries no prompt to hash")
}

func TestRoutingDatasetGateResult(t *testing.T) {
	noGate := RoutingSample{Decision: Decision{TaskID: "t-5", At: datasetNow}}
	assert.Equal(t, DatasetGateNone, gateResultOf(noGate.Outcome))
	assert.Equal(t, DatasetGateNone, gateResultOf(&OutcomeEvidence{Checks: []CheckResult{{Source: "solo", Passed: false, At: datasetNow}}}))
	assert.Equal(t, DatasetGatePass, gateResultOf(&OutcomeEvidence{Checks: []CheckResult{{Source: "gate", Passed: true, At: datasetNow}}}))
	assert.Equal(t, DatasetGateFail, gateResultOf(&OutcomeEvidence{Checks: []CheckResult{
		{Source: "gate", Passed: true, At: datasetNow.Add(time.Minute)},
		{Source: "gate", Passed: false, At: datasetNow.Add(2 * time.Minute)},
	}}))
}

// A settled-by-silence task is a weak signal the estimator already distrusts
// (RoutingSample.Labeled). The dataset must not launder it into a label.
func TestRoutingDatasetWithholdsASilenceLabel(t *testing.T) {
	rows := BuildRoutingDataset([]RoutingSample{
		{
			Decision: Decision{At: datasetNow, TaskID: "t-6", Task: "tidy", Chosen: LegGLM},
			Events:   []Event{{At: datasetNow, TaskID: "t-6", Leg: LegGLM, Attempt: 1, Outcome: "ok"}},
			Outcome:  &OutcomeEvidence{TaskID: "t-6", Status: AcceptanceAccepted, DecidedBy: DecidedBySilence, SettledAt: datasetNow},
		},
	}, nil)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Settled, "the status did settle")
	assert.Empty(t, rows[0].Label, "silence must not become a label")
	assert.False(t, rows[0].Success)
}

func TestWriteRoutingDatasetMakesAPrivateFreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dataset.jsonl")
	rows := BuildRoutingDataset([]RoutingSample{datasetSample()}, nil)
	require.NoError(t, WriteRoutingDataset(path, rows))

	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, len(rows))
	var back DatasetRow
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &back))
	assert.Equal(t, rows[0].TaskID, back.TaskID)
	assert.Equal(t, rows[0].PromptSHA, back.PromptSHA)

	err = WriteRoutingDataset(path, rows)
	require.Error(t, err, "a dataset is a fresh artifact")
	assert.Contains(t, err.Error(), "already exists")
	_, statErr := os.Lstat(path)
	require.NoError(t, statErr)
}

func TestWriteRoutingDatasetRefusesAnEmptyPath(t *testing.T) {
	require.Error(t, WriteRoutingDataset("  ", nil))
}

func BenchmarkBuildRoutingDataset(b *testing.B) {
	var samples []RoutingSample
	frontier := true
	for i := 0; i < 400; i++ {
		at := datasetNow.Add(-time.Duration(i) * time.Hour)
		id := "t-" + string(rune('a'+i%26)) + time.Now().UTC().Format("150405")
		s := RoutingSample{
			Decision: Decision{At: at, TaskID: id, Task: "task text", Class: ClassMedium, Chosen: LegGemini, ChosenFrontier: &frontier, TiePolicy: "cheap-capable-v1"},
			Outcome:  &OutcomeEvidence{TaskID: id, Status: AcceptanceAccepted, DecidedBy: DecidedByReviewer, Attempts: 2},
		}
		for e := 1; e <= 3; e++ {
			s.Events = append(s.Events, Event{At: at.Add(time.Duration(e) * time.Second), TaskID: id, Leg: LegGemini, Attempt: e, Outcome: "ok", CostStatus: UsageMeasured})
		}
		samples = append(samples, s)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := BuildRoutingDataset(samples, nil)
		if len(rows) != 1200 {
			b.Fatalf("rows = %d, want 1200", len(rows))
		}
	}
}
