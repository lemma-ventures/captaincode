package captaincode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewAndRegressionQueueDistinctOutcomeEvents(t *testing.T) {
	ledger := &Ledger{}
	ledger.MergeMemoryExposure("t1", TaskExposure{
		BrainRoot:      "/brains/main",
		SourceRevision: "abc123",
		Entries:        []ExposureEntry{{Lesson: "lesson-1", Arm: "shown", P: 0.5, Rendered: boolPtr(true)}},
		Guardrails:     []string{"failure:abcd"},
	})
	require.NoError(t, ledger.RecordTaskReview("t1", ReviewAccept, "reviewer", "", false))
	first := ledger.TakeOutcomeNotices()
	require.Len(t, first, 1)
	assert.Equal(t, "task_outcome", first[0].Event.Kind)
	assert.Equal(t, "accepted", first[0].Event.Outcome)
	assert.Equal(t, "task accepted, decided by reviewer", first[0].Event.Text)
	assert.Equal(t, "abc123", first[0].Event.SourceRevision)
	assert.Equal(t, []string{"lesson-1", "failure:abcd"}, first[0].Event.Evidence)
	require.Len(t, first[0].Event.Exposure, 1)
	assert.Equal(t, "lesson-1", first[0].Event.Exposure[0].Lesson)
	require.Error(t, ledger.RecordTaskReview("t1", ReviewReject, "reviewer", "", false))
	assert.Empty(t, ledger.TakeOutcomeNotices())

	ledger.RecordRegression("t1", "broke after the merge", "ci")
	second := ledger.TakeOutcomeNotices()
	require.Len(t, second, 1)
	assert.Equal(t, "rejected", second[0].Event.Outcome)
	assert.Contains(t, second[0].Event.ID, ":regressed:")
	assert.NotEqual(t, first[0].Event.ID, second[0].Event.ID)
	assert.Equal(t, first[0].Event.Exposure, second[0].Event.Exposure)
}

func TestSettleQueuesOneEventAndASecondSettleQueuesNothing(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	ledger := &Ledger{}
	ledger.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending, UpdatedAt: now,
		Checks: []CheckResult{{Command: "go test ./...", Source: "gate", Passed: false, ExitCode: 1, At: now}}})
	require.Equal(t, 1, ledger.SettleOutcomes(now))
	notices := ledger.TakeOutcomeNotices()
	require.Len(t, notices, 1)
	assert.Equal(t, "rejected", notices[0].Event.Outcome)
	assert.Equal(t, "unavailable", notices[0].Event.SourceRevision)
	assert.Contains(t, notices[0].Event.Text, "decided by checks")
	id := notices[0].Event.ID
	require.Equal(t, 0, ledger.SettleOutcomes(now.Add(time.Hour)))
	assert.Empty(t, ledger.TakeOutcomeNotices())
	ledger.queueOutcome(ledger.OutcomeFor("t1"))
	again := ledger.TakeOutcomeNotices()
	require.Len(t, again, 1)
	assert.Equal(t, id, again[0].Event.ID, "the same transition keeps the same event ID")
}
