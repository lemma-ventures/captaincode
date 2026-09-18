package captaincode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRepo builds a throwaway git repository and returns its path and the
// pinned revision a suite would name.
func fixtureRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	run("git", "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "answer.txt"), []byte("wrong\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "--quiet", "-m", "seed")
	rev, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(rev))
}

func baseSuite(repo, rev string) *EvalSuite {
	return &EvalSuite{
		Version: EvalSuiteVersion, Name: "pilot", Repeats: 1, Seed: 7,
		Arms: []EvalArm{{Name: "fixer", Kind: "economical", Run: []string{"sh", "-c", "printf right > answer.txt"}}},
		Tasks: []EvalTask{{
			ID: "t1", Family: "edit", Prompt: "fix it", Repo: repo, Revision: rev,
			Checks: []EvalCheck{{Name: "answer", Run: []string{"sh", "-c", "grep -q right answer.txt"}}},
		}},
	}
}

func TestValidateRejectsUnreproducibleFixtures(t *testing.T) {
	repo, rev := fixtureRepo(t)
	cases := map[string]func(*EvalSuite){
		"unpinned revision":  func(s *EvalSuite) { s.Tasks[0].Revision = "main" },
		"no acceptance rule": func(s *EvalSuite) { s.Tasks[0].Checks = nil },
		"unknown version":    func(s *EvalSuite) { s.Version = EvalSuiteVersion + 1 },
		"armless":            func(s *EvalSuite) { s.Arms = nil },
		"no family":          func(s *EvalSuite) { s.Tasks[0].Family = "" },
	}
	for name, mangle := range cases {
		s := baseSuite(repo, rev)
		mangle(s)
		if err := s.Validate(); err == nil {
			t.Fatalf("%s must be rejected at load, not discovered after the compute is spent", name)
		}
	}
	ok := baseSuite(repo, rev)
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid suite rejected: %v", err)
	}
	if ok.Arms[0].Alias == "" {
		t.Fatal("validation must assign a blinding alias to every arm")
	}
}

func TestBlindedTaskWithoutChecksIsAllowed(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].Checks, s.Tasks[0].Review = nil, "blinded"
	if err := s.Validate(); err != nil {
		t.Fatalf("blinded review is a legitimate acceptance rule: %v", err)
	}
}

func TestPlanIsSeededAndComplete(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Repeats = 3
	s.Arms = append(s.Arms, EvalArm{Name: "other", Kind: "frontier", Run: []string{"true"}})
	_ = s.Validate()
	a, b := s.Plan(), s.Plan()
	if len(a) != 6 {
		t.Fatalf("2 arms x 3 repeats = 6 executions, got %d", len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("a seeded plan must replay in the same order")
		}
	}
}

func runSuite(t *testing.T, s *EvalSuite) *EvalResult {
	t.Helper()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	res, err := RunSuite(context.Background(), s, EvalOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRunAcceptsWhenChecksPassAndLeavesSourceRepoClean(t *testing.T) {
	repo, rev := fixtureRepo(t)
	res := runSuite(t, baseSuite(repo, rev))
	if len(res.Executions) != 1 || res.Executions[0].Status != EvalAccepted {
		t.Fatalf("expected one accepted execution, got %+v", res.Executions)
	}
	if got := res.Executions[0].Changed; len(got) != 1 || got[0] != "answer.txt" {
		t.Fatalf("changed-file evidence wrong: %v", got)
	}
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if err != nil || len(out) != 0 {
		t.Fatalf("the harness must never touch the source worktree: %q %v", out, err)
	}
}

func TestFailingCheckRejectsWithItsReason(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Arms[0].Run = []string{"true"} // does nothing
	ex := runSuite(t, s).Executions[0]
	if ex.Status != EvalRejected || !strings.Contains(ex.Reason, "answer") {
		t.Fatalf("a failing check must reject and name itself: %+v", ex)
	}
	if len(ex.Checks) != 1 || ex.Checks[0].Passed {
		t.Fatalf("check verdicts must be recorded: %+v", ex.Checks)
	}
}

func TestArtifactConstraintsDecideBeyondChecks(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].MustNotChange = []string{"answer.txt"}
	if ex := runSuite(t, s).Executions[0]; ex.Status != EvalRejected || !strings.Contains(ex.Reason, "forbidden") {
		t.Fatalf("a forbidden edit must reject even when the checks pass: %+v", ex)
	}
	s = baseSuite(repo, rev)
	s.Tasks[0].MustChange = []string{"never.txt"}
	if ex := runSuite(t, s).Executions[0]; ex.Status != EvalRejected || !strings.Contains(ex.Reason, "missing") {
		t.Fatalf("a missing required change must reject: %+v", ex)
	}
}

func TestBlindedReviewIsNotAcceptance(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].Review = "blinded"
	res := runSuite(t, s)
	ex := res.Executions[0]
	if ex.Status != EvalPendingReview || ex.Accepted() {
		t.Fatalf("passing checks under blinded review is pending, never accepted: %+v", ex)
	}
	if rep := res.Report(); rep.Arms[0].Accepted != 0 || rep.Arms[0].PendingReview != 1 || rep.Arms[0].Defined {
		t.Fatalf("pending review must not enter the numerator: %+v", rep.Arms[0])
	}
	if got := res.PendingReviews(); len(got) != 1 || !strings.Contains(got[0], "arm-A") {
		t.Fatalf("the reviewer must see the alias, not the arm: %v", got)
	}
}

func TestNonZeroArmIsRejectionNotHarnessError(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Arms[0].Run = []string{"sh", "-c", "exit 3"}
	if ex := runSuite(t, s).Executions[0]; ex.Status != EvalRejected {
		t.Fatalf("a worker that reports failure rejected the task, it did not break the harness: %+v", ex)
	}
}

func TestTimeoutIsItsOwnOutcome(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Arms[0].Run, s.Tasks[0].TimeoutSec = []string{"sleep", "5"}, 1
	if ex := runSuite(t, s).Executions[0]; ex.Status != EvalTimeout {
		t.Fatalf("a timed-out arm is a timeout, distinguishable from a rejection: %+v", ex)
	}
}

func TestSnapshotIsPristinePerRepeat(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Repeats = 2
	// The arm passes only if it starts from the pinned content: a repeat that
	// inherited the previous tree would find "right" already there.
	s.Arms[0].Run = []string{"sh", "-c", "grep -q wrong answer.txt && printf right > answer.txt"}
	for _, ex := range runSuite(t, s).Executions {
		if ex.Status != EvalAccepted {
			t.Fatalf("every repeat starts from the pinned revision: %+v", ex)
		}
	}
}

func TestReportRatiosAreUndefinedWithoutAcceptance(t *testing.T) {
	r := &EvalResult{Suite: "s", Executions: []EvalExecution{
		{TaskID: "t1", Family: "edit", Arm: "a", Status: EvalRejected, DurationMs: 1000},
		{TaskID: "t2", Family: "edit", Arm: "a", Status: EvalError, DurationMs: 1000},
	}}
	arm := r.Report().Arms[0]
	if arm.Defined || arm.CostPerAccept != 0 {
		t.Fatalf("no acceptance means undefined, not zero: %+v", arm)
	}
	if arm.Executions != 2 || arm.Rejected != 1 || arm.Errors != 1 {
		t.Fatalf("failures stay in the denominator: %+v", arm)
	}
}

func TestReportChargesWholeWorkloadToAcceptedTasks(t *testing.T) {
	r := &EvalResult{Suite: "s", Executions: []EvalExecution{
		{TaskID: "t1", Family: "edit", Arm: "a", Status: EvalAccepted, DurationMs: 10_000, Totals: Totals{CostUSD: 1, Calls: 1, Measured: 1}},
		{TaskID: "t2", Family: "edit", Arm: "a", Status: EvalRejected, DurationMs: 10_000, Totals: Totals{CostUSD: 3, Calls: 1, Measured: 1}},
	}}
	arm := r.Report().Arms[0]
	if arm.CostPerAccept != 4 || arm.SecPerAccept != 20 {
		t.Fatalf("a failed attempt's cost and time belong to the accepted task's price: %+v", arm)
	}
	if !arm.Complete() {
		t.Fatal("all-measured calls make the total a bill")
	}
}

func TestIncompleteCoverageIsNotPresentedAsABill(t *testing.T) {
	r := &EvalResult{Suite: "s", Executions: []EvalExecution{
		{TaskID: "t1", Family: "edit", Arm: "a", Status: EvalAccepted, Totals: Totals{CostUSD: 1, Calls: 2, Measured: 1, Unknown: 1}},
	}}
	rep := r.Report()
	if rep.Arms[0].Complete() {
		t.Fatal("a call that reported no usage makes the sum partial")
	}
	var sb strings.Builder
	WriteReport(&sb, rep)
	if !strings.Contains(sb.String(), "*") || !strings.Contains(sb.String(), "not a billed total") {
		t.Fatalf("the table must mark a partial sum:\n%s", sb.String())
	}
}

func TestFamilyStratification(t *testing.T) {
	r := &EvalResult{Suite: "s", Executions: []EvalExecution{
		{TaskID: "t1", Family: "edit", Arm: "a", Status: EvalAccepted},
		{TaskID: "t2", Family: "debug", Arm: "a", Status: EvalRejected},
	}}
	fams := r.Report().Families
	if len(fams) != 2 || fams[0].Family != "debug" || fams[0].Accepted != 0 || fams[1].Accepted != 1 {
		t.Fatalf("acceptance must be reported per family: %+v", fams)
	}
}

func TestNoRecordedCallsIsNotZeroCost(t *testing.T) {
	r := &EvalResult{Suite: "s", Executions: []EvalExecution{
		{TaskID: "t1", Family: "edit", Arm: "a", Status: EvalAccepted, DurationMs: 1000},
	}}
	var sb strings.Builder
	WriteReport(&sb, r.Report())
	if strings.Contains(sb.String(), "$0.0000") {
		t.Fatalf("an arm whose calls were never recorded must not read as free:\n%s", sb.String())
	}
}

func TestCommittedProtectedChangeIsRejected(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Tasks[0].MustNotChange = []string{"answer.txt"}
	s.Arms[0].Run = []string{"sh", "-c", "printf right > answer.txt && git add answer.txt && git -c user.name=test -c user.email=test@example.invalid commit -qm fix"}
	ex := runSuite(t, s).Executions[0]
	if ex.Status != EvalRejected || !strings.Contains(ex.Reason, "forbidden") {
		t.Fatalf("committing must not hide protected changes: %+v", ex)
	}
}

func TestArtifactInspectionFailureBlocksAcceptance(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	s.Arms[0].Run = []string{"sh", "-c", "printf right > answer.txt && rm -rf .git"}
	ex := runSuite(t, s).Executions[0]
	if ex.Status != EvalError || !strings.Contains(ex.Reason, "artifact inspection") {
		t.Fatalf("missing artifact evidence must block acceptance: %+v", ex)
	}
}

func TestArtifactPathsPreserveRenamesAndSpecialCharacters(t *testing.T) {
	repo, rev := fixtureRepo(t)
	cmd := exec.Command("git", "mv", "answer.txt", "renamed\nfile.txt")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rename: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "new\tfile.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := changedFiles(context.Background(), repo, rev)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"answer.txt", "new\tfile.txt", "renamed\nfile.txt"}
	if strings.Join(files, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got %q, want %q", files, want)
	}
}

func TestEvalRejectsUnsafePathNamesBeforeCreatingWork(t *testing.T) {
	for _, name := range []string{"../outside", "a/b", `a\b`, "/absolute", ".", "..", "C:drive", "nul\x00byte"} {
		for _, field := range []string{"task", "arm"} {
			t.Run(field+"/"+name, func(t *testing.T) {
				s := baseSuite("unused", strings.Repeat("a", 40))
				if field == "task" {
					s.Tasks[0].ID = name
				} else {
					s.Arms[0].Name = name
				}
				work := filepath.Join(t.TempDir(), "work")
				if _, err := RunSuite(context.Background(), s, EvalOptions{WorkDir: work}); err == nil {
					t.Fatal("execution accepted unsafe name")
				}
				if _, err := VerifySuite(context.Background(), s, work, nil); err == nil {
					t.Fatal("verification accepted unsafe name")
				}
				if _, err := os.Stat(work); !os.IsNotExist(err) {
					t.Fatalf("validation touched work directory: %v", err)
				}
			})
		}
	}
}

func TestEvalRepeatedRunsPreserveArtifacts(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	work := t.TempDir()
	var dirs []string
	for range 2 {
		result, err := RunSuite(context.Background(), s, EvalOptions{WorkDir: work})
		if err != nil {
			t.Fatal(err)
		}
		if result.Executions[0].Status != EvalAccepted {
			t.Fatalf("execution failed: %+v", result.Executions[0])
		}
		baseline, err := VerifySuite(context.Background(), s, work, nil)
		if err != nil || !baseline[0].Runnable() {
			t.Fatalf("baseline failed: %+v, %v", baseline, err)
		}
		for _, dir := range []string{result.Executions[0].Dir, baseline[0].Dir} {
			for _, previous := range dirs {
				if dir == previous {
					t.Fatalf("snapshot directory reused: %s", dir)
				}
			}
			dirs = append(dirs, dir)
			if err := os.WriteFile(filepath.Join(dir, "retained"), []byte("evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, dir := range dirs {
		if data, err := os.ReadFile(filepath.Join(dir, "retained")); err != nil || string(data) != "evidence" {
			t.Fatalf("prior artifact lost: %s: %v", dir, err)
		}
	}
}

func TestValidateReviewAliases(t *testing.T) {
	for _, aliases := range [][2]string{{"same", "same"}, {"arm-B", ""}, {"", "arm-A"}, {"bad/alias", "valid"}, {" ", "valid"}} {
		t.Run(strings.Join(aliases[:], ":"), func(t *testing.T) {
			s := baseSuite("unused", strings.Repeat("a", 40))
			s.Arms[0].Alias = aliases[0]
			s.Arms = append(s.Arms, EvalArm{Name: "other", Alias: aliases[1], Run: []string{"true"}})
			if err := s.Validate(); err == nil {
				t.Fatal("ambiguous or unaddressable review aliases accepted")
			}
		})
	}
	s := baseSuite("unused", strings.Repeat("a", 40))
	s.Arms = append(s.Arms, EvalArm{Name: "other", Run: []string{"true"}})
	for range 2 {
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if s.Arms[0].Alias == s.Arms[1].Alias {
		t.Fatal("generated aliases collide")
	}
}
