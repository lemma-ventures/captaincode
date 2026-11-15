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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

func ExportReviewPacket(ctx context.Context, r *EvalResult, root string) (err error) {
	if err := os.Mkdir(root, 0o700); err != nil {
		return fmt.Errorf("create review export: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(root)
		}
	}()
	packet := filepath.Join(root, "packet")
	if err := os.Mkdir(packet, 0o700); err != nil {
		return err
	}
	redactor := reviewRedactor(r)
	pending := r.Pending()
	seen := map[string]bool{}
	type keyRow struct {
		TaskID string `json:"task_id"`
		Alias  string `json:"alias"`
		Repeat int    `json:"repeat"`
		Arm    string `json:"arm"`
	}
	keys := make([]keyRow, 0, len(pending))
	for _, item := range pending {
		if !evalPathName(item.TaskID) || !evalPathName(item.Alias) || item.Repeat < 1 {
			return fmt.Errorf("unsafe review key %s", item.Key())
		}
		if seen[item.Key()] {
			return fmt.Errorf("ambiguous review key %s", item.Key())
		}
		seen[item.Key()] = true
		var execution *EvalExecution
		for i := range r.Executions {
			e := &r.Executions[i]
			if e.TaskID == item.TaskID && e.ArmAlias == item.Alias && e.Repeat == item.Repeat {
				execution = e
				break
			}
		}
		if execution == nil {
			return fmt.Errorf("missing execution %s", item.Key())
		}
		diff, err := reviewPacketDiff(ctx, *execution)
		if err != nil {
			return fmt.Errorf("%s: %w", item.Key(), err)
		}
		var body strings.Builder
		fmt.Fprintf(&body, "task: %s\nfamily: %s\nalias: %s\nrepeat: %d\n\nchecks\n", item.TaskID, item.Family, item.Alias, item.Repeat)
		for _, check := range execution.Checks {
			fmt.Fprintf(&body, "\n[%s] exit=%d want=%d passed=%t\n%s\n", check.Name, check.Exit, check.Want, check.Passed, check.Output)
		}
		fmt.Fprintf(&body, "\ndiff\n%s", diff)
		bodyText := redactor.ReplaceAllString(body.String(), "[redacted]")
		bodyText = strings.ReplaceAll(bodyText, execution.Dir, "[snapshot]")
		name := fmt.Sprintf("%s.%s.%d.txt", item.TaskID, item.Alias, item.Repeat)
		if err := os.WriteFile(filepath.Join(packet, name), []byte(bodyText), 0o600); err != nil {
			return err
		}
		keys = append(keys, keyRow{TaskID: item.TaskID, Alias: item.Alias, Repeat: item.Repeat, Arm: execution.Arm})
	}
	keyData, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "alias-key.json"), keyData, 0o600); err != nil {
		return err
	}
	complete = true
	return nil
}

func reviewPacketDiff(ctx context.Context, e EvalExecution) (string, error) {
	if !pinnedRevision(e.Revision) {
		return "", fmt.Errorf("execution has no pinned source revision")
	}
	if e.Dir == "" {
		return "", fmt.Errorf("execution has no snapshot")
	}
	out, code, err := runIn(ctx, e.Dir, []string{"git", "diff", "--binary", "--full-index", "--no-renames", e.Revision, "--"}, nil, time.Minute)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git diff exited %d: %s", code, tail(out))
	}
	var diff strings.Builder
	diff.WriteString(out)
	list, code, err := runIn(ctx, e.Dir, []string{"git", "ls-files", "--others", "--exclude-standard", "-z"}, nil, time.Minute)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git ls-files exited %d: %s", code, tail(list))
	}
	for _, name := range strings.Split(list, "\x00") {
		if name == "" {
			continue
		}
		out, code, err = runIn(ctx, e.Dir, []string{"git", "diff", "--no-index", "--binary", "--full-index", "/dev/null", filepath.Join(e.Dir, name)}, nil, time.Minute)
		if err != nil {
			return "", err
		}
		if code != 0 && code != 1 {
			return "", fmt.Errorf("untracked diff for %q exited %d: %s", name, code, tail(out))
		}
		diff.WriteString(out)
	}
	return diff.String(), nil
}

func reviewRedactor(r *EvalResult) *regexp.Regexp {
	seen := map[string]bool{}
	for _, e := range r.Executions {
		seen[e.Arm] = true
		for _, charge := range e.Charges {
			seen[string(charge.Leg)] = true
			seen[charge.Model] = true
		}
	}
	for _, spec := range Registry() {
		seen[string(spec.ID)] = true
		seen[spec.Model] = true
	}
	for _, pin := range LegModelPins() {
		seen[string(pin.Leg)] = true
		seen[pin.Model] = true
	}
	delete(seen, "")
	words := make([]string, 0, len(seen))
	for word := range seen {
		words = append(words, regexp.QuoteMeta(word))
	}
	sort.Slice(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
	if len(words) == 0 {
		return regexp.MustCompile(`a^`)
	}
	return regexp.MustCompile("(?i)" + strings.Join(words, "|"))
}

// ReviewedAccept reports whether this execution's acceptance came from a
// person rather than from the checks alone.
func (e EvalExecution) ReviewedAccept() bool {
	return e.Review != nil && e.Review.Verdict == ReviewAccept
}
