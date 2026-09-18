package captaincode

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBaselineOKWhenTheTaskAsksForSomething(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	b, err := VerifySuite(context.Background(), s, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || b[0].Status != EvalBaselineOK {
		t.Fatalf("a check that fails at the pinned revision is a runnable fixture, got %+v", b)
	}
	if !b[0].Runnable() {
		t.Fatal("ok must be runnable")
	}
}

func TestVacuousFixtureIsRefusedBeforeTheComputeIsSpent(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	// A check that already passes on the untouched tree: every arm would be
	// accepted without writing anything.
	s.Tasks[0].Checks = []EvalCheck{{Name: "exists", Run: []string{"test", "-f", "answer.txt"}}}
	b, err := VerifySuite(context.Background(), s, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].Status != EvalBaselineVacuous {
		t.Fatalf("a fixture no arm can fail must be named vacuous, got %q", b[0].Status)
	}
	if b[0].Runnable() {
		t.Fatal("a vacuous fixture must not be runnable: it inflates every arm equally")
	}
}

func TestUnrunnableCheckIsBrokenNotAHealthyBaselineFailure(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].Checks = []EvalCheck{{Name: "missing-tool", Run: []string{"captain-no-such-binary"}}}
	b, err := VerifySuite(context.Background(), s, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].Status != EvalBaselineBroken {
		t.Fatalf("a check that cannot run rejects every arm regardless of its work, got %q", b[0].Status)
	}
	if b[0].Checks[0].Ran {
		t.Fatal("a command that never executed must not be recorded as having run")
	}
}

func TestBlindedTaskMayHaveNoBaselineFailure(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].Review = "blinded"
	s.Tasks[0].Checks = []EvalCheck{{Name: "exists", Run: []string{"test", "-f", "answer.txt"}}}
	b, err := VerifySuite(context.Background(), s, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].Status != EvalBaselineReview {
		t.Fatalf("blinded checks are necessary, not sufficient; got %q", b[0].Status)
	}
	if !b[0].Runnable() {
		t.Fatal("a blinded fixture whose verdict carries the acceptance is runnable")
	}
}

func TestUnpinnedRevisionIsBrokenBaseline(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].Revision = "0000000000000000000000000000000000000000"
	b, err := VerifySuite(context.Background(), s, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].Status != EvalBaselineBroken {
		t.Fatalf("a revision the repository does not contain must be found now, got %q", b[0].Status)
	}
}

func TestAbsentGuardIsNamed(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].MustNotChange = []string{"answer.txt", "go.mod"}
	b, err := VerifySuite(context.Background(), s, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].Runnable() {
		t.Fatal("missing protected paths must fail verification")
	}
	if len(b[0].Absent) != 1 || b[0].Absent[0] != "go.mod" {
		t.Fatalf("a guard over a file that does not exist protects nothing and must be named, got %v", b[0].Absent)
	}
}

func TestVerifyLeavesTheSourceRepositoryUntouched(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	if _, err := VerifySuite(context.Background(), s, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if files, err := changedFiles(context.Background(), repo, rev); err != nil || len(files) != 0 {
		t.Fatalf("verification must only read the source repository, found %v", files)
	}
}

func TestVerifyRestrictsToRequestedTasks(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks = append(s.Tasks, EvalTask{
		ID: "t2", Family: "edit", Prompt: "x", Repo: repo, Revision: rev,
		Checks: []EvalCheck{{Name: "answer", Run: []string{"sh", "-c", "grep -q right answer.txt"}}},
	})
	b, err := VerifySuite(context.Background(), s, t.TempDir(), []string{"t2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || b[0].TaskID != "t2" {
		t.Fatalf("--only must restrict the probe, got %+v", b)
	}
}

func TestVerifyRejectsUnknownSelection(t *testing.T) {
	repo, rev := fixtureRepo(t)
	for _, only := range [][]string{{"missing"}, {"t1", "missing"}} {
		if _, err := VerifySuite(context.Background(), baseSuite(repo, rev), t.TempDir(), only); err == nil {
			t.Fatalf("unknown selection %v must not produce a successful partial verification", only)
		}
	}
}

func TestVerifyReportsCancellation(t *testing.T) {
	repo, rev := fixtureRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifySuite(ctx, baseSuite(repo, rev), t.TempDir(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestBaselineRecordsElapsedTime(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].Checks = []EvalCheck{{Name: "delayed", Run: []string{"sleep", "0.05"}}}
	b, err := VerifySuite(context.Background(), s, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].DurationMs < 50 {
		t.Fatalf("elapsed time not recorded: %+v", b[0])
	}
}

func TestRunInReportsCancellationDuringCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	_, _, err := runIn(ctx, t.TempDir(), []string{"sleep", "10"}, nil, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestBaselineProtectedPatterns(t *testing.T) {
	repo, rev := fixtureRepo(t)
	for _, tc := range []struct {
		pattern string
		status  string
		absent  bool
	}{
		{"*.txt", EvalBaselineOK, false},
		{"answer.?xt", EvalBaselineOK, false},
		{"[a-z]*.txt", EvalBaselineOK, false},
		{"tests/*.go", EvalBaselineBroken, true},
		{"*.missing", EvalBaselineBroken, true},
		{"[", EvalBaselineBroken, false},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			s := baseSuite(repo, rev)
			s.Tasks[0].MustNotChange = []string{tc.pattern}
			b, err := VerifySuite(context.Background(), s, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if b[0].Status != tc.status {
				t.Fatalf("expected %s, got %+v", tc.status, b[0])
			}
			if tc.absent && (len(b[0].Absent) != 1 || b[0].Absent[0] != tc.pattern) {
				t.Fatalf("missing guard not reported: %+v", b[0])
			}
		})
	}
}
