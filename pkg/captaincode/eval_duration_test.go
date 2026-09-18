package captaincode

import (
	"context"
	"encoding/json"
	"testing"
)

func TestExecutionDurationSurvivesExportAndReport(t *testing.T) {
	repo, rev := fixtureRepo(t)
	for _, status := range []string{EvalAccepted, EvalRejected, EvalError, EvalTimeout, EvalPendingReview} {
		t.Run(status, func(t *testing.T) {
			s := baseSuite(repo, rev)
			s.Tasks[0].Setup = [][]string{{"sleep", "0.05"}}
			switch status {
			case EvalRejected:
				s.Arms[0].Run = []string{"false"}
			case EvalError:
				s.Tasks[0].Setup = append(s.Tasks[0].Setup, []string{"false"})
			case EvalTimeout:
				s.Arms[0].Run = []string{"sleep", "5"}
				s.Tasks[0].TimeoutSec = 1
			case EvalPendingReview:
				s.Tasks[0].Review = "blinded"
			}
			res := &EvalResult{Executions: []EvalExecution{runOne(context.Background(), s.Tasks[0], s.Arms[0], 1, t.TempDir(), nil)}}
			if ex := res.Executions[0]; ex.Status != status || ex.DurationMs < 50 {
				t.Fatalf("want %s with setup time included, got %+v", status, ex)
			}
			if status == EvalTimeout && res.Executions[0].DurationMs < 1000 {
				t.Fatal("timeout duration must include the consumed worker allowance")
			}
			data, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			var exported EvalResult
			if err := json.Unmarshal(data, &exported); err != nil {
				t.Fatal(err)
			}
			want := float64(res.Executions[0].DurationMs) / 1000
			report := exported.Report().Arms[0]
			if report.TotalSeconds != want {
				t.Fatalf("exported report duration = %v, want %v", report.TotalSeconds, want)
			}
			if status == EvalAccepted && report.SecPerAccept != want {
				t.Fatalf("seconds per accepted task = %v, want %v", report.SecPerAccept, want)
			}
		})
	}
}
