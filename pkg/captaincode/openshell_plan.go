package captaincode

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type OpenShellTeamPlan struct {
	Workflow    Workflow
	Rationale   string
	Assignments []string
	Stages      []OpenShellPlanStage
	// DirectorAttempts counts the planner's model calls, a malformed reply's
	// retry included, whether or not planning succeeded.
	DirectorAttempts int
}

type OpenShellPlanStage struct {
	Mode        string   `json:"mode"`
	Assignments []string `json:"assignments"`
}

type OpenShellPlanRecord struct {
	Stages           []OpenShellPlanStage `json:"stages,omitempty"`
	Rationale        string               `json:"rationale,omitempty"`
	Assignments      []string             `json:"assignments"`
	DirectorAttempts int                  `json:"director_attempts"`
}

// Record is the plan as the ledger keeps it.
func (p OpenShellTeamPlan) Record() OpenShellPlanRecord {
	return OpenShellPlanRecord{Rationale: p.Rationale, Assignments: slices.Clone(p.Assignments), Stages: cloneOpenShellPlanStages(p.Stages), DirectorAttempts: p.DirectorAttempts}
}

// RecordOpenShellPlan keeps a running sandbox attempt's plan, once.
func (l *Ledger) RecordOpenShellPlan(attemptID string, plan OpenShellPlanRecord) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	as := l.AttemptStateFor(attemptID)
	if as == nil || as.Leg != LegOpenShell || as.State != StateRunning {
		return errors.New("openshell: a team plan requires a running sandbox attempt")
	}
	if as.OpenShellPlan != nil {
		return errors.New("openshell: attempt already has a team plan")
	}
	plan.Assignments = slices.Clone(plan.Assignments)
	plan.Stages = cloneOpenShellPlanStages(plan.Stages)
	as.OpenShellPlan = &plan
	as.UpdatedAt = time.Now()
	return nil
}

func cloneOpenShellPlanStages(stages []OpenShellPlanStage) []OpenShellPlanStage {
	cloned := slices.Clone(stages)
	for i := range cloned {
		cloned[i].Assignments = slices.Clone(cloned[i].Assignments)
	}
	return cloned
}

func (p OpenShellPlanRecord) Validate() error {
	invalid := errors.New("openshell: invalid team plan record")
	if len(p.Assignments) == 0 || len(p.Assignments) > MaxWorkflowRuns || p.DirectorAttempts < 0 || p.DirectorAttempts > openShellPlanSlots {
		return invalid
	}
	if p.Stages == nil {
		if len(p.Assignments) > MaxStageWidth {
			return invalid
		}
		return nil
	}
	if len(p.Stages) == 0 || len(p.Stages) > MaxWorkflowStages {
		return invalid
	}
	var assignments []string
	for _, stage := range p.Stages {
		if stage.Mode != "edit" && stage.Mode != "review" || len(stage.Assignments) == 0 || len(stage.Assignments) > MaxStageWidth {
			return invalid
		}
		for _, brief := range stage.Assignments {
			if strings.TrimSpace(brief) == "" || !utf8.ValidString(brief) || strings.ContainsRune(brief, 0) || utf8.RuneCountInString(brief) > openShellPlanBriefLimit {
				return invalid
			}
		}
		assignments = append(assignments, stage.Assignments...)
	}
	if !slices.Equal(assignments, p.Assignments) {
		return invalid
	}
	return nil
}

const (
	// openShellPlanSlots is the planner's worst case: one call, and one
	// retry after a reply with no valid JSON.
	openShellPlanSlots = 2
	// openShellPlanBriefLimit bounds one assignment. The composed prompt,
	// conversation included, must still fit a task's 16384 characters.
	openShellPlanBriefLimit = 4000
	openShellPromptLimit    = 16384
)

// PlanOpenShellTeam asks the tool-less director to plan sandbox stages.
// A planned team needs the director that
// also rules on conflicts (CAPTAIN_OPENSHELL_DIRECTOR=claude) and a complete
// sandbox configuration; both, and the attempt cap, are checked before any
// model call. The director sees the task and the editable paths only: no
// conversation, memory or repository content, and it has no tools.
func PlanOpenShellTeam(ctx context.Context, dir, task, history string) (OpenShellTeamPlan, error) {
	return planOpenShellTeam(ctx, dir, task, history, toolLessClaude)
}

func planOpenShellTeam(ctx context.Context, dir, task, history string, ask func(context.Context, string) (string, error)) (OpenShellTeamPlan, error) {
	if openShellStrictCost(ctx) > 0 {
		return OpenShellTeamPlan{}, fmt.Errorf("%w: the planner is an unpriced host call; type explicit /openshell stages instead", ErrOpenShellCostCap)
	}
	runner, template, err := openShellConfig(ctx, dir, "sandbox team plan")
	if err != nil {
		return OpenShellTeamPlan{}, err
	}
	if runner.Director == nil {
		return OpenShellTeamPlan{}, errors.New("openshell: a planned sandbox team needs CAPTAIN_OPENSHELL_DIRECTOR=claude, the director that also rules on conflicts")
	}
	task = strings.TrimSpace(task)
	if task == "" || !utf8.ValidString(task) || strings.ContainsRune(task, 0) {
		return OpenShellTeamPlan{}, errors.New("openshell: a planned sandbox team needs a task")
	}
	room := openShellPromptLimit
	for _, mode := range []string{"edit", "review"} {
		assignment := openShellStageAssignment(MaxWorkflowStages, MaxWorkflowStages, MaxStageWidth, MaxStageWidth, mode, task, "")
		room = min(room, openShellPromptLimit-utf8.RuneCountInString(openShellWorkerPrompt(history, assignment)))
	}
	limit := min(openShellPlanBriefLimit, room)
	if limit < 200 {
		return OpenShellTeamPlan{}, errors.New("openshell: the conversation leaves no room for a planned assignment; shorten it or type explicit /openshell stages")
	}
	if cap, _ := ctx.Value(openShellAttemptLimitKey{}).(int); cap > 0 && cap <= openShellPlanSlots {
		return OpenShellTeamPlan{}, fmt.Errorf("%w: planning needs up to %d attempt slots and a worker one more; cap is %d", ErrOpenShellAttemptCap, openShellPlanSlots, cap)
	}
	attempts := &openShellDirectorAttempts{}
	plan, err := askOpenShellPlan(context.WithValue(ctx, openShellDirectorAttemptsKey{}, attempts), template, task, history, limit, ask)
	plan.DirectorAttempts = attempts.count
	return plan, err
}

func askOpenShellPlan(ctx context.Context, template OpenShellTeam, task, history string, limit int, ask func(context.Context, string) (string, error)) (OpenShellTeamPlan, error) {
	type worker struct {
		Brief string `json:"brief"`
	}
	type stage struct {
		Mode    string   `json:"mode"`
		Workers []worker `json:"workers"`
	}
	type replyPlan struct {
		Rationale string   `json:"rationale"`
		Workers   []worker `json:"workers"`
		Stages    []stage  `json:"stages"`
	}
	var reply replyPlan
	prompt := directorConstraint + openShellPlanPrompt(task, template.Tasks[0].Allowed, limit)
	text, err := ask(ctx, prompt)
	if err != nil {
		return OpenShellTeamPlan{}, fmt.Errorf("openshell: plan: %w", err)
	}
	if extractJSON(text, &reply) != nil {
		retry := prompt + "\n\nYour previous reply did not contain valid JSON - it was:\n" + truncateStr(text, 300) +
			"\n\nThat is not acceptable. Reply again with ONLY the JSON object, no other text."
		if text, err = ask(ctx, retry); err != nil {
			return OpenShellTeamPlan{}, fmt.Errorf("openshell: plan: %w", err)
		}
		reply = replyPlan{}
		if err := extractJSON(text, &reply); err != nil {
			return OpenShellTeamPlan{}, fmt.Errorf("openshell: no valid JSON in the director's plan after retry (%w)", err)
		}
	}
	if reply.Stages != nil && reply.Workers != nil {
		return OpenShellTeamPlan{}, errors.New("openshell: the director must return stages or workers, not both")
	}
	stages := reply.Stages
	if stages == nil {
		stages = []stage{{Mode: "edit", Workers: reply.Workers}}
	}
	if len(stages) == 0 || len(stages) > MaxWorkflowStages {
		return OpenShellTeamPlan{}, fmt.Errorf("openshell: a plan needs 1-%d stages", MaxWorkflowStages)
	}
	plan := OpenShellTeamPlan{Rationale: truncateStr(oneLine(reply.Rationale), 200)}
	total := 0
	for s, stage := range stages {
		if stage.Mode == "" {
			stage.Mode = "edit"
		}
		if stage.Mode != "edit" && stage.Mode != "review" {
			return OpenShellTeamPlan{}, errors.New("openshell: planned stage mode must be edit or review")
		}
		if len(stage.Workers) == 0 || len(stage.Workers) > MaxStageWidth {
			return OpenShellTeamPlan{}, fmt.Errorf("openshell: the director planned %d workers; a sandbox stage takes 1-%d", len(stage.Workers), MaxStageWidth)
		}
		total += len(stage.Workers)
		if total > MaxWorkflowRuns {
			return OpenShellTeamPlan{}, fmt.Errorf("openshell: a plan supports at most %d workers", MaxWorkflowRuns)
		}
		var briefs []string
		seen := map[string]bool{}
		for _, w := range stage.Workers {
			brief := strings.TrimSpace(w.Brief)
			if brief == "" || !utf8.ValidString(brief) || strings.ContainsRune(brief, 0) || utf8.RuneCountInString(brief) > limit {
				return OpenShellTeamPlan{}, fmt.Errorf("openshell: the director's plan has an assignment that is empty or over %d characters", limit)
			}
			if key := strings.Join(strings.Fields(brief), " "); !seen[key] {
				seen[key] = true
				briefs = append(briefs, brief)
			}
		}
		workflowStage := WorkflowStage{}
		for i, brief := range briefs {
			workflowStage.Legs = append(workflowStage.Legs, WorkflowLeg{Leg: LegOpenShell,
				Prompt: openShellStageAssignment(s+1, len(stages), i+1, len(briefs), stage.Mode, task, brief)})
		}
		if _, err := openShellWorkflowTeam(template, Workflow{Stages: []WorkflowStage{workflowStage}}, history); err != nil {
			return OpenShellTeamPlan{}, err
		}
		plan.Workflow.Stages = append(plan.Workflow.Stages, workflowStage)
		plan.Stages = append(plan.Stages, OpenShellPlanStage{Mode: stage.Mode, Assignments: briefs})
		plan.Assignments = append(plan.Assignments, briefs...)
	}
	if err := validateOpenShellWorkflow(plan.Workflow); err != nil {
		return OpenShellTeamPlan{}, err
	}
	return plan, nil
}

func openShellPlanPrompt(task string, allowed []string, limit int) string {
	return fmt.Sprintf(`You are Captain Code's director. Plan a coding task as bounded stages of isolated sandbox workers.

How the workers run:
- Workers within a stage run in parallel from the same repository snapshot. Each sees the conversation, the team task and its own assignment.
- Each worker may change only these paths: %s
- Each edit is verified independently, then the combined tree is verified in a fresh sandbox. Overlapping files get one whole winner, never a blend of patches.
- A later stage starts only after the previous stage passes and receives its exact verified repository tree. Previous answers and review prose are not forwarded as instructions.
- A review stage must leave the tree unchanged and pass the configured tests. Its prose is not an approval and cannot authorize later actions.

Rules:
- Return 1 to %d stages, 1 to %d workers per stage, and at most %d workers in total.
- Prefer one stage and one worker for a small task. Add stages only for real dependencies or an explicitly requested review.
- Use mode "edit" or "review" for each stage. All workers in a review stage are read-only. Do not mix review and editing in the same stage.
- Give parallel workers different files. Each stage must pass the same configured tests independently; never split mutually dependent edits across workers or stages.
- Make each assignment self-contained with its goal, constraints and paths, in at most %d characters. Never ask parallel workers to coordinate, wait for or review each other.
- Use only the configured runtime, profile, allowed paths and tests; do not return host legs, shell gates or new configuration.

Task:
%s

Reply with STRICT JSON only: {"rationale":"<one line, <=140 chars>","stages":[{"mode":"edit","workers":[{"brief":"<assignment>"}]}]}`,
		strings.Join(allowed, ", "), MaxWorkflowStages, MaxStageWidth, MaxWorkflowRuns, limit, task)
}

func openShellStageAssignment(stage, stages, i, n int, mode, task, brief string) string {
	if mode == "review" {
		return fmt.Sprintf("--review [captain] Sandbox review worker %d of %d in stage %d of %d. Inspect the current verified snapshot without changing files; run the configured checks. Your prose is not an approval and is not forwarded to later stages.\n\nTeam task:\n%s\n\nYour assignment:\n%s", i, n, stage, stages, task, brief)
	}
	prompt := openShellAssignment(i, n, task, brief)
	if stages > 1 {
		prompt = fmt.Sprintf("[captain] Stage %d of %d. This stage receives the previous stage's verified tree, or the pinned base for stage one. Complete only this stage's assignment.\n\n%s", stage, stages, prompt)
	}
	return prompt
}

// openShellAssignment is worker i of n's prompt. It never starts with
// "--review": a planned worker always edits.
func openShellAssignment(i, n int, task, brief string) string {
	prompt := fmt.Sprintf("[captain] Sandbox worker %d of %d on a director-planned team. Every worker edits its own copy of the repository; verified patches land together, and when two workers change the same file only one worker's changes land.\n\nTeam task:\n%s\n\nYour assignment:\n%s", i, n, task, brief)
	if n > 1 {
		prompt += "\n\nStay inside your assignment; the other workers cover the rest."
	}
	return prompt
}

// openShellWorkerPrompt is the prompt openShellWorkflowTeam builds from an
// assignment and the conversation.
func openShellWorkerPrompt(history, assignment string) string {
	if history == "" {
		return assignment
	}
	return history + "\n[user]\n" + assignment
}

// WithOpenShellAttemptsSpent lowers the attempt cap ctx carries by n attempts
// the task already spent, such as the director's plan. A spent cap is
// refused: an exhausted limit must not read as "no limit".
func WithOpenShellAttemptsSpent(ctx context.Context, n int) (context.Context, error) {
	cap, _ := ctx.Value(openShellAttemptLimitKey{}).(int)
	if cap <= 0 || n <= 0 {
		return ctx, nil
	}
	if n >= cap {
		return ctx, fmt.Errorf("%w: %d attempts already spent; cap is %d", ErrOpenShellAttemptCap, n, cap)
	}
	return context.WithValue(ctx, openShellAttemptLimitKey{}, cap-n), nil
}
