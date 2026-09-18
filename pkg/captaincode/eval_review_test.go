package captaincode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// blindedResult runs a one-task suite whose task needs a human verdict, and
// returns the result with exactly one pending execution.
func blindedResult(t *testing.T) *EvalResult {
	t.Helper()
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].Review = "blinded"
	res, err := RunSuite(context.Background(), s, EvalOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pending()) != 1 {
		t.Fatalf("want 1 pending execution, got %d", len(res.Pending()))
	}
	return res
}

func TestPendingItemShowsTheWorkButNotTheArm(t *testing.T) {
	res := blindedResult(t)
	p := res.Pending()[0]
	if p.Alias == "" || strings.Contains(p.Key(), res.Executions[0].Arm) {
		t.Fatalf("reviewer can see the arm: %s", p.Key())
	}
	if p.Prompt == "" || p.Dir == "" {
		t.Fatalf("reviewer cannot judge the change: prompt=%q dir=%q", p.Prompt, p.Dir)
	}
}

func TestAcceptedReviewEntersTheNumerator(t *testing.T) {
	res := blindedResult(t)
	p := res.Pending()[0]
	if err := res.RecordReview(p.TaskID, p.Alias, p.Repeat, ReviewAccept, "romain", "reads correctly", false); err != nil {
		t.Fatal(err)
	}
	rep := res.Report()
	if rep.Arms[0].Accepted != 1 || rep.Arms[0].PendingReview != 0 {
		t.Fatalf("verdict did not move the execution: %+v", rep.Arms[0])
	}
	// The distinction the column exists for: this acceptance was a person's.
	if rep.Arms[0].ReviewAccepted != 1 {
		t.Fatalf("human acceptance not counted as such: %+v", rep.Arms[0])
	}
	if len(res.Pending()) != 0 {
		t.Fatal("execution still listed as owing a verdict")
	}
}

func TestRejectedReviewStaysInTheDenominator(t *testing.T) {
	res := blindedResult(t)
	p := res.Pending()[0]
	if err := res.RecordReview(p.TaskID, p.Alias, p.Repeat, ReviewReject, "romain", "weakens the assertion", false); err != nil {
		t.Fatal(err)
	}
	rep := res.Report()
	if rep.Arms[0].Rejected != 1 || rep.Arms[0].Accepted != 0 || rep.Arms[0].Executions != 1 {
		t.Fatalf("rejection mis-filed: %+v", rep.Arms[0])
	}
	if rep.Arms[0].Defined {
		t.Fatal("ratios defined with nothing accepted")
	}
}

// Review decides sufficiency, never necessity: a run the checks rejected is
// not a reviewer's to overturn.
func TestReviewCannotOverturnACheckedRejection(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Arms[0].Run = []string{"sh", "-c", "printf still-wrong > answer.txt"}
	res, err := RunSuite(context.Background(), s, EvalOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Executions[0].Status != EvalRejected {
		t.Fatalf("fixture did not reject: %s", res.Executions[0].Status)
	}
	e := res.Executions[0]
	if err := res.RecordReview(e.TaskID, e.ArmAlias, e.Repeat, ReviewAccept, "romain", "", false); err == nil {
		t.Fatal("a checked rejection was reviewed into acceptance")
	}
}

func TestSecondVerdictNeedsAnAmendAndIsMarked(t *testing.T) {
	res := blindedResult(t)
	p := res.Pending()[0]
	if err := res.RecordReview(p.TaskID, p.Alias, p.Repeat, ReviewAccept, "romain", "", false); err != nil {
		t.Fatal(err)
	}
	if err := res.RecordReview(p.TaskID, p.Alias, p.Repeat, ReviewReject, "romain", "", false); err == nil {
		t.Fatal("a verdict was replaced silently")
	}
	if err := res.RecordReview(p.TaskID, p.Alias, p.Repeat, ReviewReject, "someone-else", "on reflection", true); err != nil {
		t.Fatal(err)
	}
	rev := res.Executions[0].Review
	if rev == nil || !rev.Amended || rev.Verdict != ReviewReject {
		t.Fatalf("amendment not recorded as one: %+v", rev)
	}
}

func TestVerdictNeedsAReviewerAndAKnownValue(t *testing.T) {
	res := blindedResult(t)
	p := res.Pending()[0]
	if err := res.RecordReview(p.TaskID, p.Alias, p.Repeat, ReviewAccept, "  ", "", false); err == nil {
		t.Fatal("anonymous acceptance allowed")
	}
	if err := res.RecordReview(p.TaskID, p.Alias, p.Repeat, "maybe", "romain", "", false); err == nil {
		t.Fatal("unknown verdict allowed")
	}
	if err := res.RecordReview(p.TaskID, "arm-Z", p.Repeat, ReviewAccept, "romain", "", false); err == nil {
		t.Fatal("verdict recorded against an execution that does not exist")
	}
}

func TestReviewHistorySurvivesAmendmentsAndExport(t *testing.T) {
	res := blindedResult(t)
	p := res.Pending()[0]
	var history []EvalReview
	for i, verdict := range []string{ReviewAccept, ReviewReject, ReviewAccept} {
		if err := res.RecordReview(p.TaskID, p.Alias, p.Repeat, verdict, "reviewer", verdict, i > 0); err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(history)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(res.Executions[0].ReviewHistory)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("lost prior verdicts: %+v", res.Executions[0].ReviewHistory)
		}
		history = append(history, *res.Executions[0].Review)
		data, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		var loaded EvalResult
		if err := json.Unmarshal(data, &loaded); err != nil {
			t.Fatal(err)
		}
		res = &loaded
	}
	before, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.RecordReview(p.TaskID, p.Alias, p.Repeat, ReviewReject, "reviewer", "", false); err == nil {
		t.Fatal("allowed replacement without amendment")
	}
	after, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("refused amendment changed evidence")
	}
	if res.Report().Arms[0].ReviewAccepted != 1 {
		t.Fatal("historical verdicts affected current acceptance")
	}
}

func TestLegacyReviewRetainedOnAmendment(t *testing.T) {
	var res EvalResult
	if err := json.Unmarshal([]byte(`{"executions":[{"task_id":"task","arm_alias":"arm-A","repeat":1,"status":"accepted","review":{"verdict":"accept","reviewer":"original","note":"initial","at":"2026-09-14T12:00:00Z"}}]}`), &res); err != nil {
		t.Fatal(err)
	}
	original := *res.Executions[0].Review
	if err := res.RecordReview("task", "arm-A", 1, ReviewReject, "second", "correction", true); err != nil {
		t.Fatal(err)
	}
	if len(res.Executions[0].ReviewHistory) != 1 || res.Executions[0].ReviewHistory[0] != original {
		t.Fatal("legacy verdict was not preserved")
	}
}

func TestReviewRejectsAmbiguousImportedResult(t *testing.T) {
	original := EvalResult{Executions: []EvalExecution{
		{TaskID: "task", ArmAlias: "arm-A", Repeat: 1, Status: EvalPendingReview},
		{TaskID: "task", ArmAlias: "arm-A", Repeat: 1, Status: EvalPendingReview},
	}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var result EvalResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	for _, amend := range []bool{false, true} {
		if err := result.RecordReview("task", "arm-A", 1, ReviewAccept, "reviewer", "", amend); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("expected ambiguous-key error, got %v", err)
		}
	}
	after, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(after) {
		t.Fatal("ambiguous review mutated evidence")
	}
}
