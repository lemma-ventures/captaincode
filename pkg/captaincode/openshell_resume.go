package captaincode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

type openShellSequencePlan struct {
	Version     int                  `json:"version"`
	Pilot       string               `json:"pilot"`
	Prepared    string               `json:"prepared"`
	StateRoot   string               `json:"state_root"`
	RunDir      string               `json:"run_dir"`
	Repo        string               `json:"repo"`
	Revision    string               `json:"revision"`
	Runtime     string               `json:"runtime"`
	Director    string               `json:"director"`
	Concurrency int                  `json:"concurrency"`
	DeadlineAt  time.Time            `json:"deadline_at,omitempty"`
	MaxAttempts int                  `json:"max_attempts,omitempty"`
	MaxCostUSD  float64              `json:"max_cost_usd,omitempty"`
	Teams       []OpenShellTeam      `json:"teams"`
	Provenance  *OpenShellProvenance `json:"provenance"`
}

// lockOpenShellSequence takes the run's exclusive lock. Only a new sequence
// creates the lock file: recovery opens the one its first run left, so
// pointing --resume at an ordinary directory leaves nothing behind.
func lockOpenShellSequence(dir string, create bool) (*os.File, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("openshell: run directory must be a real directory without group or public write access")
	}
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(filepath.Join(dir, "sequence.lock"), flags, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("openshell: not a sandbox sequence run directory (no sequence.lock)")
	}
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		f.Close()
		return nil, errors.New("openshell: unsafe sequence lock")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Getuid()) {
		f.Close()
		return nil, errors.New("openshell: unsafe sequence lock owner or link count")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("openshell: sequence is already active: %w", err)
	}
	return f, nil
}

func (r *OpenShellRunner) saveSequencePlan(teams []OpenShellTeam, run *OpenShellRun) error {
	provenance, err := r.provenance()
	if err != nil {
		return err
	}
	plan := openShellSequencePlan{Version: 1, Pilot: r.Pilot, Prepared: r.Prepared, StateRoot: r.StateRoot,
		RunDir: r.RunDir, Repo: r.Repo, Revision: r.Revision, Runtime: r.Runtime,
		Director: r.DirectorName, Concurrency: r.Concurrency, DeadlineAt: r.DeadlineAt, MaxAttempts: r.MaxAttempts, MaxCostUSD: r.MaxCostUSD, Teams: teams, Provenance: provenance}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > openShellReportLimit {
		return errors.New("openshell: sequence plan is too large")
	}
	file := filepath.Join(r.RunDir, "sequence.json")
	if err := writeOpenShellFile(file, data); err != nil {
		return err
	}
	f, err := openRegular(file)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return err
	}
	run.SequenceSHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	run.Provenance = provenance
	return nil
}

func decodeOpenShellSequence(file string, value any) ([]byte, error) {
	data, err := readOpenShellFile(file, openShellReportLimit)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("openshell: trailing checkpoint data")
	}
	return data, nil
}

type OpenShellRecovery struct {
	mu        sync.Mutex
	lock      *os.File
	runner    *OpenShellRunner
	teams     []OpenShellTeam
	run       *OpenShellRun
	recovered []*OpenShellRun
	expected  *OpenShellCheckpoint
	aside     *OpenShellSetAside // an interrupted stage run to set aside before running it again
	used      bool
}

func (r *OpenShellRecovery) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lock == nil {
		return nil
	}
	err := r.lock.Close()
	r.lock = nil
	return err
}

func PrepareOpenShellRecovery(ctx context.Context, checkpoint OpenShellCheckpoint, log func(string, ...any)) (*OpenShellRecovery, error) {
	if err := checkpoint.Validate(); err != nil {
		return nil, err
	}
	return prepareOpenShellRecovery(ctx, checkpoint.RunDir, &checkpoint, log)
}

func ResumeOpenShellSequence(ctx context.Context, dir string, log func(string, ...any)) (Result, error) {
	recovery, err := prepareOpenShellRecovery(ctx, dir, nil, log)
	if err != nil {
		return Result{}, err
	}
	defer recovery.Close()
	return recovery.Run(ctx)
}

func (r *OpenShellRecovery) Run(ctx context.Context) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lock == nil || r.used {
		return Result{}, errors.New("openshell: recovery is closed or already used")
	}
	r.used = true
	ctx, stop, err := OpenShellBudgetContext(ctx, &Budget{StartedAt: r.run.StartedAt})
	if err != nil {
		return Result{}, err
	}
	defer stop()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	ctx, deadlineCancel := r.runner.deadlineContext(ctx)
	defer deadlineCancel()
	ctx, cancel := context.WithTimeout(ctx, workerTimeout())
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	runner, run := r.runner, r.run
	if fn := openShellReviewerFromContext(ctx); fn != nil {
		runner.Reviewer = fn
	}
	if runner.Task == "" && len(r.teams) > 0 && len(r.teams[0].Tasks) > 0 {
		runner.Task = r.teams[0].Tasks[0].Prompt
	}
	if r.expected != nil {
		checkpoint, err := openShellCheckpoint(runner.RunDir, run, r.expected.VerifiedStages)
		if err != nil {
			return Result{}, err
		}
		if checkpoint != *r.expected {
			return Result{}, errors.New("openshell: verified-stage evidence changed after recovery admission")
		}
	}
	if len(r.recovered) == len(r.teams) && run.Verdict == "pass" {
		return finishOpenShellRun(ctx, runner, run, nil, false)
	}
	run.Resumptions = append(run.Resumptions, time.Now())
	if r.aside != nil {
		// Saved before dispatch: the stopped run keeps its directory and its
		// count, and the stage runs again in a directory of its own.
		run.SetAside = append(run.SetAside, *r.aside)
		run.Stages = run.Stages[:r.aside.Stage-1]
		if err := saveOpenShellSequence(runner.RunDir, run); err != nil {
			return Result{}, err
		}
		runner.logf("stage %d/%d: set aside a run a cancellation stopped; running the stage again", r.aside.Stage, len(r.teams))
	}
	run.Tasks, run.Integrated = nil, nil
	run.Verdict, run.Error = "fail", ""
	err = runner.runSequence(ctx, r.teams, run, r.recovered)
	if err != nil {
		run.Error = err.Error()
	}
	if saveErr := saveOpenShellSequence(runner.RunDir, run); saveErr != nil {
		err = errors.Join(err, saveErr)
	}
	return finishOpenShellRun(ctx, runner, run, err, false)
}

func prepareOpenShellRecovery(ctx context.Context, dir string, expected *OpenShellCheckpoint, log func(string, ...any)) (*OpenShellRecovery, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	lock, err := lockOpenShellSequence(dir, false)
	if err != nil {
		return nil, err
	}
	ready := false
	defer func() {
		if !ready {
			lock.Close()
		}
	}()
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	var plan openShellSequencePlan
	data, err := decodeOpenShellSequence(filepath.Join(dir, "sequence.json"), &plan)
	if err != nil {
		return nil, fmt.Errorf("openshell: cannot resume without a saved sequence plan: %w", err)
	}
	if expected != nil && (expected.RunDir != dir || expected.SequenceSHA256 != fmt.Sprintf("%x", sha256.Sum256(data))) {
		return nil, errors.New("openshell: saved sequence does not match the task checkpoint")
	}
	var run OpenShellRun
	if _, err := decodeOpenShellSequence(filepath.Join(dir, "run.json"), &run); err != nil {
		return nil, err
	}
	if plan.Version != 1 || plan.RunDir != dir || run.Version != ArtifactVersion ||
		run.Team != filepath.Base(dir) || run.Repo != plan.Repo || run.Revision != plan.Revision ||
		run.Runtime != plan.Runtime || run.Director != plan.Director || run.Concurrency != plan.Concurrency ||
		!run.DeadlineAt.Equal(plan.DeadlineAt) || !run.RequireAll || run.SequenceSHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
		return nil, errors.New("openshell: sequence plan does not match the checkpoint")
	}
	if expected != nil {
		checkpoint, err := openShellCheckpoint(dir, &run, expected.VerifiedStages)
		if err != nil {
			return nil, err
		}
		if checkpoint != *expected {
			return nil, errors.New("openshell: verified-stage evidence does not match the task checkpoint")
		}
	}
	ctx, cancel, err := OpenShellBudgetContext(ctx, &Budget{StartedAt: run.StartedAt})
	if err != nil {
		return nil, err
	}
	defer cancel()
	if !plan.DeadlineAt.IsZero() {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, plan.DeadlineAt)
		defer stop()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("openshell: saved workflow deadline: %w", err)
	}
	if !reflect.DeepEqual(run.Provenance, plan.Provenance) {
		return nil, errors.New("openshell: checkpoint provenance changed")
	}
	if _, err := validateOpenShellSequence(plan.Teams); err != nil {
		return nil, err
	}
	runner := &OpenShellRunner{Pilot: plan.Pilot, Prepared: plan.Prepared, StateRoot: plan.StateRoot,
		Repo: plan.Repo, Revision: plan.Revision, RunDir: dir, Runtime: plan.Runtime,
		Concurrency: plan.Concurrency, DeadlineAt: plan.DeadlineAt, MaxAttempts: plan.MaxAttempts, RequireAll: true, DirectorName: plan.Director, Log: log}
	switch plan.Director {
	case "", "none":
	case "claude":
		runner.Director = ToolLessClaudeDirector
	default:
		return nil, errors.New("openshell: unsupported saved director")
	}
	savedBudget, err := runner.attemptBudget(context.Background(), plan.Teams)
	if err != nil {
		return nil, err
	}
	if run.AttemptBudget == nil {
		if plan.MaxAttempts != 0 {
			return nil, errors.New("openshell: missing saved attempt admission")
		}
	} else if *run.AttemptBudget != *savedBudget {
		return nil, errors.New("openshell: attempt admission does not match saved plan")
	}
	if _, err := runner.attemptBudget(ctx, plan.Teams); err != nil {
		return nil, err
	}
	if err := runner.check(ctx); err != nil {
		return nil, err
	}
	provenance, err := runner.provenance()
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(provenance, plan.Provenance) {
		return nil, errors.New("openshell: runtime or pilot changed; start a new workflow")
	}
	recovered, aside, err := runner.recoverSequence(ctx, plan.Teams, &run)
	if err != nil {
		return nil, err
	}
	if expected != nil && len(recovered) != expected.VerifiedStages {
		return nil, errors.New("openshell: completed stage evidence was not saved to the task checkpoint; inspect the run before explicit run-directory recovery")
	}
	if len(recovered) < len(plan.Teams) && (aside != nil || len(run.SetAside) > 0) {
		if err := runner.rerunFits(ctx, plan.Teams, recovered, run.SetAside, aside); err != nil {
			return nil, err
		}
	}
	if len(recovered) == len(plan.Teams) && run.Verdict == "pass" {
		last := recovered[len(recovered)-1].Integrated
		var tasks []*OpenShellResult
		usage := &OpenShellAttemptUsage{}
		for i := range run.SetAside {
			usage.add(run.SetAside[i].Attempts)
		}
		for _, stage := range recovered {
			tasks = append(tasks, stage.Tasks...)
			usage.add(stage.AttemptUsage)
		}
		if run.Integrated == nil || run.Integrated.Tree != last.Tree ||
			!slices.Equal(run.Integrated.Verify, last.Verify) || !reflect.DeepEqual(run.Tasks, tasks) {
			return nil, errors.New("openshell: cumulative result does not match verified stages")
		}
		if _, err := runner.verifiedExport(ctx, &run); err != nil {
			return nil, err
		}
		if run.AttemptUsage != nil && *run.AttemptUsage != *usage {
			return nil, errors.New("openshell: cumulative attempt usage does not match verified stages")
		}
		run.AttemptUsage = usage
	}
	if _, err := os.Lstat(filepath.Join(dir, "integrated.patch")); err == nil && len(recovered) != len(plan.Teams) {
		return nil, errors.New("openshell: unexpected cumulative patch on an incomplete sequence")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ready = true
	return &OpenShellRecovery{lock: lock, runner: runner, teams: plan.Teams, run: &run, recovered: recovered, aside: aside, expected: expected}, nil
}

func (r *OpenShellRunner) recoverSequence(ctx context.Context, teams []OpenShellTeam, run *OpenShellRun) ([]*OpenShellRun, *OpenShellSetAside, error) {
	snapshot := filepath.Join(r.RunDir, "snapshot")
	resolved, err := filepath.EvalSymlinks(snapshot)
	if err != nil || resolved != snapshot {
		return nil, nil, errors.New("openshell: snapshot is missing or redirected")
	}
	for _, dir := range []string{snapshot, filepath.Join(snapshot, ".git")} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			return nil, nil, errors.New("openshell: invalid snapshot repository")
		}
	}
	hooks, err := gitOutput(ctx, snapshot, nil, nil, "config", "--local", "--get", "core.hooksPath")
	if err != nil || strings.TrimSpace(string(hooks)) != "/dev/null" {
		return nil, nil, errors.New("openshell: snapshot hooks must remain disabled")
	}
	attributes, err := readOpenShellFile(filepath.Join(snapshot, ".git", "info", "attributes"), 1024)
	if err != nil || string(attributes) != "* -filter -text -ident -working-tree-encoding -eol\n" {
		return nil, nil, errors.New("openshell: snapshot filters must remain disabled")
	}
	var recovered []*OpenShellRun
	var aside *OpenShellSetAside
	current := r.Revision
	missing := false
	if len(run.Stages) > len(teams) {
		return nil, nil, errors.New("openshell: unexpected stage checkpoints")
	}
	if err := checkOpenShellSetAside(r.RunDir, len(teams), run); err != nil {
		return nil, nil, err
	}
	for i, team := range teams {
		for _, s := range run.SetAside {
			if s.Stage == i+1 && (missing || s.Revision != current) {
				return nil, nil, fmt.Errorf("openshell: stage %d: a set-aside run does not match the snapshot lineage", i+1)
			}
		}
		dir := openShellStageDir(r.RunDir, i+1, run.reruns(i+1))
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) && i >= len(run.Stages) {
			missing = true
			continue
		}
		fail := func(err error) ([]*OpenShellRun, *OpenShellSetAside, error) {
			return nil, nil, fmt.Errorf("openshell: stage %d cannot be reused: %w", i+1, err)
		}
		if err != nil || !info.IsDir() || missing || i >= len(run.Stages) {
			return fail(errors.New("missing, redirected or out-of-order stage; inspect and clean up before starting a new workflow"))
		}
		record := run.Stages[i]
		if record.Stage != i+1 || record.RunRecord != filepath.Join(dir, "run.json") || record.Revision != current {
			return fail(errors.New("snapshot lineage does not match the checkpoint"))
		}
		if record.Verdict == "interrupted" {
			// A cancellation stopped this run's workers before it was
			// verified: nothing of it is reused, and the stage runs again.
			if i != len(run.Stages)-1 {
				return fail(errors.New("only the last recorded stage can have been interrupted"))
			}
			aside = setAsideOpenShellStage(team, record)
			missing = true
			continue
		}
		var stage OpenShellRun
		decodeErr := error(nil)
		if _, decodeErr = decodeOpenShellSequence(record.RunRecord, &stage); decodeErr != nil || record.Verdict == "running" {
			if record.Verdict == "running" && i == len(run.Stages)-1 && (decodeErr != nil || func() error {
				check := &OpenShellRunner{Repo: snapshot, Revision: current, RunDir: dir}
				return check.revalidateSequenceStage(ctx, team, &stage, run.Provenance)
			}() != nil) {
				// The controller died mid-stage. An unfinished record is not
				// replayed; the stage runs again and the stopped spend counts.
				aside = setAsideOpenShellStage(team, record)
				missing = true
				continue
			}
		}
		if decodeErr != nil {
			return fail(errors.New("no completed stage record; in-flight work is not replayed"))
		}
		check := &OpenShellRunner{Repo: snapshot, Revision: current, RunDir: dir}
		if err := check.revalidateSequenceStage(ctx, team, &stage, run.Provenance); err != nil {
			return fail(err)
		}
		if stage.Runtime != r.Runtime || stage.Director != r.DirectorName || stage.Concurrency != r.Concurrency {
			return fail(errors.New("execution settings changed"))
		}
		if record.Verdict == "pass" && record.Tree != stage.Integrated.Tree {
			return fail(errors.New("verified tree changed"))
		}
		if i+1 < len(teams) {
			current, err = openShellSnapshotCommit(ctx, snapshot, stage.Integrated.Tree, current)
			if err != nil {
				return fail(err)
			}
			if record.NextRevision != "" && record.NextRevision != current {
				return fail(errors.New("successor snapshot changed"))
			}
		}
		recovered = append(recovered, &stage)
	}
	if len(recovered) == 0 && aside == nil {
		return nil, nil, errors.New("openshell: no verified stage to resume")
	}
	return recovered, aside, nil
}

// checkOpenShellSetAside validates the stopped stage runs a sequence kept:
// each names its own directory, in order, and carries sane counts.
func checkOpenShellSetAside(runDir string, stages int, run *OpenShellRun) error {
	seen := map[int]int{}
	for _, s := range run.SetAside {
		k := seen[s.Stage]
		seen[s.Stage]++
		if s.Stage < 1 || s.Stage > stages || s.Stage > len(run.Stages)+1 ||
			s.RunRecord != filepath.Join(openShellStageDir(runDir, s.Stage, k), "run.json") ||
			s.Requests < 0 || s.Tokens < 0 || s.CostUSD < 0 {
			return errors.New("openshell: invalid set-aside stage record")
		}
		if s.Attempts != nil {
			if err := s.Attempts.Validate(); err != nil {
				return err
			}
		}
		info, err := os.Lstat(filepath.Dir(s.RunRecord))
		if err != nil || !info.IsDir() {
			return errors.New("openshell: a set-aside stage directory is missing or redirected")
		}
	}
	return nil
}

func (r *OpenShellRunner) revalidateSequenceStage(ctx context.Context, team OpenShellTeam, run *OpenShellRun, provenance *OpenShellProvenance) error {
	if run.Team != team.ID || run.Repo != r.Repo || run.Revision != r.Revision || !run.RequireAll ||
		run.Verdict != "pass" || run.Error != "" || run.Integrated == nil || len(run.Tasks) != len(team.Tasks) ||
		!reflect.DeepEqual(run.Provenance, provenance) {
		return errors.New("stage is incomplete or its identity changed; in-flight work is not replayed")
	}
	var allowed []string
	baseTree, err := r.treeWith(ctx, nil)
	if err != nil {
		return err
	}
	for i, task := range team.Tasks {
		res := run.Tasks[i]
		if res == nil || res.Task != task.ID || res.Profile != task.Profile || res.Mode != task.Mode ||
			res.Error != "" || res.State != "" || res.Report == nil || res.NotStarted ||
			(res.Outcome != OpenShellLanded && res.Outcome != OpenShellDropped && res.Outcome != OpenShellUnchanged) {
			return errors.New("worker identity, completion or cleanup is unverified")
		}
		if err := checkOpenShellReport(res.Report, task.ID, openShellTaskChecks); err != nil {
			return err
		}
		export := res.Report.Export
		if export == nil || export.Patch != "result.patch" || export.BaseRevision != r.Revision ||
			export.Verify.ExitCode != 0 || !slices.Equal(export.Verify.Argv, task.Verify) {
			return errors.New("worker export does not match saved verification")
		}
		evidence := filepath.Join(r.RunDir, "tasks", task.ID)
		if res.Evidence != evidence {
			return errors.New("worker evidence path changed")
		}
		resolved, err := filepath.EvalSymlinks(evidence)
		if err != nil || resolved != evidence {
			return errors.New("worker evidence redirected")
		}
		patch, err := readOpenShellFile(filepath.Join(evidence, "result.patch"), openShellPatchLimit)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(patch)) != export.PatchSHA256 {
			return errors.New("worker export checksum changed")
		}
		if (task.Mode == "review") != (len(patch) == 0) {
			return errors.New("worker export does not match edit or review mode")
		}
		files, err := checkOpenShellPatch(ctx, r.Repo, patch, task.Allowed)
		if err != nil {
			return err
		}
		if !slices.Equal(files, sortedCopy(export.ChangedFiles)) {
			return errors.New("worker export scope changed")
		}
		if _, err := r.treeWith(ctx, patch); err != nil {
			return err
		}
		if baseTree != export.Tree {
			return errors.New("worker export does not match its starting tree")
		}
		allowed = append(allowed, task.Allowed...)
	}
	usage := run.measuredAttempts(team.Tasks...)
	if run.AttemptUsage != nil && *run.AttemptUsage != *usage {
		return errors.New("stage attempt usage does not match worker reports and rulings")
	}
	run.AttemptUsage = usage
	in := run.Integrated
	if in.Report == nil || in.State != "" || in.Error != "" || !slices.Equal(in.Verify, team.Tasks[0].Verify) {
		return errors.New("integrated verification or cleanup is unverified")
	}
	if err := checkOpenShellReport(in.Report, "integrated", openShellVerifyChecks); err != nil {
		return err
	}
	if in.Report.BaselineExitCode == nil || *in.Report.BaselineExitCode != 0 {
		return errors.New("integrated verification did not pass")
	}
	export, err := r.verifiedExport(ctx, run)
	if err != nil {
		return err
	}
	for _, file := range export.Manifest.ChangedFiles {
		if !slices.Contains(allowed, file) {
			return errors.New("integrated export is outside the saved scope")
		}
	}
	return nil
}
