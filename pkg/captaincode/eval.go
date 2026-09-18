package captaincode

// Evaluation harness (ROADMAP M1.3). M1.2 made every provider call billable
// to a task; a bill is only half of "cost per accepted task". The other half
// is an acceptance verdict that a second person can reproduce, which means
// the suite, the repository revision, the checks and the arms all have to be
// written down before the run rather than recalled after it.
//
// Four rules this file exists to enforce:
//
//   - a fixture is pinned or it is not a fixture. A task names a repository
//     revision; a suite that omits one cannot be replayed and is rejected at
//     load, not at report time.
//   - every execution starts from a pristine snapshot. The harness clones,
//     never mutates the user's worktree, and never reuses a directory between
//     repeats - one task's leftover build cache is the next task's silent
//     pass.
//   - acceptance is decided by commands, not by prose. A check is a command
//     and an expected exit code. Where checks are insufficient the task says
//     `review: blinded` and the execution ends `pending-review` - which is
//     NOT accepted and never silently counted as one.
//   - an arm's cost is what the ledger recorded while it ran. Executions are
//     serial so that the charge rows appearing between two reads belong to
//     exactly one of them; a parallel runner would break that attribution and
//     is therefore not offered.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EvalSuiteVersion is stamped on every suite and echoed into every result.
// A reader that does not understand the shape refuses it rather than
// mis-summing a newer one - the same contract AccountingVersion carries.
const EvalSuiteVersion = 1

// EvalCheck is one deterministic acceptance rule: a command and the exit code
// it must produce in the snapshot after the arm has finished.
type EvalCheck struct {
	Name       string   `json:"name"`
	Run        []string `json:"run"`
	ExpectExit int      `json:"expect_exit"`
	TimeoutSec int      `json:"timeout_sec,omitempty"`
}

// EvalTask is one fixture. Repo/Revision pin what the work starts from;
// Checks and the artifact constraints decide acceptance without a human.
type EvalTask struct {
	ID            string      `json:"id"`
	Family        string      `json:"family"` // edit, navigate, test-repair, debug, multi-file, review
	Prompt        string      `json:"prompt"`
	Repo          string      `json:"repo"`     // local path to the source repository
	Revision      string      `json:"revision"` // pinned; a branch name is not a revision
	Setup         [][]string  `json:"setup,omitempty"`
	Checks        []EvalCheck `json:"checks,omitempty"`
	MustChange    []string    `json:"must_change,omitempty"`
	MustNotChange []string    `json:"must_not_change,omitempty"`
	TimeoutSec    int         `json:"timeout_sec,omitempty"`
	// Review is "none" (checks decide) or "blinded" (checks are necessary but
	// not sufficient; a reviewer decides later, arm hidden behind Alias).
	Review string `json:"review,omitempty"`
}

// EvalArm is one thing being compared: a fixed worker, Captain's auto
// routing, or an explicit workflow. Run is the command line, with "{prompt}"
// replaced by the task's prompt.
type EvalArm struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"` // frontier, economical, auto, workflow
	Run   []string `json:"run"`
	Env   []string `json:"env,omitempty"`
	Alias string   `json:"alias,omitempty"` // blinding label; filled at load when absent
}

// EvalSuite is the versioned fixture file.
type EvalSuite struct {
	Version int        `json:"version"`
	Name    string     `json:"name"`
	Repeats int        `json:"repeats"`
	Seed    int64      `json:"seed"`
	Arms    []EvalArm  `json:"arms"`
	Tasks   []EvalTask `json:"tasks"`
}

// LoadSuite reads and validates a suite. Validation is deliberately strict:
// every defect it catches here is a defect that would otherwise be found
// after the compute was spent.
func LoadSuite(path string) (*EvalSuite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s EvalSuite
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// Validate rejects a suite that could not be reproduced from its own text.
func (s *EvalSuite) Validate() error {
	if s.Version != EvalSuiteVersion {
		return fmt.Errorf("suite version %d, this build understands %d", s.Version, EvalSuiteVersion)
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("suite needs a name; the report is filed under it")
	}
	if s.Repeats < 1 {
		return fmt.Errorf("repeats must be at least 1")
	}
	if len(s.Arms) == 0 {
		return fmt.Errorf("no arms: there is nothing to compare")
	}
	seenArm := map[string]bool{}
	seenAlias := map[string]bool{}
	for i := range s.Arms {
		a := &s.Arms[i]
		if strings.TrimSpace(a.Name) == "" {
			return fmt.Errorf("arm %d has no name", i)
		}
		if !evalPathName(a.Name) {
			return fmt.Errorf("arm %q: name must be a single portable path component", a.Name)
		}
		if seenArm[a.Name] {
			return fmt.Errorf("duplicate arm %q", a.Name)
		}
		seenArm[a.Name] = true
		if len(a.Run) == 0 {
			return fmt.Errorf("arm %q has no command", a.Name)
		}
		if a.Alias == "" {
			a.Alias = fmt.Sprintf("arm-%c", 'A'+rune(i)) // blinding label for review
		}
		if !evalPathName(a.Alias) {
			return fmt.Errorf("arm %q: alias must be a single portable path component", a.Name)
		}
		if seenAlias[a.Alias] {
			return fmt.Errorf("duplicate review alias %q", a.Alias)
		}
		seenAlias[a.Alias] = true
	}
	if len(s.Tasks) == 0 {
		return fmt.Errorf("no tasks")
	}
	seenTask := map[string]bool{}
	for _, t := range s.Tasks {
		switch {
		case strings.TrimSpace(t.ID) == "":
			return fmt.Errorf("a task has no id")
		case !evalPathName(t.ID):
			return fmt.Errorf("task %q: id must be a single portable path component", t.ID)
		case seenTask[t.ID]:
			return fmt.Errorf("duplicate task %q", t.ID)
		case strings.TrimSpace(t.Family) == "":
			return fmt.Errorf("task %q has no family; the report is stratified by it", t.ID)
		case strings.TrimSpace(t.Prompt) == "":
			return fmt.Errorf("task %q has no prompt", t.ID)
		case strings.TrimSpace(t.Repo) == "":
			return fmt.Errorf("task %q has no repo", t.ID)
		case !pinnedRevision(t.Revision):
			return fmt.Errorf("task %q: revision %q is not pinned (need a full 40-character commit sha)", t.ID, t.Revision)
		case t.Review != "" && t.Review != "none" && t.Review != "blinded":
			return fmt.Errorf("task %q: review must be none or blinded, got %q", t.ID, t.Review)
		case len(t.Checks) == 0 && t.Review != "blinded":
			return fmt.Errorf("task %q has no checks and no blinded review: nothing decides acceptance", t.ID)
		}
		for _, c := range t.Checks {
			if len(c.Run) == 0 {
				return fmt.Errorf("task %q: check %q has no command", t.ID, c.Name)
			}
		}
		seenTask[t.ID] = true
	}
	return nil
}

func evalPathName(name string) bool {
	return name != "." && name != ".." && strings.TrimSpace(name) != "" &&
		!strings.ContainsAny(name, "/\\:\x00")
}

// pinnedRevision accepts only a full commit sha. A branch or tag moves, and a
// suite whose meaning moves is not a fixture.
func pinnedRevision(rev string) bool {
	if len(rev) != 40 {
		return false
	}
	for _, r := range rev {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// EvalExec names one planned execution: one task, under one arm, one repeat.
type EvalExec struct {
	TaskID string `json:"task_id"`
	Arm    string `json:"arm"`
	Repeat int    `json:"repeat"`
}

// Plan enumerates every execution in randomized order. The order is a
// function of the suite's seed, so "randomize run order to reduce load/time
// bias" and "reproduce the report from its manifest" do not contradict each
// other.
func (s *EvalSuite) Plan() []EvalExec {
	var out []EvalExec
	for _, t := range s.Tasks {
		for _, a := range s.Arms {
			for r := 1; r <= s.Repeats; r++ {
				out = append(out, EvalExec{TaskID: t.ID, Arm: a.Name, Repeat: r})
			}
		}
	}
	rng := rand.New(rand.NewSource(s.Seed))
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// Acceptance outcomes. Only StatusAccepted counts in the numerator; the
// others are reported beside it rather than dropped.
const (
	EvalAccepted      = "accepted"
	EvalRejected      = "rejected"       // ran, checks or artifact constraints failed
	EvalPendingReview = "pending-review" // checks passed, a human still owes a verdict
	EvalError         = "error"          // the harness or the arm could not run
	EvalTimeout       = "timeout"
)

// EvalCheckResult is one check's verdict, with enough of its output to
// diagnose a failure without re-running the task.
type EvalCheckResult struct {
	Name   string `json:"name"`
	Exit   int    `json:"exit"`
	Want   int    `json:"want"`
	Passed bool   `json:"passed"`
	Output string `json:"output,omitempty"`
}

// EvalExecution is one finished execution.
type EvalExecution struct {
	TaskID   string            `json:"task_id"`
	Family   string            `json:"family"`
	Prompt   string            `json:"prompt,omitempty"`
	Arm      string            `json:"arm"`
	ArmAlias string            `json:"arm_alias"`
	Repeat   int               `json:"repeat"`
	Status   string            `json:"status"`
	Reason   string            `json:"reason,omitempty"`
	Checks   []EvalCheckResult `json:"checks,omitempty"`
	Changed  []string          `json:"changed,omitempty"`
	// Dir is the snapshot the arm worked in; a blinded reviewer reads the
	// diff there instead of trusting a summary of it.
	Dir           string       `json:"dir,omitempty"`
	Review        *EvalReview  `json:"review,omitempty"`
	ReviewHistory []EvalReview `json:"review_history,omitempty"`
	StartedAt     time.Time    `json:"started_at"`
	DurationMs    int64        `json:"duration_ms"`
	Charges       []Charge     `json:"charges,omitempty"`
	Totals        Totals       `json:"totals"`
}

// Accepted is the single place the numerator is defined. Pending review is
// not acceptance.
func (e EvalExecution) Accepted() bool { return e.Status == EvalAccepted }

// EvalResult is the exportable record: the suite that ran, what it ran
// against, and every execution including the failures.
type EvalResult struct {
	Version    int             `json:"version"`
	Suite      string          `json:"suite"`
	Seed       int64           `json:"seed"`
	Repeats    int             `json:"repeats"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	Build      []SelfStatus    `json:"build,omitempty"`
	Baselines  []EvalBaseline  `json:"baselines,omitempty"`
	Executions []EvalExecution `json:"executions"`
}

// EvalOptions configures a run. Ledger is read (never written) before and
// after each execution so an arm's provider calls can be attributed to it.
type EvalOptions struct {
	WorkDir  string  // where snapshots are made; a temp dir when empty
	Ledger   *Ledger // re-read between executions to attribute charges
	Progress func(EvalExecution)
	Only     []string // task ids to restrict to
	// Build stamps the report with the captain that produced it. The caller
	// supplies it (it knows the checkout); an unstamped result is honest about
	// being unattributable rather than guessing.
	Build []SelfStatus
}

// RunSuite executes the plan serially and returns the exportable result.
// Serial is a correctness requirement, not a simplification: charge
// attribution reads the ledger between executions.
func RunSuite(ctx context.Context, s *EvalSuite, o EvalOptions) (*EvalResult, error) {
	// Validate here too, not only in LoadSuite: a suite assembled in process
	// must not run unvalidated, and this is also where an arm missing its
	// blinding alias gets one - an unaliased arm would show the reviewer
	// exactly what the blinding exists to hide.
	if err := s.Validate(); err != nil {
		return nil, err
	}
	work := o.WorkDir
	if work == "" {
		d, err := os.MkdirTemp("", "captain-eval-")
		if err != nil {
			return nil, err
		}
		work = d
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	baselines, err := VerifySuite(ctx, s, work, o.Only)
	if err != nil {
		return nil, err
	}
	for _, baseline := range baselines {
		if !baseline.Runnable() {
			return nil, fmt.Errorf("task %q baseline %s: %s (snapshot: %s)", baseline.TaskID, baseline.Status, baseline.Reason, baseline.Dir)
		}
	}
	tasks := map[string]EvalTask{}
	for _, t := range s.Tasks {
		tasks[t.ID] = t
	}
	arms := map[string]EvalArm{}
	for _, a := range s.Arms {
		arms[a.Name] = a
	}
	only := map[string]bool{}
	for _, id := range o.Only {
		only[id] = true
	}
	res := &EvalResult{Version: EvalSuiteVersion, Suite: s.Name, Seed: s.Seed, Repeats: s.Repeats, StartedAt: time.Now(), Build: o.Build, Baselines: baselines}
	for _, p := range s.Plan() {
		if len(only) > 0 && !only[p.TaskID] {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		ex := runOne(ctx, tasks[p.TaskID], arms[p.Arm], p.Repeat, work, o.Ledger)
		res.Executions = append(res.Executions, ex)
		if o.Progress != nil {
			o.Progress(ex)
		}
	}
	res.FinishedAt = time.Now()
	return res, nil
}

// runOne snapshots, runs the arm, then decides acceptance. Every early return
// carries a reason: a report whose failures say only "error" cannot be acted
// on.
func runOne(ctx context.Context, t EvalTask, a EvalArm, repeat int, work string, ledger *Ledger) (ex EvalExecution) {
	ex = EvalExecution{TaskID: t.ID, Family: t.Family, Prompt: t.Prompt, Arm: a.Name, ArmAlias: a.Alias, Repeat: repeat, StartedAt: time.Now()}
	defer func() { ex.DurationMs = time.Since(ex.StartedAt).Milliseconds() }()

	dir, err := os.MkdirTemp(work, fmt.Sprintf("%s.%s.%d-", t.ID, a.Name, repeat))
	if err != nil {
		ex.Status, ex.Reason = EvalError, "snapshot directory: "+err.Error()
		return ex
	}
	ex.Dir = dir
	if err := snapshot(ctx, t, dir); err != nil {
		ex.Status, ex.Reason = EvalError, "snapshot: "+err.Error()
		return ex
	}
	for _, cmd := range t.Setup {
		if _, code, err := runIn(ctx, dir, cmd, nil, 10*time.Minute); err != nil || code != 0 {
			ex.Status, ex.Reason = EvalError, fmt.Sprintf("setup %v exited %d: %v", cmd, code, err)
			return ex
		}
	}

	before := chargeIDs(ledger)
	timeout := time.Duration(t.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	run := make([]string, len(a.Run))
	for i, w := range a.Run {
		run[i] = strings.ReplaceAll(w, "{prompt}", t.Prompt)
	}
	out, code, err := runIn(ctx, dir, run, a.Env, timeout)
	ex.Charges, ex.Totals = newCharges(ledger, before)
	switch {
	case err == context.DeadlineExceeded:
		ex.Status, ex.Reason = EvalTimeout, fmt.Sprintf("arm exceeded %s", timeout)
		return ex
	case err != nil:
		ex.Status, ex.Reason = EvalError, "arm: "+err.Error()
		return ex
	case code != 0:
		// A non-zero arm is a rejection, not a harness error: the worker was
		// asked to do the task and reported that it did not.
		ex.Status, ex.Reason = EvalRejected, fmt.Sprintf("arm exited %d: %s", code, tail(out))
		return ex
	}

	ex.Changed, err = changedFiles(ctx, dir, t.Revision)
	if err != nil {
		ex.Status, ex.Reason = EvalError, "artifact inspection: "+err.Error()
		return ex
	}
	for _, c := range t.Checks {
		to := time.Duration(c.TimeoutSec) * time.Second
		if to <= 0 {
			to = 10 * time.Minute
		}
		o, code, err := runIn(ctx, dir, c.Run, nil, to)
		r := EvalCheckResult{Name: c.Name, Exit: code, Want: c.ExpectExit, Passed: err == nil && code == c.ExpectExit}
		if !r.Passed {
			r.Output = tail(o)
		}
		ex.Checks = append(ex.Checks, r)
	}
	if reason := verdict(t, ex); reason != "" {
		ex.Status, ex.Reason = EvalRejected, reason
		return ex
	}
	if t.Review == "blinded" {
		ex.Status = EvalPendingReview
		return ex
	}
	ex.Status = EvalAccepted
	return ex
}

// verdict returns the first reason the execution is not acceptable, or "".
func verdict(t EvalTask, ex EvalExecution) string {
	for _, c := range ex.Checks {
		if !c.Passed {
			return fmt.Sprintf("check %q exited %d, wanted %d", c.Name, c.Exit, c.Want)
		}
	}
	for _, want := range t.MustChange {
		if !matchAny(want, ex.Changed) {
			return fmt.Sprintf("required change to %q is missing", want)
		}
	}
	for _, forbid := range t.MustNotChange {
		if matchAny(forbid, ex.Changed) {
			return fmt.Sprintf("forbidden change to %q", forbid)
		}
	}
	return ""
}

func matchAny(pattern string, paths []string) bool {
	for _, p := range paths {
		if ok, _ := filepath.Match(pattern, p); ok || p == pattern {
			return true
		}
	}
	return false
}

// snapshot materializes a pristine copy at the pinned revision. A local clone
// reads the source repository and never writes it, so a developer's dirty
// worktree survives an evaluation run untouched.
func snapshot(ctx context.Context, t EvalTask, dir string) error {
	repo, err := filepath.Abs(t.Repo)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		return fmt.Errorf("%s is not a git repository", repo)
	}
	if _, code, err := runIn(ctx, filepath.Dir(dir), []string{"git", "clone", "--no-local", "--quiet", repo, dir}, nil, 10*time.Minute); err != nil || code != 0 {
		return fmt.Errorf("clone %s exited %d: %v", repo, code, err)
	}
	if out, code, err := runIn(ctx, dir, []string{"git", "checkout", "--quiet", t.Revision}, nil, 5*time.Minute); err != nil || code != 0 {
		return fmt.Errorf("checkout %s exited %d: %v %s", t.Revision, code, err, tail(out))
	}
	return nil
}

// changedFiles lists what the arm touched, tracked and untracked, relative to
// the snapshot root. This is the artifact evidence the constraints read.
func changedFiles(ctx context.Context, dir, revision string) ([]string, error) {
	seen := map[string]bool{}
	for _, args := range [][]string{
		{"git", "diff", "--name-only", "--no-renames", "-z", revision, "--"},
		{"git", "ls-files", "--others", "--exclude-standard", "-z"},
	} {
		out, code, err := runIn(ctx, dir, args, nil, time.Minute)
		if err != nil {
			return nil, err
		}
		if code != 0 {
			return nil, fmt.Errorf("git artifact inspection exited %d: %s", code, tail(out))
		}
		for _, name := range strings.Split(out, "\x00") {
			if name != "" {
				seen[name] = true
			}
		}
	}
	var files []string
	for name := range seen {
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
}

func runIn(ctx context.Context, dir string, argv []string, env []string, timeout time.Duration) (string, int, error) {
	if len(argv) == 0 {
		return "", 0, fmt.Errorf("empty command")
	}
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(c, argv[0], argv[1:]...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	if c.Err() != nil {
		return string(out), -1, c.Err()
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode(), nil
	}
	if err != nil {
		return string(out), -1, err
	}
	return string(out), 0, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		return "..." + s[len(s)-2000:]
	}
	return s
}

func chargeIDs(l *Ledger) map[string]bool {
	seen := map[string]bool{}
	if l == nil {
		return seen
	}
	fresh := reloaded(l)
	for _, c := range fresh.Charges {
		seen[c.ID] = true
	}
	return seen
}

// newCharges attributes the charge rows that appeared while the arm ran. The
// ledger is re-read from disk because the arm is a separate process; an
// in-memory ledger (tests, throwaway brains) is read as it stands.
func newCharges(l *Ledger, before map[string]bool) ([]Charge, Totals) {
	if l == nil {
		return nil, Totals{}
	}
	var fresh []Charge
	for _, c := range reloaded(l).Charges {
		if !before[c.ID] {
			fresh = append(fresh, c)
		}
	}
	return fresh, totalCalls(fresh, "")
}

func reloaded(l *Ledger) *Ledger {
	if l == nil || !l.Persistent() {
		return l
	}
	if fresh, err := LoadLedger(); err == nil {
		return fresh
	}
	return l
}
