package captaincode

// Blinded review (ROADMAP M1.3). The harness can decide most acceptances with
// a command and an exit code, but some tasks - "is this refactor actually
// better?", "did the review find the real defect?" - have no such command.
// Those declare `review: blinded`, end `pending-review`, and stay out of the
// numerator until a person says otherwise. This file is the path back: how
// that person is shown the work, how their verdict is recorded, and what the
// record then means.
//
// Three rules it exists to enforce:
//
//   - the reviewer never learns which arm produced the change. Executions are
//     addressed by alias, and looking one up by arm name is not offered.
//   - review decides sufficiency, never necessity. A run the checks already
//     rejected cannot be reviewed into acceptance; only a `pending-review`
//     execution has a verdict to give.
//   - an acceptance that rested on human judgement is countable as such. The
//     verdict is stored beside the execution with its reviewer and time, and
//     the report says how many of an arm's acceptances came this way.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Review verdicts. Deliberately the two the roadmap's rubric admits: a
// reviewer who is unsure has not finished reviewing, so there is no third
// value to park the execution in.
const (
	ReviewAccept = "accept"
	ReviewReject = "reject"
)

// EvalReview is one recorded human verdict.
type EvalReview struct {
	Verdict  string    `json:"verdict"`
	Reviewer string    `json:"reviewer"`
	Note     string    `json:"note,omitempty"`
	At       time.Time `json:"at"`
	// Amended records that this verdict replaced an earlier one. A rubric
	// applied twice to the same execution is a fact about the review, not
	// something to overwrite silently.
	Amended bool `json:"amended,omitempty"`
}

// PendingItem is what a reviewer is handed: enough to judge the change, and
// nothing that identifies the worker behind it.
type PendingItem struct {
	TaskID string `json:"task_id"`
	Family string `json:"family"`
	Alias  string `json:"alias"`
	Repeat int    `json:"repeat"`
	Prompt string `json:"prompt,omitempty"`
	// Dir is the snapshot the arm worked in, so the reviewer can read the
	// diff rather than trust a summary of it.
	Dir     string   `json:"dir,omitempty"`
	Changed []string `json:"changed,omitempty"`
}

// Key is the reviewer's address for one execution.
func (p PendingItem) Key() string { return fmt.Sprintf("%s/%s/%d", p.TaskID, p.Alias, p.Repeat) }

// Pending lists every execution still owing a verdict, in a stable order.
func (r *EvalResult) Pending() []PendingItem {
	var out []PendingItem
	for _, e := range r.Executions {
		if e.Status != EvalPendingReview {
			continue
		}
		out = append(out, PendingItem{
			TaskID: e.TaskID, Family: e.Family, Alias: e.ArmAlias, Repeat: e.Repeat, Prompt: e.Prompt,
			Dir: e.Dir, Changed: e.Changed,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// RecordReview applies one verdict, addressed by task/alias/repeat.
//
// The execution must currently be pending: a rejection the checks already
// decided is not a reviewer's to overturn, and an execution already reviewed
// needs `amend` to be revisited so a second pass cannot quietly replace a
// first. On acceptance the status becomes `accepted` and counts in the
// numerator from then on; on rejection it becomes `rejected` and stays in the
// denominator, which is where it already was.
func (r *EvalResult) RecordReview(taskID, alias string, repeat int, verdict, reviewer, note string, amend bool) error {
	if verdict != ReviewAccept && verdict != ReviewReject {
		return fmt.Errorf("verdict must be %s or %s, got %q", ReviewAccept, ReviewReject, verdict)
	}
	if strings.TrimSpace(reviewer) == "" {
		return fmt.Errorf("a verdict needs a reviewer; an anonymous acceptance cannot be audited")
	}
	matches := 0
	for _, e := range r.Executions {
		if e.TaskID == taskID && e.ArmAlias == alias && e.Repeat == repeat {
			matches++
		}
	}
	if matches > 1 {
		return fmt.Errorf("ambiguous review key %s/%s/%d: %d executions", taskID, alias, repeat, matches)
	}
	for i := range r.Executions {
		e := &r.Executions[i]
		if e.TaskID != taskID || e.ArmAlias != alias || e.Repeat != repeat {
			continue
		}
		if e.Review != nil && !amend {
			return fmt.Errorf("%s/%s/%d was already reviewed (%s by %s); pass amend to replace that verdict",
				taskID, alias, repeat, e.Review.Verdict, e.Review.Reviewer)
		}
		if e.Review == nil && e.Status != EvalPendingReview {
			return fmt.Errorf("%s/%s/%d is %s, not %s: review decides where checks are insufficient, it does not overturn them",
				taskID, alias, repeat, e.Status, EvalPendingReview)
		}
		rev := &EvalReview{Verdict: verdict, Reviewer: reviewer, Note: note, At: time.Now(), Amended: e.Review != nil}
		if e.Review != nil {
			e.ReviewHistory = append(e.ReviewHistory, *e.Review)
		}
		e.Review = rev
		if verdict == ReviewAccept {
			e.Status = EvalAccepted
		} else {
			e.Status = EvalRejected
		}
		e.Reason = fmt.Sprintf("blinded review by %s", reviewer)
		if note != "" {
			e.Reason += ": " + note
		}
		return nil
	}
	return fmt.Errorf("no execution %s/%s/%d in this result", taskID, alias, repeat)
}

// ReviewedAccept reports whether this execution's acceptance came from a
// person rather than from the checks alone.
func (e EvalExecution) ReviewedAccept() bool {
	return e.Review != nil && e.Review.Verdict == ReviewAccept
}
