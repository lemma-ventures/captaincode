package captaincode

// The routing dataset, step 1 of P7 (ROADMAP build queue): every routing
// decision joined to what came of it, ready for an offline router evaluation.
//
// `captain why --export <file>` writes one JSON row per attempt. The join is
// keyed on the task and the attempt: the decision (class, domain, path,
// chosen leg, tie policy), the run (leg, outcome, verdict, quality, cost),
// and the task's settlement (gate result, repairs, label, decided-by).
//
// The prompt never enters the file. Its SHA-256 and the task class stand in
// for it, so rows from the same prompt still join. A plain hash is
// deliberate - a salted one would break the join - and it has the usual
// property that a reader who can guess a short prompt can confirm it. The
// file is made 0600 and refuses an existing path for that reason. No free
// text field of any record is exported: no task label, no rationale, no
// error, no reason, no file path. A dataset row is identities, enums,
// numbers and timestamps.
//
// One row per legged run event. A fan-out's team aggregate event carries no
// leg and is skipped: its cost would count the same work twice. A task with
// a decision and no run yet still exports one row (attempt 1, empty run
// fields), because an unrun decision is a router choice worth scoring.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The task's completion gate, as the outcome's checks record it: a stage's
// `gate:` command result, or "none" when the task had no gate at all.
const (
	DatasetGatePass = "pass"
	DatasetGateFail = "fail"
	DatasetGateNone = "none"
)

// DatasetRow is one attempt's slice of the join. Field names are stable:
// an offline evaluation reads them by name, so renaming one needs a version
// bump, not a silent edit.
type DatasetRow struct {
	TaskID     string      `json:"task_id,omitempty"`
	PromptSHA  string      `json:"prompt_sha256,omitempty"`
	Class      Class       `json:"class,omitempty"`
	Domain     Domain      `json:"domain,omitempty"`
	Path       string      `json:"path,omitempty"`
	Shape      string      `json:"shape,omitempty"`
	TiePolicy  string      `json:"tie_policy,omitempty"`
	Attempt    int         `json:"attempt"`
	Attempts   int         `json:"attempts,omitempty"`
	Repairs    int         `json:"repairs,omitempty"`
	Chosen     Leg         `json:"chosen,omitempty"`
	Leg        Leg         `json:"leg,omitempty"`
	Effort     Effort      `json:"effort,omitempty"`
	Frontier   bool        `json:"frontier,omitempty"`
	GateResult string      `json:"gate_result"`
	RunOutcome string      `json:"run_outcome,omitempty"`
	Verdict    string      `json:"verdict,omitempty"`
	Quality    float64     `json:"quality,omitempty"`
	Cost       float64     `json:"cost_usd,omitempty"`
	CostStatus UsageStatus `json:"cost_status,omitempty"`
	// TaskCost and TaskBilled read the ledger's own accounting for the
	// task. They go empty once the bounded charge log no longer holds it,
	// and stay false: a partial charge log is not a bill.
	TaskCost   float64 `json:"task_cost_usd,omitempty"`
	TaskBilled bool    `json:"task_cost_billed,omitempty"`
	// Settled says the status stopped moving; Label carries that verdict
	// only when the evidence supports one (never by silence - an empty
	// Label with Settled true means nobody objected).
	Settled   bool             `json:"settled,omitempty"`
	Label     AcceptanceStatus `json:"label,omitempty"`
	DecidedBy OutcomeDecider   `json:"decided_by,omitempty"`
	Success   bool             `json:"success,omitempty"`
	At        time.Time        `json:"at"`
	SettledAt time.Time        `json:"settled_at,omitempty"`
	DecidedAt time.Time        `json:"decided_at,omitempty"`
}

// PromptDigest is the prompt's stand-in: the same text always hashes the
// same way, so rows from one prompt join across exports.
func PromptDigest(task string) string {
	sum := sha256.Sum256([]byte(task))
	return hex.EncodeToString(sum[:])
}

// BuildRoutingDataset turns samples into rows, oldest task first. charges is
// the ledger's accounting, used for the per-task billed total; nil gives
// rows with an empty task cost.
func BuildRoutingDataset(samples []RoutingSample, charges []Charge) []DatasetRow {
	var rows []DatasetRow
	for _, s := range samples {
		taskID := s.Decision.TaskID
		if taskID == "" && s.Outcome != nil {
			taskID = s.Outcome.TaskID
		}
		prompt := s.Decision.Task
		if prompt == "" && s.Outcome != nil {
			prompt = s.Outcome.Task
		}
		attempts := s.Decision.Attempt
		for _, e := range s.Events {
			if e.Leg == "" {
				continue // team aggregate: no leg means no attempt row
			}
			if e.Attempt > attempts {
				attempts = e.Attempt
			}
		}
		if s.Outcome != nil && s.Outcome.Attempts > attempts {
			attempts = s.Outcome.Attempts
		}
		if attempts < 1 {
			attempts = 1
		}
		var taskCost float64
		var taskBilled bool
		if taskID != "" {
			t := totalCalls(charges, taskID)
			taskCost, taskBilled = t.CostUSD, t.Complete()
		}
		base := DatasetRow{
			TaskID:     taskID,
			PromptSHA:  promptDigestOrEmpty(prompt),
			GateResult: gateResultOf(s.Outcome),
			Attempts:   attempts,
			Repairs:    attempts - 1,
			TaskCost:   taskCost,
			TaskBilled: taskBilled,
		}
		if s.Decision.TaskID != "" {
			base.Class = s.Decision.Class
			base.Domain = s.Decision.Domain
			base.Path = s.Decision.Path
			base.Shape = s.Decision.Shape
			base.TiePolicy = s.Decision.TiePolicy
			base.Chosen = s.Decision.Chosen
			base.Frontier = s.Decision.ChosenFrontier != nil && *s.Decision.ChosenFrontier
			base.Effort = s.Decision.Effort
			base.DecidedAt = s.Decision.At
		}
		if s.Outcome != nil && s.Outcome.Settled() {
			base.Settled = true
			base.SettledAt = s.Outcome.SettledAt
		}
		// A label the evidence does not support is worse than an empty one:
		// a "settled" row with a label only when the sample carries one
		// (RoutingSample.Labeled - never by silence). A settled row with an
		// empty label means nobody objected, and a learner must see that as
		// the weak signal it is.
		if s.Labeled() {
			base.Label = s.Outcome.Status
			base.DecidedBy = s.Outcome.DecidedBy
			base.Success = s.Outcome.Status == AcceptanceAccepted
		}
		events := append([]Event(nil), s.Events...)
		sort.SliceStable(events, func(i, j int) bool {
			if !events[i].At.Equal(events[j].At) {
				return events[i].At.Before(events[j].At)
			}
			return events[i].Leg < events[j].Leg
		})
		started := false
		for _, e := range events {
			if e.Leg == "" {
				continue
			}
			started = true
			row := base
			row.Leg = e.Leg
			row.Attempt = e.Attempt
			if row.Attempt < 1 {
				row.Attempt = 1
			}
			row.RunOutcome = e.Outcome
			row.Verdict = e.Verdict
			row.Quality = e.Quality
			row.Cost = e.CostUSD
			row.CostStatus = e.CostStatus
			row.At = e.At
			if row.At.IsZero() {
				row.At = row.DecidedAt
			}
			if e.Class != "" {
				row.Class = e.Class
			}
			if e.Effort != "" {
				row.Effort = e.Effort
			}
			if e.TaskID != "" {
				row.TaskID = e.TaskID
			}
			rows = append(rows, row)
		}
		if !started {
			r := base
			r.Attempt = s.Decision.Attempt
			if r.Attempt < 1 {
				r.Attempt = 1
			}
			r.Leg = s.Decision.Chosen
			r.At = r.DecidedAt
			if r.At.IsZero() {
				r.At = time.Now().UTC()
			}
			rows = append(rows, r)
		}
	}
	return rows
}

// promptDigestOrEmpty keeps an absent prompt out of the hash column.
func promptDigestOrEmpty(task string) string {
	if task == "" {
		return ""
	}
	return PromptDigest(task)
}

// gateResultOf reads the task's completion gate from its checks: the last
// `gate:` check by time decides, and a task without one is "none".
func gateResultOf(o *OutcomeEvidence) string {
	if o == nil {
		return DatasetGateNone
	}
	var last *CheckResult
	for i := range o.Checks {
		c := &o.Checks[i]
		if c.Source != "gate" {
			continue
		}
		if last == nil || c.At.After(last.At) {
			last = c
		}
	}
	if last == nil {
		return DatasetGateNone
	}
	if last.Passed {
		return DatasetGatePass
	}
	return DatasetGateFail
}

// WriteRoutingDataset writes one JSON row per line to a new file at 0600.
// The parent directory is created 0700 when it is missing. An existing path
// is refused: a dataset is a fresh artifact, and O_EXCL also keeps a symlink
// from redirecting the write.
func WriteRoutingDataset(path string, rows []DatasetRow) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("routing dataset path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("routing dataset directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("routing dataset already exists: %s", path)
		}
		return fmt.Errorf("routing dataset: %w", err)
	}
	if err := writeRoutingDatasetRows(f, rows); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("routing dataset: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("routing dataset: %w", err)
	}
	return nil
}

// WriteRoutingDatasetTo streams the rows for a caller's own writer
// (`captain why --export -`).
func WriteRoutingDatasetTo(w io.Writer, rows []DatasetRow) error {
	return writeRoutingDatasetRows(w, rows)
}

func writeRoutingDatasetRows(w io.Writer, rows []DatasetRow) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	for i := range rows {
		line, err := json.Marshal(&rows[i])
		if err != nil {
			return err
		}
		if _, err := bw.Write(line); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
	}
	return bw.Flush()
}
