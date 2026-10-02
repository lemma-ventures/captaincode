package captaincode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// OpenShellTeamPlan is the director's split of one task into parallel
// sandbox assignments. Its workflow is one stage of /openshell workers and
// runs exactly like typed "/openshell A + /openshell B" stages.
type OpenShellTeamPlan struct {
	Workflow    Workflow
	Rationale   string
	Assignments []string // the director's briefs, one per worker, in order
	// DirectorAttempts counts the planner's model calls, a malformed reply's
	// retry included, whether or not planning succeeded.
	DirectorAttempts int
}

// OpenShellPlanRecord is what a planned sandbox team was asked to do, kept on
// its attempt: run.json holds only a digest of the worker prompts, and the
// sandboxes that held them are gone once the run ends.
type OpenShellPlanRecord struct {
	Rationale        string   `json:"rationale,omitempty"`
	Assignments      []string `json:"assignments"`
	DirectorAttempts int      `json:"director_attempts"`
}

// Record is the plan as the ledger keeps it.
func (p OpenShellTeamPlan) Record() OpenShellPlanRecord {
	return OpenShellPlanRecord{Rationale: p.Rationale, Assignments: append([]string(nil), p.Assignments...), DirectorAttempts: p.DirectorAttempts}
}

// RecordOpenShellPlan keeps a running sandbox attempt's plan, once.
func (l *Ledger) RecordOpenShellPlan(attemptID string, plan OpenShellPlanRecord) error {
	if len(plan.Assignments) == 0 || len(plan.Assignments) > MaxStageWidth || plan.DirectorAttempts < 0 || plan.DirectorAttempts > openShellPlanSlots {
		return errors.New("openshell: invalid team plan record")
	}
	as := l.AttemptStateFor(attemptID)
	if as == nil || as.Leg != LegOpenShell || as.State != StateRunning {
		return errors.New("openshell: a team plan requires a running sandbox attempt")
	}
	if as.OpenShellPlan != nil {
		return errors.New("openshell: attempt already has a team plan")
	}
	plan.Assignments = append([]string(nil), plan.Assignments...)
	as.OpenShellPlan = &plan
	as.UpdatedAt = time.Now()
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

// PlanOpenShellTeam asks the tool-less director to split task into 1 to
// MaxStageWidth sandbox assignments. A planned team needs the director that
// also rules on conflicts (CAPTAIN_OPENSHELL_DIRECTOR=claude) and a complete
// sandbox configuration; both, and the attempt cap, are checked before any
// model call. The director sees the task and the editable paths only: no
// conversation, memory or repository content, and it has no tools.
func PlanOpenShellTeam(ctx context.Context, dir, task, history string) (OpenShellTeamPlan, error) {
	return planOpenShellTeam(ctx, dir, task, history, toolLessClaude)
}

func planOpenShellTeam(ctx context.Context, dir, task, history string, ask func(context.Context, string) (string, error)) (OpenShellTeamPlan, error) {
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
	room := openShellPromptLimit - utf8.RuneCountInString(openShellWorkerPrompt(history, openShellAssignment(MaxStageWidth, MaxStageWidth, task, "")))
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
	var reply struct {
		Rationale string `json:"rationale"`
		Workers   []struct {
			Brief string `json:"brief"`
		} `json:"workers"`
	}
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
		if err := extractJSON(text, &reply); err != nil {
			return OpenShellTeamPlan{}, fmt.Errorf("openshell: no valid JSON in the director's plan after retry (%w)", err)
		}
	}
	var briefs []string
	seen := map[string]bool{}
	for _, w := range reply.Workers {
		brief := strings.TrimSpace(w.Brief)
		if brief == "" || !utf8.ValidString(brief) || strings.ContainsRune(brief, 0) || utf8.RuneCountInString(brief) > limit {
			return OpenShellTeamPlan{}, fmt.Errorf("openshell: the director's plan has an assignment that is empty or over %d characters", limit)
		}
		// Two identical assignments would only produce the same patch twice.
		if key := strings.Join(strings.Fields(brief), " "); !seen[key] {
			seen[key] = true
			briefs = append(briefs, brief)
		}
	}
	if len(briefs) == 0 || len(reply.Workers) > MaxStageWidth {
		return OpenShellTeamPlan{}, fmt.Errorf("openshell: the director planned %d workers; a sandbox team takes 1-%d", len(reply.Workers), MaxStageWidth)
	}
	stage := WorkflowStage{}
	for i, brief := range briefs {
		stage.Legs = append(stage.Legs, WorkflowLeg{Leg: LegOpenShell, Prompt: openShellAssignment(i+1, len(briefs), task, brief)})
	}
	plan := OpenShellTeamPlan{Workflow: Workflow{Stages: []WorkflowStage{stage}}, Rationale: truncateStr(oneLine(reply.Rationale), 200), Assignments: briefs}
	if _, err := openShellWorkflowTeam(template, plan.Workflow, history); err != nil {
		return OpenShellTeamPlan{}, err
	}
	return plan, nil
}

func openShellPlanPrompt(task string, allowed []string, limit int) string {
	return fmt.Sprintf(`You are Captain Code's director. Split one coding task into assignments for isolated sandbox workers that run in parallel.

How the workers run:
- Each worker starts from the same repository snapshot in its own sandbox. It sees the conversation, the task and its own assignment, never the other workers.
- A worker may change only these paths: %s
- Each worker's patch is verified on its own, then the patches land together and the combined tree is verified again.
- When two workers change the same file, only one worker's changes land; the others are set aside, not merged.

Rules:
- Return 1 to %d assignments. Return exactly one when the task is small or cannot be split into parts that change different files.
- Give each worker different files to change.
- Make each assignment self-contained: restate the goal, constraints and file paths it needs from the task, in at most %d characters. Never ask workers to coordinate, wait for or review each other.

Task:
%s

Reply with STRICT JSON only: {"rationale":"<one line, <=140 chars>","workers":[{"brief":"<assignment>"}]}`,
		strings.Join(allowed, ", "), MaxStageWidth, limit, task)
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
