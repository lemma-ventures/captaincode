package captaincode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordOutcome_CreatesAndUpdates(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{
		TaskID: "t1",
		Task:   "add pagination",
		Leg:    LegClaude,
		Status: AcceptancePending,
	})
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Equal(t, "t1", o.TaskID)
	assert.Equal(t, AcceptancePending, o.Status)
	assert.Equal(t, OutcomeVersion, o.Version)
	assert.Equal(t, "add pagination", o.Task)
}

func TestRecordOutcome_UpdatesInPlace(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptanceAccepted})
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Equal(t, AcceptanceAccepted, o.Status)
	assert.Len(t, l.Outcomes, 1)
}

func TestRecordCheckResult_CreatesOutcomeIfMissing(t *testing.T) {
	l := &Ledger{}
	l.RecordCheckResult("t1", CheckResult{
		Command:  "go test ./...",
		ExitCode: 0,
		Passed:   true,
		Source:   "gate",
	})
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Len(t, o.Checks, 1)
	assert.True(t, o.Checks[0].Passed)
	assert.True(t, o.AllChecksPassed())
}

func TestRecordCheckResult_AppendsToExisting(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	l.RecordCheckResult("t1", CheckResult{Command: "go test", ExitCode: 0, Passed: true})
	l.RecordCheckResult("t1", CheckResult{Command: "go vet", ExitCode: 1, Passed: false})
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Len(t, o.Checks, 2)
	assert.False(t, o.AllChecksPassed())
}

func TestAllChecksPassed_NoChecks(t *testing.T) {
	o := OutcomeEvidence{Checks: nil}
	assert.False(t, o.AllChecksPassed(), "no checks is not all-passed")
}

func TestRecordTaskReview_Accept(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	err := l.RecordTaskReview("t1", ReviewAccept, "alice", "looks good", false)
	require.NoError(t, err)
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Equal(t, AcceptanceAccepted, o.Status)
	assert.NotNil(t, o.Review)
	assert.Equal(t, "accept", o.Review.Verdict)
	assert.Equal(t, "alice", o.Review.Reviewer)
	assert.False(t, o.AcceptedAt.IsZero())
}

func TestRecordTaskReview_Reject(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	err := l.RecordTaskReview("t1", ReviewReject, "bob", "missing tests", false)
	require.NoError(t, err)
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Equal(t, AcceptanceRejected, o.Status)
	assert.False(t, o.RejectedAt.IsZero())
}

func TestRecordTaskReview_DuplicateWithoutAmend(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	require.NoError(t, l.RecordTaskReview("t1", ReviewAccept, "alice", "", false))
	err := l.RecordTaskReview("t1", ReviewReject, "bob", "", false)
	assert.Error(t, err, "second review without amend should fail")
}

func TestRecordTaskReview_AmendPreservesHistory(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	require.NoError(t, l.RecordTaskReview("t1", ReviewAccept, "alice", "ok", false))
	require.NoError(t, l.RecordTaskReview("t1", ReviewReject, "alice", "actually broken", true))
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Equal(t, AcceptanceRejected, o.Status)
	assert.Len(t, o.ReviewHistory, 1)
	assert.Equal(t, "accept", o.ReviewHistory[0].Verdict)
	assert.True(t, o.Review.Amended)
}

func TestRecordTaskReview_CreatesOutcomeIfMissing(t *testing.T) {
	l := &Ledger{}
	err := l.RecordTaskReview("t1", ReviewAccept, "alice", "", false)
	require.NoError(t, err)
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Equal(t, AcceptanceAccepted, o.Status)
}

func TestRecordTaskReview_EmptyReviewer(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	err := l.RecordTaskReview("t1", ReviewAccept, "  ", "", false)
	assert.Error(t, err)
}

func TestRecordTaskReview_InvalidVerdict(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	err := l.RecordTaskReview("t1", "maybe", "alice", "", false)
	assert.Error(t, err)
}

func TestRecordCorrection(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptancePending})
	l.RecordCorrection("t1", 15, "fixed import")
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Len(t, o.Corrections, 1)
	assert.Equal(t, 15, o.TotalCorrectionMinutes())
}

func TestRecordCorrection_SkipsInvalid(t *testing.T) {
	l := &Ledger{}
	l.RecordCorrection("t1", 0, "zero minutes")
	assert.Nil(t, l.OutcomeFor("t1"), "zero minutes should not create an outcome")
	l.RecordCorrection("", 10, "no task")
	assert.Nil(t, l.OutcomeFor(""))
}

func TestRecordRegression(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptanceAccepted, AcceptedAt: time.Now()})
	l.RecordRegression("t1", "broke after upstream merge", "ci")
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Equal(t, AcceptanceRegressed, o.Status)
	assert.NotNil(t, o.Regression)
	assert.Equal(t, "broke after upstream merge", o.Regression.Reason)
}

func TestRecordRegression_PreservesNonAccepted(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptanceRejected})
	l.RecordRegression("t1", "still broken", "ci")
	o := l.OutcomeFor("t1")
	require.NotNil(t, o)
	assert.Equal(t, AcceptanceRejected, o.Status, "regression does not change non-accepted status")
	assert.NotNil(t, o.Regression)
}

func TestOutcomeCoverage(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptanceAccepted})
	l.RecordOutcome(OutcomeEvidence{TaskID: "t2", Status: AcceptanceRejected})
	l.RecordOutcome(OutcomeEvidence{TaskID: "t3", Status: AcceptancePending})
	l.RecordCorrection("t1", 10, "")
	l.RecordCorrection("t1", 5, "")
	total, byStatus, corr := l.OutcomeCoverage()
	assert.Equal(t, 3, total)
	assert.Equal(t, 1, byStatus[AcceptanceAccepted])
	assert.Equal(t, 1, byStatus[AcceptanceRejected])
	assert.Equal(t, 1, byStatus[AcceptancePending])
	assert.Equal(t, 15, corr)
}

func TestAcceptedOutcomes(t *testing.T) {
	l := &Ledger{}
	l.RecordOutcome(OutcomeEvidence{TaskID: "t1", Status: AcceptanceAccepted, AcceptedAt: time.Now()})
	l.RecordOutcome(OutcomeEvidence{TaskID: "t2", Status: AcceptanceRejected})
	l.RecordOutcome(OutcomeEvidence{TaskID: "t3", Status: AcceptanceRegressed, AcceptedAt: time.Now().Add(-time.Hour)})
	accepted := l.AcceptedOutcomes()
	assert.Len(t, accepted, 2)
}

func TestMaxOutcomes(t *testing.T) {
	l := &Ledger{}
	for i := 0; i < maxOutcomes+10; i++ {
		l.RecordOutcome(OutcomeEvidence{
			TaskID: "t" + string(rune('a'+(i%26))) + string(rune('0'+(i/26%10))),
			Status: AcceptancePending,
		})
	}
	assert.Len(t, l.Outcomes, maxOutcomes)
}

func TestFormatOutcomeEvidence(t *testing.T) {
	o := OutcomeEvidence{
		TaskID: "t1",
		Task:   "add pagination",
		Leg:    LegClaude,
		Status: AcceptanceAccepted,
		Checks: []CheckResult{
			{Command: "go test", ExitCode: 0, Passed: true},
			{Command: "go vet", ExitCode: 0, Passed: true},
		},
		Review:      &TaskReview{Verdict: "accept", Reviewer: "alice", Note: "clean"},
		Corrections: []CorrectionRecord{{Minutes: 5, Reason: "typo"}},
		AcceptedAt:  time.Now(),
	}
	s := FormatOutcomeEvidence(o)
	assert.Contains(t, s, "accepted")
	assert.Contains(t, s, "all 2 passed")
	assert.Contains(t, s, "alice")
	assert.Contains(t, s, "5 minutes")
}

func TestFormatOutcomeSummary(t *testing.T) {
	outcomes := []OutcomeEvidence{
		{TaskID: "t1", Status: AcceptanceAccepted, Checks: []CheckResult{{Passed: true}}, Review: &TaskReview{Verdict: "accept"}, Corrections: []CorrectionRecord{{Minutes: 5}}},
		{TaskID: "t2", Status: AcceptanceRejected},
	}
	s := FormatOutcomeSummary(outcomes)
	assert.Contains(t, s, "TASK")
	assert.Contains(t, s, "t1")
	assert.Contains(t, s, "accepted")
}

func TestFormatOutcomeSummary_Empty(t *testing.T) {
	s := FormatOutcomeSummary(nil)
	assert.Contains(t, s, "no outcome evidence")
}
