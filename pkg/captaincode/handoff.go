package captaincode

// Structured handoffs (ROADMAP M3.5). When a task finishes — successfully,
// failed, cancelled, or interrupted — the conversation's text output is the
// only thing the user sees. What the worker changed, which checks passed,
// which failed, what was interrupted, and what remains uncertain is scattered
// across charges, lifecycle states, integration candidates and budget
// records that a reader has to know to ask for.
//
// M3.5 assembles those into one compact HandoffBrief: the original
// requirements, the work each worker completed, the artifacts it verified, the
// checks that failed, the actions that remain, and the side effects whose
// state is uncertain. The brief is built from the data M3.1–M3.4 already
// persist; it does not create new state. It is what a developer or a
// downstream host (M4) reads to decide whether to accept, retry, or hand off.
//
// The brief is deliberately NOT an acceptance decision. A check that passed
// is a fact; whether that fact is sufficient is the reviewer's call (M1.3).
// A side effect marked uncertain is a flag for the reader, not a command to
// act — M3.4's resume policy still decides what to do about interrupted work.

import (
	"fmt"
	"strings"
	"time"
)

// HandoffVersion is stamped on every brief. A reader that does not understand
// the shape refuses it, the same contract the other versioned types carry.
const HandoffVersion = 1

// HandoffBrief is a compact summary of one task's outcome: what was asked,
// what was done, what was verified, what failed, what remains, and what is
// uncertain. It is built from the ledger's persisted state, not from
// conversation text.
type HandoffBrief struct {
	Version      int            `json:"version"`
	TaskID       string         `json:"task_id"`
	Requirements string         `json:"requirements"`
	State        LifecycleState `json:"state"`
	StopReason   string         `json:"stop_reason,omitempty"`

	// Work is what each worker attempt did, in execution order. Each entry
	// carries the leg, the stage it belonged to, its outcome, and its
	// duration. This is the "completed work" section.
	Work []WorkSummary `json:"work,omitempty"`

	// Artifacts are the verified file changes from M3.2: what each worker
	// touched, the diff digest, and whether its checks passed. Derived from
	// the IntegrationCandidate's PatchManifests.
	Artifacts []ArtifactSummary `json:"artifacts,omitempty"`

	// Integration is the M3.2 integration candidate status: clean,
	// conflicted, or empty, with the files in conflict.
	Integration *IntegrationSummary `json:"integration,omitempty"`

	// FailedChecks lists the checks that did not pass, with the worker and
	// the command that failed. This is what a repair or escalation would
	// target.
	FailedChecks []FailedCheck `json:"failed_checks,omitempty"`

	// RemainingActions are the things the task did not complete: interrupted
	// attempts, escalations that were not exhausted, stages that did not run.
	RemainingActions []string `json:"remaining_actions,omitempty"`

	// UncertainSideEffects are operations whose completion or effect cannot
	// be confirmed from the ledger: an interrupted attempt with no terminal
	// state, a charge with unknown usage, a worktree that was not cleaned up.
	UncertainSideEffects []string `json:"uncertain_side_effects,omitempty"`

	// Budget is the resource envelope summary: attempts used, cost, and
	// whether the ceiling was reached.
	Budget *BudgetSummary `json:"budget,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// WorkSummary is one worker's contribution to the task.
type WorkSummary struct {
	AttemptID  string         `json:"attempt_id"`
	StageID    string         `json:"stage_id,omitempty"`
	Leg        Leg            `json:"leg"`
	State      LifecycleState `json:"state"`
	DurationMs int64          `json:"duration_ms,omitempty"`
}

// ArtifactSummary is the verified-artifact view of one worker's changes.
type ArtifactSummary struct {
	AttemptID    string   `json:"attempt_id"`
	Leg          Leg      `json:"leg"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	DiffDigest   string   `json:"diff_digest,omitempty"`
	DiffPath     string   `json:"diff_path,omitempty"`
	CheckPassed  bool     `json:"check_passed"`
	CheckCommand string   `json:"check_command,omitempty"`
}

// IntegrationSummary is the M3.2 integration candidate in brief form.
type IntegrationSummary struct {
	Status       string   `json:"status"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	Conflicts    []string `json:"conflicts,omitempty"`
}

// FailedCheck is one check that did not pass.
type FailedCheck struct {
	AttemptID string `json:"attempt_id"`
	Leg       Leg    `json:"leg"`
	Command   string `json:"command,omitempty"`
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output,omitempty"`
}

// BudgetSummary is the resource envelope at task completion.
type BudgetSummary struct {
	AttemptsUsed int     `json:"attempts_used"`
	AttemptsMax  int     `json:"attempts_max,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	CostComplete bool    `json:"cost_complete"`
	UnknownUsage int     `json:"unknown_usage"`
}

// maxHandoffs caps the persisted brief log.
const maxHandoffs = 200

// BuildHandoffBrief assembles a compact brief from the ledger's persisted
// state for one task. It reads charges, lifecycle states, budgets, and
// integration candidates — the data M1.2–M3.4 already persist — and does not
// create new state. The requirements text is the original task prompt, passed
// by the caller because the ledger does not store it. The integration
// candidate may be nil when no parallel workers ran.
// (M3.2 parallel-workflow artifacts), or when the brain did not store one.
func BuildHandoffBrief(l *Ledger, taskID, requirements string, integration *IntegrationCandidate) HandoffBrief {
	brief := HandoffBrief{
		Version:      HandoffVersion,
		TaskID:       taskID,
		Requirements: requirements,
		CreatedAt:    time.Now(),
	}

	if ts := l.TaskStateFor(taskID); ts != nil {
		brief.State = ts.State
		brief.StopReason = ts.StopReason
	}

	brief.Work = buildWorkSummaries(l, taskID)
	brief.FailedChecks = buildFailedChecks(l, taskID, integration)
	brief.Artifacts = buildArtifactSummaries(l, taskID, integration)
	brief.RemainingActions = buildRemainingActions(l, taskID)
	brief.UncertainSideEffects = buildUncertainSideEffects(l, taskID)
	brief.Budget = buildBudgetSummary(l, taskID)

	if integration != nil {
		brief.Integration = &IntegrationSummary{
			Status:       integration.Status,
			ChangedFiles: integration.ChangedFiles(),
			Conflicts:    integration.ConflictFiles(),
		}
	}

	return brief
}

func buildWorkSummaries(l *Ledger, taskID string) []WorkSummary {
	attempts := l.AttemptStatesFor(taskID)
	out := make([]WorkSummary, 0, len(attempts))
	for _, as := range attempts {
		out = append(out, WorkSummary{
			AttemptID:  as.AttemptID,
			StageID:    as.StageID,
			Leg:        as.Leg,
			State:      as.State,
			DurationMs: attemptDurationMs(as),
		})
	}
	return out
}

func attemptDurationMs(as AttemptState) int64 {
	if !as.TerminalAt.IsZero() && !as.StartedAt.IsZero() {
		return as.TerminalAt.Sub(as.StartedAt).Milliseconds()
	}
	return 0
}

func buildArtifactSummaries(l *Ledger, taskID string, integration *IntegrationCandidate) []ArtifactSummary {
	if integration != nil {
		return buildArtifactSummariesFromIntegration(integration)
	}
	// M3.5 remaining: richer artifact summaries for solo turns. A solo turn
	// has no IntegrationCandidate (no parallel workers), but it may still
	// have produced file changes. Build summaries from the ledger's
	// attempt states and charges — the data M3.1–M3.3 already persist.
	return buildArtifactSummariesFromLedger(l, taskID)
}

func buildArtifactSummariesFromIntegration(integration *IntegrationCandidate) []ArtifactSummary {
	out := make([]ArtifactSummary, 0, len(integration.Manifests))
	for _, m := range integration.Manifests {
		s := ArtifactSummary{
			AttemptID:    m.AttemptID,
			Leg:          Leg(m.Leg),
			ChangedFiles: m.ChangedFiles,
			DiffDigest:   m.DiffDigest,
			DiffPath:     m.DiffPath,
		}
		if m.Check != nil {
			s.CheckPassed = m.Check.Passed
			s.CheckCommand = strings.Join(m.Check.Command, " ")
		}
		out = append(out, s)
	}
	return out
}

// buildArtifactSummariesFromLedger builds artifact summaries for solo turns
// that produced file changes without parallel workers. It reads the
// AttemptState's ChangedFiles, DiffDigest and DiffPath (M3.5 solo artifact
// capture) — the same data a parallel workflow's PatchManifest carries.
func buildArtifactSummariesFromLedger(l *Ledger, taskID string) []ArtifactSummary {
	attempts := l.AttemptStatesFor(taskID)
	out := make([]ArtifactSummary, 0, len(attempts))
	for _, as := range attempts {
		// Skip attempts with no artifact references: they either produced
		// text only or failed before writing.
		if as.DiffPath == "" && as.WorktreeDir == "" && len(as.ChangedFiles) == 0 {
			continue
		}
		s := ArtifactSummary{
			AttemptID:    as.AttemptID,
			Leg:          as.Leg,
			ChangedFiles: as.ChangedFiles,
			DiffDigest:   as.DiffDigest,
			DiffPath:     as.DiffPath,
		}
		out = append(out, s)
	}
	return out
}

func buildFailedChecks(l *Ledger, taskID string, integration *IntegrationCandidate) []FailedCheck {
	if integration != nil {
		var out []FailedCheck
		for _, m := range integration.Manifests {
			if m.Check == nil || m.Check.Passed {
				continue
			}
			out = append(out, FailedCheck{
				AttemptID: m.AttemptID,
				Leg:       Leg(m.Leg),
				Command:   strings.Join(m.Check.Command, " "),
				ExitCode:  m.Check.ExitCode,
				Output:    m.Check.Output,
			})
		}
		return out
	}
	// M3.5: for solo turns, read failed checks from the OutcomeEvidence the
	// brain's M5.1 wiring already records (director assessment, gate checks).
	if o := l.OutcomeFor(taskID); o != nil {
		var out []FailedCheck
		for _, cr := range o.Checks {
			if cr.Passed {
				continue
			}
			out = append(out, FailedCheck{
				Command:  cr.Command,
				ExitCode: cr.ExitCode,
			})
		}
		return out
	}
	return nil
}

func buildRemainingActions(l *Ledger, taskID string) []string {
	var actions []string
	for _, as := range l.AttemptStatesFor(taskID) {
		switch as.State {
		case StateInterrupted:
			actions = append(actions, fmt.Sprintf("attempt %s (leg %s) was interrupted — resume decision needed", as.AttemptID, as.Leg))
		case StateWaitingForInput:
			actions = append(actions, fmt.Sprintf("attempt %s (leg %s) is waiting for input", as.AttemptID, as.Leg))
		case StateCancelRequested:
			actions = append(actions, fmt.Sprintf("attempt %s (leg %s) has cancellation pending", as.AttemptID, as.Leg))
		}
	}
	ts := l.TaskStateFor(taskID)
	if ts != nil && !ts.State.IsTerminal() {
		actions = append(actions, fmt.Sprintf("task is %s — not terminal", ts.State))
	}
	return actions
}

func buildUncertainSideEffects(l *Ledger, taskID string) []string {
	var uncertain []string
	for _, c := range l.Charges {
		if c.TaskID != taskID || c.Kind != KindCall {
			continue
		}
		if c.Usage.Status == UsageUnknown {
			uncertain = append(uncertain, fmt.Sprintf("call %s (leg %s, label %s) has unknown usage", c.ID, c.Leg, c.Label))
		}
	}
	for _, as := range l.AttemptStatesFor(taskID) {
		if as.State == StateInterrupted && as.WorktreeDir != "" {
			uncertain = append(uncertain, fmt.Sprintf("worktree %s from interrupted attempt %s may have uncommitted changes", as.WorktreeDir, as.AttemptID))
		}
	}
	return uncertain
}

func buildBudgetSummary(l *Ledger, taskID string) *BudgetSummary {
	b := l.BudgetFor(taskID)
	if b == nil {
		return nil
	}
	totals := l.TaskTotals(taskID)
	return &BudgetSummary{
		AttemptsUsed: b.SettledAttempts,
		AttemptsMax:  b.MaxAttempts,
		CostUSD:      b.SettledCostUSD,
		CostComplete: totals.Complete(),
		UnknownUsage: totals.Unknown,
	}
}

// ---- ledger integration ----

// RecordHandoff stores a handoff brief in the ledger. One per task, updated in
// place when rebuilt.
func (l *Ledger) RecordHandoff(brief HandoffBrief) {
	if brief.TaskID == "" {
		return
	}
	brief.Version = HandoffVersion
	if brief.CreatedAt.IsZero() {
		brief.CreatedAt = time.Now()
	}
	for i, old := range l.Handoffs {
		if old.TaskID == brief.TaskID {
			l.Handoffs[i] = brief
			return
		}
	}
	l.Handoffs = append(l.Handoffs, brief)
	if len(l.Handoffs) > maxHandoffs {
		l.Handoffs = l.Handoffs[len(l.Handoffs)-maxHandoffs:]
	}
}

// HandoffFor returns the brief for a task, or nil when none exists.
func (l *Ledger) HandoffFor(taskID string) *HandoffBrief {
	for i, h := range l.Handoffs {
		if h.TaskID == taskID {
			return &l.Handoffs[i]
		}
	}
	return nil
}

// HandoffCoverage returns how many briefs exist and how many tasks they
// cover, for `captain stats`.
func (l *Ledger) HandoffCoverage() (briefs int, byState map[LifecycleState]int) {
	byState = map[LifecycleState]int{}
	briefs = len(l.Handoffs)
	for _, h := range l.Handoffs {
		byState[h.State]++
	}
	return
}

// ---- formatting ----

// FormatHandoffBrief renders a brief for the CLI. It is compact by design:
// each section is a heading followed by a list, so a developer can scan it
// without reading prose.
func FormatHandoffBrief(b HandoffBrief) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "task %s — %s\n", b.TaskID, b.State)
	if b.StopReason != "" {
		fmt.Fprintf(&sb, "  stop: %s\n", b.StopReason)
	}
	if b.Requirements != "" {
		peek := b.Requirements
		if len(peek) > 200 {
			peek = peek[:200] + "…"
		}
		fmt.Fprintf(&sb, "  requirements: %s\n", peek)
	}
	if len(b.Work) > 0 {
		sb.WriteString("\nwork:\n")
		for _, w := range b.Work {
			stage := ""
			if w.StageID != "" {
				stage = fmt.Sprintf(" [%s] ", w.StageID)
			} else {
				stage = " "
			}
			dur := ""
			if w.DurationMs > 0 {
				dur = fmt.Sprintf(" %dms", w.DurationMs)
			}
			fmt.Fprintf(&sb, "  · %s%s— %s%s\n", w.Leg, stage, w.State, dur)
		}
	}
	if len(b.Artifacts) > 0 {
		sb.WriteString("\nartifacts:\n")
		for _, a := range b.Artifacts {
			check := "no check"
			if a.CheckPassed {
				check = "check passed"
			} else if a.CheckCommand != "" {
				check = "check failed"
			}
			files := "no files"
			if len(a.ChangedFiles) > 0 {
				files = fmt.Sprintf("%d files", len(a.ChangedFiles))
			}
			fmt.Fprintf(&sb, "  · %s — %s, %s", a.Leg, files, check)
			if a.DiffDigest != "" {
				d := a.DiffDigest
				if len(d) > 12 {
					d = d[:12]
				}
				fmt.Fprintf(&sb, ", diff %s", d)
			}
			sb.WriteString("\n")
		}
	}
	if b.Integration != nil {
		sb.WriteString("\nintegration:\n")
		fmt.Fprintf(&sb, "  status: %s\n", b.Integration.Status)
		if len(b.Integration.ChangedFiles) > 0 {
			fmt.Fprintf(&sb, "  changed: %s\n", strings.Join(b.Integration.ChangedFiles, ", "))
		}
		if len(b.Integration.Conflicts) > 0 {
			fmt.Fprintf(&sb, "  conflicts: %s\n", strings.Join(b.Integration.Conflicts, ", "))
		}
	}
	if len(b.FailedChecks) > 0 {
		sb.WriteString("\nfailed checks:\n")
		for _, fc := range b.FailedChecks {
			cmd := fc.Command
			if cmd == "" {
				cmd = "(no command)"
			}
			fmt.Fprintf(&sb, "  · %s: %s (exit %d)\n", fc.Leg, cmd, fc.ExitCode)
			if fc.Output != "" {
				fmt.Fprintf(&sb, "    %s\n", fc.Output)
			}
		}
	}
	if len(b.RemainingActions) > 0 {
		sb.WriteString("\nremaining:\n")
		for _, a := range b.RemainingActions {
			fmt.Fprintf(&sb, "  · %s\n", a)
		}
	}
	if len(b.UncertainSideEffects) > 0 {
		sb.WriteString("\nuncertain:\n")
		for _, u := range b.UncertainSideEffects {
			fmt.Fprintf(&sb, "  · %s\n", u)
		}
	}
	if b.Budget != nil {
		sb.WriteString("\nbudget:\n")
		fmt.Fprintf(&sb, "  attempts: %d", b.Budget.AttemptsUsed)
		if b.Budget.AttemptsMax > 0 {
			fmt.Fprintf(&sb, "/%d", b.Budget.AttemptsMax)
		}
		sb.WriteString("\n")
		if b.Budget.CostUSD > 0 {
			label := "estimated"
			if b.Budget.CostComplete {
				label = "billed"
			}
			fmt.Fprintf(&sb, "  cost: $%.4f (%s)\n", b.Budget.CostUSD, label)
		}
		if b.Budget.UnknownUsage > 0 {
			fmt.Fprintf(&sb, "  unknown usage: %d call(s)\n", b.Budget.UnknownUsage)
		}
	}
	return sb.String()
}
