package captaincode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

func ParseOpenShellWorkflow(task string) (Workflow, error) {
	wf, err := ParseWorkflow(task)
	if !LooksLikeWorkflow(task) {
		if err == nil && wf.HasGate() {
			return Workflow{}, errors.New("openshell: host gates are unsupported; use CAPTAIN_OPENSHELL_VERIFY")
		}
		if err == nil && len(wf.Stages) == 1 && len(wf.Stages[0].Legs) == 1 &&
			wf.Stages[0].Legs[0].Leg == LegOpenShell && isOpenShellReview(wf.Stages[0].Legs[0].Prompt) {
			return wf, validateOpenShellWorkflow(wf)
		}
		return Workflow{}, nil
	}
	if err != nil {
		return Workflow{}, err
	}
	return wf, validateOpenShellWorkflow(wf)
}

func isOpenShellReview(prompt string) bool {
	fields := strings.Fields(prompt)
	return len(fields) > 0 && fields[0] == "--review"
}

func validateOpenShellWorkflow(wf Workflow) error {
	if len(wf.Stages) == 0 || len(wf.Stages) > MaxWorkflowStages {
		return fmt.Errorf("openshell: a workflow needs 1-%d stages", MaxWorkflowStages)
	}
	total := 0
	for _, stage := range wf.Stages {
		if n := len(stage.Legs); n == 0 || n > MaxStageWidth {
			return fmt.Errorf("openshell: a stage needs 1-%d workers", MaxStageWidth)
		}
		total += len(stage.Legs)
		for _, worker := range stage.Legs {
			if worker.Leg != LegOpenShell {
				return errors.New(MixedWorkflowRefusal)
			}
			if worker.Gate != "" {
				return errors.New("openshell: host gates are unsupported; use CAPTAIN_OPENSHELL_VERIFY")
			}
			if worker.Prompt == "" {
				return errors.New("openshell: every worker needs an assignment")
			}
		}
	}
	if total > MaxWorkflowRuns {
		return fmt.Errorf("openshell: a workflow supports at most %d workers", MaxWorkflowRuns)
	}
	return nil
}

func openShellWorkflowTeam(template OpenShellTeam, wf Workflow, history string) (OpenShellTeam, error) {
	if err := validateOpenShellWorkflow(wf); err != nil {
		return OpenShellTeam{}, err
	}
	if len(wf.Stages) != 1 {
		return OpenShellTeam{}, errors.New("openshell: build one stage at a time")
	}
	if len(template.Tasks) != 1 {
		return OpenShellTeam{}, errors.New("openshell: workflow requires one configured task template")
	}
	team := OpenShellTeam{Schema: 1}
	for i, worker := range wf.Stages[0].Legs {
		task := template.Tasks[0]
		task.ID = fmt.Sprintf("w%d-openshell", i+1)
		task.Prompt = worker.Prompt
		if isOpenShellReview(task.Prompt) {
			task.Mode = "review"
			task.Prompt = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(task.Prompt), "--review"))
			task.Allowed, task.Baseline, task.RepairAttempts = nil, "pass", 0
			if task.Prompt == "" {
				return OpenShellTeam{}, errors.New("openshell: review needs an assignment")
			}
		}
		if history != "" {
			task.Prompt = history + "\n[user]\n" + task.Prompt
		}
		team.Tasks = append(team.Tasks, task)
	}
	data, err := json.Marshal(team.Tasks)
	if err != nil {
		return team, err
	}
	team.ID = fmt.Sprintf("parallel-%x", sha256.Sum256(data))[:21]
	return team, team.Validate()
}

func advisoryHostReviewDisabled() bool { return os.Getenv("CAPTAIN_OPENSHELL_ADVISORY_REVIEW") == "0" }

func defaultAdvisoryHostReview(ctx context.Context, task string, diff []byte) (string, error) {
	reviewCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directorTimeout)
	defer cancel()
	prompt := "You are reviewing a sandbox agent's work. The agent cannot access the internet, your machine or your tools. Its output is a patch the sandbox controller verified.\n\n"
	prompt += "Task: " + task + "\n\nPatch:\n" + string(diff) + "\n\n"
	prompt += "Check the patch for correctness, completeness and safety. Report what you find. Do NOT apply or sign anything. This review is advisory only."
	text, err := toolLessClaude(reviewCtx, prompt)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	return text, nil
}

func advisoryHostReview(ctx context.Context, task string, diff []byte) (string, error) {
	if advisoryHostReviewDisabled() || len(diff) == 0 {
		return "", nil
	}
	if fn := openShellReviewerFromContext(ctx); fn != nil {
		text, err := fn(ctx, task, diff)
		if err != nil || text == "" {
			return "", err
		}
		return "\n[advisory host review]\n" + text, nil
	}
	text, err := defaultAdvisoryHostReview(ctx, task, diff)
	if err != nil || text == "" {
		return "", err
	}
	return "\n[advisory host review]\n" + text, nil
}

func (ws Workspace) RunOpenShellWorkflow(ctx context.Context, wf Workflow, history string) (Result, error) {
	if err := validateOpenShellWorkflow(wf); err != nil {
		return openShellRefused(err)
	}
	ctx, stop, err := OpenShellBudgetContext(ctx, nil)
	if err != nil {
		return openShellRefused(err)
	}
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, workerTimeout())
	defer cancel()
	task := wf.PlainRequest()
	if task == "" {
		task = history
	}
	runner, template, err := openShellConfig(ctx, ws.Dir, "sandbox workflow")
	if err != nil {
		return openShellRefused(err)
	}
	runner.Task = task
	if fn := openShellReviewerFromContext(ctx); fn != nil {
		runner.Reviewer = fn
	}
	teams := make([]OpenShellTeam, 0, len(wf.Stages))
	for i, stage := range wf.Stages {
		team, err := openShellWorkflowTeam(template, Workflow{Stages: []WorkflowStage{stage}}, history)
		if err != nil {
			return openShellRefused(err)
		}
		if len(wf.Stages) > 1 {
			team.ID = fmt.Sprintf("s%d-%s", i+1, team.ID)
			for j := range team.Tasks {
				team.Tasks[j].ID = fmt.Sprintf("s%d-%s", i+1, team.Tasks[j].ID)
				if i > 0 {
					team.Tasks[j].Baseline = "pass"
				}
			}
		}
		teams = append(teams, team)
	}
	runner.RequireAll = true
	var res Result
	var runErr error
	if len(teams) == 1 {
		res, runErr = runConfiguredOpenShell(ctx, runner, teams[0], ws.Steer)
	} else {
		if err := configureOpenShellRunDir(runner, "workflow"); err != nil {
			return openShellRefused(err)
		}
		res, runErr = runOpenShellSequence(ctx, runner, teams, ws.Steer)
	}
	return res, runErr
}
