package captaincode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
)

type OpenShellStageRecord struct {
	Stage        int    `json:"stage"`
	RunRecord    string `json:"run_record"`
	Revision     string `json:"revision"`
	Tree         string `json:"tree,omitempty"`
	NextRevision string `json:"next_revision,omitempty"`
	Verdict      string `json:"verdict"`
}

func runOpenShellSequence(ctx context.Context, runner *OpenShellRunner, teams []OpenShellTeam, steer *Steer) (Result, error) {
	ctx, cancel, err := OpenShellBudgetContext(ctx, nil)
	if err != nil {
		return openShellRefused(err)
	}
	defer cancel()
	ctx, stop := runner.deadlineContext(ctx)
	defer stop()
	ctx, stopped, detach := interruptible(ctx, steer, LegOpenShell)
	defer detach()
	if !steer.Interrupted().IsZero() {
		return openShellRefused(ErrInterrupted)
	}
	lock, err := lockOpenShellSequence(runner.RunDir, true)
	if err != nil {
		return openShellRefused(err)
	}
	defer lock.Close()
	runner.RunDir, err = filepath.EvalSymlinks(runner.RunDir)
	if err != nil {
		return openShellRefused(err)
	}
	for _, name := range []string{"sequence.json", "run.json"} {
		if _, err := os.Lstat(filepath.Join(runner.RunDir, name)); !errors.Is(err, os.ErrNotExist) {
			return openShellRefused(errors.New("openshell: sequence already has state; use --resume"))
		}
	}
	run := &OpenShellRun{Version: ArtifactVersion, Team: filepath.Base(runner.RunDir), Repo: runner.Repo,
		Revision: runner.Revision, Runtime: runner.Runtime, Director: runner.DirectorName,
		Concurrency: runner.Concurrency, RequireAll: true, StartedAt: time.Now(), DeadlineAt: runner.DeadlineAt, Verdict: "fail"}
	err = runner.runSequence(ctx, teams, run, nil)
	if err != nil {
		run.Error = err.Error()
	}
	run.Seconds = seconds(time.Since(run.StartedAt))
	if werr := saveOpenShellSequence(runner.RunDir, run); werr != nil {
		err = errors.Join(err, werr)
	}
	return finishOpenShellRun(ctx, runner, run, err, stopped.Load())
}

func saveOpenShellSequence(dir string, run *OpenShellRun) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".run-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "run.json")); err != nil {
		return err
	}
	return syncOpenShellDir(dir)
}

func validateOpenShellSequence(teams []OpenShellTeam) ([]string, error) {
	if len(teams) < 2 || len(teams) > MaxWorkflowStages {
		return nil, errors.New("openshell: sequential workflow needs 2-4 stages")
	}
	var allowed []string
	var verify []string
	total := 0
	for _, team := range teams {
		if err := team.Validate(); err != nil {
			return nil, err
		}
		if len(team.Tasks) > MaxStageWidth {
			return nil, fmt.Errorf("openshell: at most %d workers per stage", MaxStageWidth)
		}
		total += len(team.Tasks)
		for _, task := range team.Tasks {
			if verify == nil {
				verify = task.Verify
			}
			if team.Verify != nil || !slices.Equal(verify, task.Verify) {
				return nil, errors.New("openshell: sequential stages require one shared verification argv")
			}
			for _, file := range task.Allowed {
				if !slices.Contains(allowed, file) {
					allowed = append(allowed, file)
				}
			}
		}
	}
	if total > MaxWorkflowRuns {
		return nil, fmt.Errorf("openshell: at most %d workers per workflow", MaxWorkflowRuns)
	}
	return allowed, nil
}

func (r *OpenShellRunner) runSequence(ctx context.Context, teams []OpenShellTeam, run *OpenShellRun, recovered []*OpenShellRun) error {
	run.AttemptUsage = &OpenShellAttemptUsage{}
	started, elapsed := time.Now(), run.Seconds
	defer func() { run.Seconds = elapsed + seconds(time.Since(started)) }()
	save := func() error {
		run.Seconds = elapsed + seconds(time.Since(started))
		return saveOpenShellSequence(r.RunDir, run)
	}
	allowed, err := validateOpenShellSequence(teams)
	if err != nil {
		return err
	}
	if err := r.check(ctx); err != nil {
		return err
	}
	budget, err := r.attemptBudget(ctx, teams)
	if recovered == nil {
		run.AttemptBudget = budget
	}
	if err != nil {
		return err
	}
	if recovered == nil {
		r.MaxAttempts = budget.Limit
		if err := r.saveSequencePlan(teams, run); err != nil {
			return err
		}
	}
	if err := save(); err != nil {
		return err
	}
	if recovered == nil {
		if err := recordOpenShellCheckpoint(ctx, r.RunDir, run.SequenceSHA256); err != nil {
			return fmt.Errorf("openshell: persist task checkpoint before dispatch: %w", err)
		}
	}
	snapshot := filepath.Join(r.RunDir, "snapshot")
	if recovered == nil {
		if _, err := gitOutput(ctx, r.RunDir, nil, nil, "-c", "init.templateDir=", "init", "--quiet", snapshot); err != nil {
			return err
		}
		snapshot, err = filepath.EvalSymlinks(snapshot)
		if err != nil {
			return err
		}
		if _, err := gitOutput(ctx, snapshot, nil, nil, "config", "core.hooksPath", "/dev/null"); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(snapshot, ".git", "info"), 0o700); err != nil {
			return err
		}
		if err := writeOpenShellFile(filepath.Join(snapshot, ".git", "info", "attributes"), []byte("* -filter -text -ident -working-tree-encoding -eol\n")); err != nil {
			return err
		}
		if _, err := gitOutput(ctx, snapshot, nil, nil, "-c", "core.hooksPath=/dev/null", "fetch", "--quiet", "--no-tags",
			"--no-recurse-submodules", "--no-auto-maintenance", "--depth=1", "--", r.Repo, r.Revision); err != nil {
			return err
		}
		if _, err := gitOutput(ctx, snapshot, nil, nil, "update-ref", "refs/captain/base", r.Revision); err != nil {
			return err
		}
	}
	current := r.Revision
	var last *OpenShellRun
	for i, team := range teams {
		if err := ctx.Err(); err != nil {
			return err
		}
		stage := &OpenShellRunner{Pilot: r.Pilot, Prepared: r.Prepared, StateRoot: r.StateRoot,
			Runtime: r.Runtime, Repo: snapshot, Revision: current, Concurrency: r.Concurrency,
			RunDir: filepath.Join(r.RunDir, fmt.Sprintf("stage-%d", i+1)), RequireAll: true,
			MaxAttempts: r.MaxAttempts, Pinned: run.Provenance,
			Director: r.Director, DirectorName: r.DirectorName, Log: r.Log}
		var result *OpenShellRun
		if i < len(recovered) {
			result = recovered[i]
		} else {
			if err := os.Mkdir(stage.RunDir, 0o700); err != nil {
				return err
			}
		}
		if i >= len(run.Stages) {
			run.Stages = append(run.Stages, OpenShellStageRecord{Stage: i + 1, Revision: current,
				RunRecord: filepath.Join(stage.RunDir, "run.json"), Verdict: "running"})
		}
		if err := save(); err != nil {
			return err
		}
		if result == nil {
			r.logf("stage %d/%d: snapshot %s", i+1, len(teams), current)
			result, err = stage.RunTeam(ctx, team)
		} else {
			r.logf("stage %d/%d: reused verified export", i+1, len(teams))
		}
		if result == nil {
			return fmt.Errorf("openshell: stage %d returned no result: %w", i+1, err)
		}
		record := &run.Stages[i]
		record.Verdict = result.Verdict
		run.Tasks = append(run.Tasks, result.Tasks...)
		run.AttemptUsage.add(result.AttemptUsage)
		if err != nil {
			return fmt.Errorf("openshell: stage %d: %w", i+1, err)
		}
		if !reflect.DeepEqual(result.Provenance, run.Provenance) {
			record.Verdict = "fail"
			return fmt.Errorf("openshell: stage %d ran under a build the sequence plan did not pin", i+1)
		}
		if _, err := stage.verifiedExport(ctx, result); err != nil {
			record.Verdict = "fail"
			return fmt.Errorf("openshell: stage %d handoff: %w", i+1, err)
		}
		record.Tree = result.Integrated.Tree
		last = result
		if i+1 < len(teams) {
			current, err = openShellSnapshotCommit(ctx, snapshot, record.Tree, current)
			if err != nil {
				return err
			}
			if _, err := gitOutput(ctx, snapshot, nil, nil, "update-ref", fmt.Sprintf("refs/captain/stage-%d", i+1), current); err != nil {
				return err
			}
			record.NextRevision = current
		}
		if err := save(); err != nil {
			return err
		}
		if err := recordOpenShellCheckpoint(ctx, r.RunDir, run.SequenceSHA256); err != nil {
			return fmt.Errorf("openshell: persist task checkpoint after stage %d: %w", i+1, err)
		}
		r.logf("stage %d/%d: checkpoint verified", i+1, len(teams))
	}
	patch, err := gitOutput(ctx, snapshot, nil, nil, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--no-renames",
		r.Revision, last.Integrated.Tree, "--")
	if err != nil {
		return err
	}
	files, err := checkOpenShellPatch(ctx, snapshot, patch, allowed)
	if err != nil {
		return err
	}
	if len(files) == 0 && !run.reviewOnly() {
		return errors.New("openshell: workflow produced no cumulative change")
	}
	integrated := *last.Integrated
	integrated.Patch = filepath.Join(r.RunDir, "integrated.patch")
	integrated.PatchSHA256 = fmt.Sprintf("%x", sha256.Sum256(patch))
	integrated.ChangedFiles = files
	if existing, err := readOpenShellFile(integrated.Patch, openShellPatchLimit); err == nil {
		if string(existing) != string(patch) {
			return errors.New("openshell: cumulative export changed after verification")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := writeOpenShellFile(integrated.Patch, patch); err != nil {
		return err
	}
	check := &OpenShellRunner{Repo: snapshot, Revision: r.Revision, RunDir: r.RunDir}
	candidate := *run
	candidate.Verdict, candidate.Integrated = "pass", &integrated
	if _, err := check.verifiedExport(ctx, &candidate); err != nil {
		return err
	}
	run.Integrated, run.Verdict = &integrated, "pass"
	return nil
}

func openShellSnapshotCommit(ctx context.Context, repo, tree, parent string) (string, error) {
	if !openShellObject.MatchString(tree) || !openShellObject.MatchString(parent) {
		return "", errors.New("openshell: invalid handoff object")
	}
	metadata, err := gitOutput(ctx, repo, nil, nil, "show", "-s", "--format=%an%x00%ae%x00%at%x00%cn%x00%ce%x00%ct", parent)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.TrimSuffix(string(metadata), "\n"), "\x00")
	if len(parts) != 6 {
		return "", errors.New("openshell: invalid snapshot identity")
	}
	env := append(os.Environ(), "GIT_AUTHOR_NAME="+parts[0], "GIT_AUTHOR_EMAIL="+parts[1], "GIT_AUTHOR_DATE=@"+parts[2]+" +0000",
		"GIT_COMMITTER_NAME="+parts[3], "GIT_COMMITTER_EMAIL="+parts[4], "GIT_COMMITTER_DATE=@"+parts[5]+" +0000")
	out, err := gitOutput(ctx, repo, env, nil, "-c", "commit.gpgsign=false", "commit-tree", tree, "-p", parent, "-m", "Verified sandbox snapshot")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(out))
	if !openShellObject.MatchString(commit) {
		return "", errors.New("openshell: invalid snapshot commit")
	}
	return commit, nil
}
