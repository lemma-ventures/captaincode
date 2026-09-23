package captaincode

// Task-wide acceptance evidence (ROADMAP M5.1). The evaluation harness
// (M1.3) records review verdicts and check results for fixed benchmark
// fixtures; this file extends that to actual task runs — the work Captain
// does for the user rather than the work it does to measure itself.
//
// An OutcomeEvidence is one task's acceptance record: the checks that ran,
// the human verdict (if any), the patch that was accepted or rejected, the
// minutes spent correcting, and any later regression that revoked an
// earlier acceptance. It is built from the data the ledger already
// persists (charges, lifecycle states, integration candidates) plus two
// new inputs: a human verdict on the task itself (not the eval harness),
// and a correction-time measurement.
//
// The record is deliberately separate from the handoff brief (M3.5): the
// brief says what happened; the outcome says whether it was accepted.
// A check that passed is a fact; whether it is sufficient is the
// reviewer's call. A regression is a later observation that revokes an
// earlier acceptance — it does not erase the original record.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// OutcomeVersion is stamped on every outcome record.
const OutcomeVersion = 1

// AcceptanceStatus is the task-level acceptance verdict.
type AcceptanceStatus string

const (
	AcceptanceAccepted  AcceptanceStatus = "accepted"
	AcceptanceRejected  AcceptanceStatus = "rejected"
	AcceptancePending   AcceptanceStatus = "pending"
	AcceptanceRegressed AcceptanceStatus = "regressed"
)

// CheckResult is one task-linked check: a command, its exit code, and
// whether it passed. Derived from the gate evidence the workflow executor
// already captures, or recorded directly for solo-turn checks.
type CheckResult struct {
	Command  string    `json:"command,omitempty"`
	ExitCode int       `json:"exit_code"`
	Passed   bool      `json:"passed"`
	Source   string    `json:"source,omitempty"` // "gate" | "solo" | "reviewer" | "external"
	At       time.Time `json:"at"`
}

// TaskReview is one human verdict on an actual task (not an eval execution).
// Unlike EvalReview, this is addressed by task ID, not by alias/repeat.
type TaskReview struct {
	Verdict  string    `json:"verdict"` // "accept" | "reject"
	Reviewer string    `json:"reviewer"`
	Note     string    `json:"note,omitempty"`
	At       time.Time `json:"at"`
	Amended  bool      `json:"amended,omitempty"`
}

// CorrectionRecord tracks time spent correcting a task's output after it
// was initially delivered. The minutes are wall time, not compute time;
// they measure the developer's effort, which is the metric M1's rubric
// names as "developer effort."
type CorrectionRecord struct {
	Minutes int       `json:"minutes"`
	Reason  string    `json:"reason,omitempty"`
	At      time.Time `json:"at"`
}

// RegressionRecord marks a later observation that a previously accepted
// task has regressed. The original acceptance is not erased; the
// regression is a new record that supersedes the acceptance status.
type RegressionRecord struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
	Source string    `json:"source,omitempty"` // who observed it
}

// OutcomeEvidence is one task's acceptance evidence record. It is stored
// in the ledger (one per task, updated in place) and assembled from the
// data the ledger already persists plus any human verdict or correction
// time that was recorded.
type OutcomeEvidence struct {
	Version       int                `json:"version"`
	TaskID        string             `json:"task_id"`
	Task          string             `json:"task,omitempty"`
	Leg           Leg                `json:"leg,omitempty"`
	Status        AcceptanceStatus   `json:"status"`
	Checks        []CheckResult      `json:"checks,omitempty"`
	Review        *TaskReview        `json:"review,omitempty"`
	ReviewHistory []TaskReview       `json:"review_history,omitempty"`
	Corrections   []CorrectionRecord `json:"corrections,omitempty"`
	Regression    *RegressionRecord  `json:"regression,omitempty"`
	AcceptedAt    time.Time          `json:"accepted_at,omitempty"`
	RejectedAt    time.Time          `json:"rejected_at,omitempty"`
	UpdatedAt     time.Time          `json:"updated_at"`

	// DecidedBy names WHAT settled the status, because a status nobody can
	// attribute is worse than a pending one: a reviewer's verdict and a
	// derived one are different evidence and must not be read as the same
	// acceptance. Empty while the outcome is pending. See settle.go.
	DecidedBy OutcomeDecider `json:"decided_by,omitempty"`
	SettledAt time.Time      `json:"settled_at,omitempty"`

	// Objective signals captain observes itself (stage 1): what was
	// delivered and when, the files the worker changed in Dir, a commit that
	// kept them, a corrective re-prompt, and how many attempts the task took
	// with the escalation record when there was one. Effort and Model say
	// what the delivering attempt ran as, so a label can be attributed to a
	// version.
	Dir            string             `json:"dir,omitempty"`
	DeliveredAt    time.Time          `json:"delivered_at,omitempty"`
	DeliveredChars int                `json:"delivered_chars,omitempty"`
	ChangedFiles   []string           `json:"changed_files,omitempty"`
	Commit         *CommitRecord      `json:"commit,omitempty"`
	Reprompt       *RepromptRecord    `json:"reprompt,omitempty"`
	Attempts       int                `json:"attempts,omitempty"`
	Escalation     *EscalationOutcome `json:"escalation,omitempty"`
	Effort         Effort             `json:"effort,omitempty"`
	Model          string             `json:"model,omitempty"`
}

// deliveredAt is when the settle window starts: the delivery when it was
// noted, the record's last update otherwise.
func (o OutcomeEvidence) deliveredAt() time.Time {
	if !o.DeliveredAt.IsZero() {
		return o.DeliveredAt
	}
	return o.UpdatedAt
}

// Delivered says the task produced something the user could keep: text, or
// changed files.
func (o OutcomeEvidence) Delivered() bool {
	return o.DeliveredChars > 0 || len(o.ChangedFiles) > 0
}

// NoteDelivery records what a task delivered: where, which files changed,
// how much text, and what the delivering attempt ran as. Called once per
// task when the answer goes out; the settle window counts from here.
func (l *Ledger) NoteDelivery(taskID, dir string, files []string, chars int, leg Leg, effort Effort, model string, attempts int) {
	if taskID == "" {
		return
	}
	o := l.OutcomeFor(taskID)
	if o == nil {
		l.RecordOutcome(OutcomeEvidence{TaskID: taskID, Status: AcceptancePending})
		o = l.OutcomeFor(taskID)
	}
	now := time.Now()
	o.Dir, o.ChangedFiles, o.DeliveredChars, o.DeliveredAt = dir, files, chars, now
	if leg != "" {
		o.Leg = leg
	}
	o.Effort, o.Model = effort, model
	if attempts > o.Attempts {
		o.Attempts = attempts
	}
	o.UpdatedAt = now
}

// RecordCommitEvidence marks a follow-up commit that touched the worker's
// files. Settles the outcome as accepted at once (settle.go): the user kept
// the work, which is the acceptance every other signal approximates.
func (l *Ledger) RecordCommitEvidence(taskID string, c CommitRecord) {
	if taskID == "" || c.SHA == "" {
		return
	}
	o := l.OutcomeFor(taskID)
	if o == nil {
		l.RecordOutcome(OutcomeEvidence{TaskID: taskID, Status: AcceptancePending})
		o = l.OutcomeFor(taskID)
	}
	if c.At.IsZero() {
		c.At = time.Now()
	}
	o.Commit = &c
	o.UpdatedAt = time.Now()
}

// RecordReprompt marks a corrective prompt sent inside the settle window.
// Settles the outcome as rejected (settle.go).
func (l *Ledger) RecordReprompt(taskID, text string, at time.Time) {
	if taskID == "" {
		return
	}
	o := l.OutcomeFor(taskID)
	if o == nil {
		l.RecordOutcome(OutcomeEvidence{TaskID: taskID, Status: AcceptancePending})
		o = l.OutcomeFor(taskID)
	}
	if at.IsZero() {
		at = time.Now()
	}
	if len(text) > 160 {
		text = text[:160]
	}
	o.Reprompt = &RepromptRecord{Text: text, At: at}
	o.UpdatedAt = time.Now()
}

// RecordEscalationOutcome attaches the repair/escalation record to a task.
func (l *Ledger) RecordEscalationOutcome(taskID string, e EscalationOutcome) {
	if taskID == "" {
		return
	}
	o := l.OutcomeFor(taskID)
	if o == nil {
		l.RecordOutcome(OutcomeEvidence{TaskID: taskID, Status: AcceptancePending})
		o = l.OutcomeFor(taskID)
	}
	o.Escalation = &e
	if e.Attempts > o.Attempts {
		o.Attempts = e.Attempts
	}
	o.UpdatedAt = time.Now()
}

// correctiveOpeners are the ways a user says "that was wrong" in the first
// words of the next prompt. Narrow on purpose: the next task in a session
// is usually a new task, and reading every follow-up as a rejection would
// manufacture failures out of ordinary work.
var correctiveOpeners = regexp.MustCompile(`(?i)^\s*(?:no[,.!\s]|nope\b|wrong\b|that'?s (?:not|wrong)|not what i|still (?:broken|fails|failing|wrong|doesn'?t)|(?:doesn'?t|does not|didn'?t|did not) work|revert\b|undo\b|fix (?:it|that|this)\b|try again\b|redo\b|you (?:broke|missed|forgot|ignored)|that broke\b|it'?s broken\b|tests? (?:still )?fail)`)

// CorrectiveReprompt reports whether a prompt reads as a correction of the
// previous answer rather than a new task.
func CorrectiveReprompt(text string) bool {
	return correctiveOpeners.MatchString(text)
}

// OutcomeDecider is what moved an outcome off pending.
type OutcomeDecider string

const (
	// DecidedByReviewer: a human verdict on the task. Authoritative - nothing
	// derived ever overwrites it.
	DecidedByReviewer OutcomeDecider = "reviewer"
	// DecidedByChecks: the task-linked checks settled it - a failed check
	// rejects immediately, an all-passed set accepts once the settle window
	// has passed without the user coming back to it.
	DecidedByChecks OutcomeDecider = "checks"
	// DecidedByLifecycle: the task never reached delivery (failed, exhausted).
	DecidedByLifecycle OutcomeDecider = "lifecycle"
	// DecidedByRegression: a later observation revoked an earlier acceptance.
	DecidedByRegression OutcomeDecider = "regression"
	// DecidedByCommit: a commit after the run touched the files the worker
	// changed - the user kept the work. Strong, objective, and immediate.
	DecidedByCommit OutcomeDecider = "commit"
	// DecidedByReprompt: the user came straight back with a corrective
	// prompt ("no, ...", "still broken", "revert") inside the settle window.
	DecidedByReprompt OutcomeDecider = "reprompt"
	// DecidedBySilence: something was delivered (an answer, a diff) and the
	// user neither corrected it, re-prompted, interrupted, nor regressed it
	// for the settle window. The weakest honest acceptance, labelled as such
	// so calibration can weigh it under the others.
	DecidedBySilence OutcomeDecider = "silence"
)

// CommitRecord is a follow-up commit that touched the worker's files.
type CommitRecord struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject,omitempty"`
	At      time.Time `json:"at"`
	Files   []string  `json:"files,omitempty"`
}

// RepromptRecord is a corrective prompt sent on the same workspace inside
// the settle window.
type RepromptRecord struct {
	Text string    `json:"text"` // the head of the prompt, redacted upstream
	At   time.Time `json:"at"`
}

// maxOutcomes caps the persisted outcome log.
const maxOutcomes = 200

// RecordOutcome stores or updates an outcome in the ledger. One per task,
// updated in place when rebuilt or amended.
func (l *Ledger) RecordOutcome(o OutcomeEvidence) {
	if o.TaskID == "" {
		return
	}
	o.Version = OutcomeVersion
	if o.UpdatedAt.IsZero() {
		o.UpdatedAt = time.Now()
	}
	for i, old := range l.Outcomes {
		if old.TaskID == o.TaskID {
			l.Outcomes[i] = o
			return
		}
	}
	l.Outcomes = append(l.Outcomes, o)
	if len(l.Outcomes) > maxOutcomes {
		l.Outcomes = l.Outcomes[len(l.Outcomes)-maxOutcomes:]
	}
}

// OutcomeFor returns the outcome evidence for a task, or nil.
func (l *Ledger) OutcomeFor(taskID string) *OutcomeEvidence {
	for i, o := range l.Outcomes {
		if o.TaskID == taskID {
			return &l.Outcomes[i]
		}
	}
	return nil
}

// RecordCheckResult adds a check result to a task's outcome, creating the
// outcome if it does not yet exist.
func (l *Ledger) RecordCheckResult(taskID string, check CheckResult) {
	if taskID == "" {
		return
	}
	if check.At.IsZero() {
		check.At = time.Now()
	}
	o := l.OutcomeFor(taskID)
	if o == nil {
		l.RecordOutcome(OutcomeEvidence{
			TaskID: taskID,
			Status: AcceptancePending,
			Checks: []CheckResult{check},
		})
		return
	}
	o.Checks = append(o.Checks, check)
	o.UpdatedAt = time.Now()
}

// RecordTaskReview applies a human verdict to a task's outcome. A second
// verdict on the same task needs amend=true and preserves the prior
// verdict in ReviewHistory, the same discipline EvalReview enforces.
func (l *Ledger) RecordTaskReview(taskID, verdict, reviewer, note string, amend bool) error {
	if verdict != ReviewAccept && verdict != ReviewReject {
		return fmt.Errorf("verdict must be %s or %s, got %q", ReviewAccept, ReviewReject, verdict)
	}
	if strings.TrimSpace(reviewer) == "" {
		return fmt.Errorf("a verdict needs a reviewer; an anonymous acceptance cannot be audited")
	}
	o := l.OutcomeFor(taskID)
	if o == nil {
		o = &OutcomeEvidence{TaskID: taskID, Status: AcceptancePending}
		l.RecordOutcome(*o)
		o = l.OutcomeFor(taskID)
	}
	if o.Review != nil && !amend {
		return fmt.Errorf("task %s was already reviewed (%s by %s); pass amend to replace",
			taskID, o.Review.Verdict, o.Review.Reviewer)
	}
	rev := TaskReview{Verdict: verdict, Reviewer: reviewer, Note: note, At: time.Now(), Amended: o.Review != nil}
	if o.Review != nil {
		o.ReviewHistory = append(o.ReviewHistory, *o.Review)
	}
	o.Review = &rev
	if verdict == ReviewAccept {
		o.Status = AcceptanceAccepted
		o.AcceptedAt = rev.At
	} else {
		o.Status = AcceptanceRejected
		o.RejectedAt = rev.At
	}
	o.DecidedBy, o.SettledAt = DecidedByReviewer, rev.At
	o.UpdatedAt = time.Now()
	l.journal(RoutingRecord{Kind: RoutingKindOutcome, TaskID: taskID, Outcome: o})
	return nil
}

// RecordCorrection adds a correction-time entry to a task's outcome.
func (l *Ledger) RecordCorrection(taskID string, minutes int, reason string) {
	if taskID == "" || minutes <= 0 {
		return
	}
	o := l.OutcomeFor(taskID)
	if o == nil {
		o = &OutcomeEvidence{TaskID: taskID, Status: AcceptancePending}
		l.RecordOutcome(*o)
		o = l.OutcomeFor(taskID)
	}
	o.Corrections = append(o.Corrections, CorrectionRecord{
		Minutes: minutes,
		Reason:  reason,
		At:      time.Now(),
	})
	o.UpdatedAt = time.Now()
}

// RecordRegression marks a previously accepted task as regressed. The
// original acceptance record stays; the regression supersedes the status.
func (l *Ledger) RecordRegression(taskID, reason, source string) {
	if taskID == "" {
		return
	}
	o := l.OutcomeFor(taskID)
	if o == nil {
		o = &OutcomeEvidence{TaskID: taskID, Status: AcceptancePending}
		l.RecordOutcome(*o)
		o = l.OutcomeFor(taskID)
	}
	o.Regression = &RegressionRecord{
		Reason: reason,
		At:     time.Now(),
		Source: source,
	}
	if o.Status == AcceptanceAccepted {
		o.Status = AcceptanceRegressed
		o.DecidedBy, o.SettledAt = DecidedByRegression, o.Regression.At
	}
	o.UpdatedAt = time.Now()
	l.journal(RoutingRecord{Kind: RoutingKindOutcome, TaskID: taskID, Outcome: o})
}

// TotalCorrectionMinutes sums all correction records for a task.
func (o OutcomeEvidence) TotalCorrectionMinutes() int {
	total := 0
	for _, c := range o.Corrections {
		total += c.Minutes
	}
	return total
}

// AllChecksPassed returns true when every check result passed. An outcome
// with no checks is not "all passed" — it is unchecked.
func (o OutcomeEvidence) AllChecksPassed() bool {
	if len(o.Checks) == 0 {
		return false
	}
	for _, c := range o.Checks {
		if !c.Passed {
			return false
		}
	}
	return true
}

// OutcomeCoverage returns aggregate counts for `captain stats`: how many
// outcomes exist, how many are accepted/rejected/pending/regressed, and
// total correction minutes across all tasks.
func (l *Ledger) OutcomeCoverage() (total int, byStatus map[AcceptanceStatus]int, correctionMinutes int) {
	byStatus = map[AcceptanceStatus]int{}
	total = len(l.Outcomes)
	for _, o := range l.Outcomes {
		byStatus[o.Status]++
		correctionMinutes += o.TotalCorrectionMinutes()
	}
	return
}

// AcceptedOutcomes returns outcomes with status accepted, sorted by
// acceptance time (most recent first).
func (l *Ledger) AcceptedOutcomes() []OutcomeEvidence {
	var out []OutcomeEvidence
	for _, o := range l.Outcomes {
		if o.Status == AcceptanceAccepted || o.Status == AcceptanceRegressed {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AcceptedAt.After(out[j].AcceptedAt) })
	return out
}

// FormatOutcomeEvidence renders one outcome for the CLI.
func FormatOutcomeEvidence(o OutcomeEvidence) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "task %s — %s", o.TaskID, o.Status)
	if o.DecidedBy != "" {
		fmt.Fprintf(&sb, " (decided by %s)", o.DecidedBy)
	}
	sb.WriteString("\n")
	if o.Task != "" {
		peek := o.Task
		if len(peek) > 120 {
			peek = peek[:120] + "…"
		}
		fmt.Fprintf(&sb, "  task: %s\n", peek)
	}
	if o.Leg != "" {
		fmt.Fprintf(&sb, "  leg: %s\n", o.Leg)
	}
	if o.AllChecksPassed() {
		fmt.Fprintf(&sb, "  checks: all %d passed\n", len(o.Checks))
	} else if len(o.Checks) > 0 {
		passed := 0
		for _, c := range o.Checks {
			if c.Passed {
				passed++
			}
		}
		fmt.Fprintf(&sb, "  checks: %d/%d passed\n", passed, len(o.Checks))
		for _, c := range o.Checks {
			if !c.Passed {
				cmd := c.Command
				if cmd == "" {
					cmd = "(no command)"
				}
				fmt.Fprintf(&sb, "    ✗ %s (exit %d)\n", cmd, c.ExitCode)
			}
		}
	}
	if o.Review != nil {
		fmt.Fprintf(&sb, "  review: %s by %s", o.Review.Verdict, o.Review.Reviewer)
		if o.Review.Amended {
			sb.WriteString(" (amended)")
		}
		sb.WriteString("\n")
		if o.Review.Note != "" {
			fmt.Fprintf(&sb, "    %s\n", o.Review.Note)
		}
	}
	if len(o.ReviewHistory) > 0 {
		fmt.Fprintf(&sb, "  review history: %d prior verdict(s)\n", len(o.ReviewHistory))
	}
	if cm := o.TotalCorrectionMinutes(); cm > 0 {
		fmt.Fprintf(&sb, "  correction: %d minutes (%d entry/entries)\n", cm, len(o.Corrections))
	}
	if o.Regression != nil {
		fmt.Fprintf(&sb, "  regression: %s\n", o.Regression.Reason)
		if o.Regression.Source != "" {
			fmt.Fprintf(&sb, "    source: %s\n", o.Regression.Source)
		}
	}
	if !o.AcceptedAt.IsZero() {
		fmt.Fprintf(&sb, "  accepted: %s\n", o.AcceptedAt.Format("2006-01-02 15:04"))
	}
	if !o.RejectedAt.IsZero() {
		fmt.Fprintf(&sb, "  rejected: %s\n", o.RejectedAt.Format("2006-01-02 15:04"))
	}
	return sb.String()
}

// FormatOutcomeSummary renders a one-line table for `captain outcomes`.
func FormatOutcomeSummary(outcomes []OutcomeEvidence) string {
	if len(outcomes) == 0 {
		return "no outcome evidence recorded\n"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-20s %-12s %-11s %-8s %-8s %-8s\n", "TASK", "STATUS", "DECIDED BY", "CHECKS", "REVIEW", "CORR")
	for _, o := range outcomes {
		checks := "-"
		if len(o.Checks) > 0 {
			passed := 0
			for _, c := range o.Checks {
				if c.Passed {
					passed++
				}
			}
			checks = fmt.Sprintf("%d/%d", passed, len(o.Checks))
		}
		review := "-"
		if o.Review != nil {
			review = o.Review.Verdict
		}
		corr := "-"
		if cm := o.TotalCorrectionMinutes(); cm > 0 {
			corr = fmt.Sprintf("%dm", cm)
		}
		task := o.TaskID
		if len(task) > 18 {
			task = task[:15] + "…"
		}
		by := "-"
		if o.DecidedBy != "" {
			by = string(o.DecidedBy)
		}
		fmt.Fprintf(&sb, "%-20s %-12s %-11s %-8s %-8s %-8s\n", task, o.Status, by, checks, review, corr)
	}
	return sb.String()
}
