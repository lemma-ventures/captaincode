package captaincode

// Fixture verification (ROADMAP M1.3). LoadSuite proves a suite can be
// replayed; it cannot prove the suite measures anything. Those are different
// defects, and the second one is the expensive one: a task whose checks
// already pass at its pinned revision is accepted by every arm without any
// work being done, so it inflates four acceptance rates equally and is
// invisible in the report that follows. 144 executions is too late to find
// that out.
//
// So before a suite is run, each task is put through the run it is about to
// receive - clone the pinned revision, run the setup - and then the checks are
// run against that untouched tree. What they must do there is the opposite of
// what they must do afterwards:
//
//   - at least one check must FAIL at baseline. A failing check is the
//     task's statement of what is not yet true; if none fails, the fixture
//     asks for nothing.
//   - every check must RUN at baseline. A check whose command is missing
//     exits non-zero too, and would read as a healthy baseline failure while
//     in fact rejecting every arm no matter what it wrote.
//
// A blinded task is the documented exception, not an oversight: its checks are
// necessary and not sufficient, so "the build still passes and a reviewer
// decides the rest" is a legitimate fixture with no baseline failure.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Baseline verdicts. Only ok and review-decides are runnable fixtures.
const (
	EvalBaselineOK      = "ok"             // at least one check fails; the task asks for something
	EvalBaselineVacuous = "vacuous"        // every check already passes; every arm would be accepted for free
	EvalBaselineBroken  = "broken"         // the snapshot, the setup, or a check could not run
	EvalBaselineReview  = "review-decides" // blinded, no baseline failure: legitimate, and named as such
)

// EvalBaselineCheck is one check's behaviour on the untouched snapshot.
type EvalBaselineCheck struct {
	Name   string `json:"name"`
	Exit   int    `json:"exit"`
	Want   int    `json:"want"`
	Passed bool   `json:"passed"`
	// Ran distinguishes "the command executed and disagreed" from "the command
	// could not be executed at all". Both are non-zero; only the first is
	// evidence about the repository.
	Ran    bool   `json:"ran"`
	Output string `json:"output,omitempty"`
}

// EvalBaseline is one task's readiness to be measured.
type EvalBaseline struct {
	TaskID string              `json:"task_id"`
	Family string              `json:"family"`
	Status string              `json:"status"`
	Reason string              `json:"reason,omitempty"`
	Checks []EvalBaselineCheck `json:"checks,omitempty"`
	// Absent lists must_not_change paths that do not exist at the pinned
	// revision. Such a constraint forbids touching nothing and silently
	// protects less than the fixture claims.
	Absent     []string `json:"absent_guards,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	DurationMs int64    `json:"duration_ms"`
}

// Runnable reports whether this fixture can contribute evidence.
func (b EvalBaseline) Runnable() bool {
	return b.Status == EvalBaselineOK || b.Status == EvalBaselineReview
}

// VerifySuite probes every task's pinned revision. It never runs an arm and
// never spends a provider call: the whole point is to be the cheap step that
// happens before the expensive one.
func VerifySuite(ctx context.Context, s *EvalSuite, workDir string, only []string) ([]EvalBaseline, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range only {
		found := false
		for _, task := range s.Tasks {
			if task.ID == id {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown task %q", id)
		}
		want[id] = true
	}
	work := workDir
	if work == "" {
		d, err := os.MkdirTemp("", "captain-eval-baseline-")
		if err != nil {
			return nil, err
		}
		work = d
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	var out []EvalBaseline
	for _, t := range s.Tasks {
		if len(want) > 0 && !want[t.ID] {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		out = append(out, verifyTask(ctx, t, work))
	}
	return out, ctx.Err()
}

func verifyTask(ctx context.Context, t EvalTask, work string) (b EvalBaseline) {
	b = EvalBaseline{TaskID: t.ID, Family: t.Family}
	started := time.Now()
	defer func() { b.DurationMs = time.Since(started).Milliseconds() }()

	dir, err := os.MkdirTemp(work, t.ID+".baseline-")
	if err != nil {
		b.Status, b.Reason = EvalBaselineBroken, "snapshot directory: "+err.Error()
		return b
	}
	b.Dir = dir
	if err := snapshot(ctx, t, dir); err != nil {
		b.Status, b.Reason = EvalBaselineBroken, "snapshot: "+err.Error()
		return b
	}
	for _, cmd := range t.Setup {
		if _, code, err := runIn(ctx, dir, cmd, nil, 10*time.Minute); err != nil || code != 0 {
			b.Status, b.Reason = EvalBaselineBroken, fmt.Sprintf("setup %v exited %d: %v", cmd, code, err)
			return b
		}
	}
	for _, g := range t.MustNotChange {
		if _, err := os.Lstat(filepath.Join(dir, g)); err == nil {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(dir, g))
		if err != nil {
			b.Status, b.Reason = EvalBaselineBroken, fmt.Sprintf("invalid protected path pattern %q: %v", g, err)
			return b
		}
		if len(matches) == 0 {
			b.Absent = append(b.Absent, g)
		}
	}

	failed := false
	for _, c := range t.Checks {
		to := time.Duration(c.TimeoutSec) * time.Second
		if to <= 0 {
			to = 10 * time.Minute
		}
		o, code, err := runIn(ctx, dir, c.Run, nil, to)
		r := EvalBaselineCheck{Name: c.Name, Exit: code, Want: c.ExpectExit, Ran: err == nil}
		r.Passed = r.Ran && code == c.ExpectExit
		if !r.Passed {
			r.Output = tail(o)
		}
		b.Checks = append(b.Checks, r)
		if !r.Ran {
			b.Status = EvalBaselineBroken
			b.Reason = fmt.Sprintf("check %q could not run at the pinned revision (%v); it would reject every arm", c.Name, err)
			return b
		}
		if !r.Passed {
			failed = true
		}
	}

	switch {
	case len(b.Absent) > 0:
		b.Status = EvalBaselineBroken
		b.Reason = "protected paths are absent from the baseline snapshot"
	case failed:
		b.Status = EvalBaselineOK
	case t.Review == "blinded":
		// Checks are necessary, not sufficient. Nothing fails here because
		// the thing being asked for is what the reviewer will judge.
		b.Status = EvalBaselineReview
		b.Reason = "no check fails at baseline; acceptance rests entirely on the blinded verdict"
	default:
		b.Status = EvalBaselineVacuous
		b.Reason = "every check already passes at the pinned revision: the task asks for nothing an arm could fail"
	}
	return b
}
