package main

// Cancellation and lifecycle HTTP handlers (ROADMAP M3.4).
//
// These expose the M3.4 cancellation tree and the M3.3 lifecycle state
// machine over the brain's local HTTP API, so the TUI and CLI can:
//
//   - cancel a task in flight (POST /v1/cancel?task=<id>) — fires every
//     cancel registered under the task, cascading to children
//   - inspect what's in flight (GET /v1/cancel) — what work is registered
//   - read lifecycle states (GET /v1/lifecycle) — task/attempt states

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// cancelHTTP handles POST /v1/cancel?task=<id> and GET /v1/cancel.
func (b *brain) cancelHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.cancelListHTTP(w, r)
	case http.MethodPost:
		b.cancelTaskHTTP(w, r)
	default:
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
	}
}

func (b *brain) cancelListHTTP(w http.ResponseWriter, _ *http.Request) {
	taskIDs := b.cancelTree.TaskIDs()
	type entry struct {
		TaskID string   `json:"task_id"`
		Active []string `json:"active"`
	}
	var out []entry
	for _, id := range taskIDs {
		out = append(out, entry{TaskID: id, Active: b.cancelTree.Active(id)})
	}
	writeJSON(w, 200, map[string]any{"tasks": out, "count": len(out)})
}

func (b *brain) cancelTaskHTTP(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	if taskID == "" {
		writeJSON(w, 400, map[string]any{"error": "missing task parameter"})
		return
	}
	result := b.cancelTaskWithDeadline(taskID)
	writeJSON(w, 200, map[string]any{
		"task_id":   taskID,
		"cancelled": result.Cancelled,
		"pending":   result.Pending,
		"count":     len(result.Cancelled),
	})
}

// lifecycleHTTP handles GET /v1/lifecycle for inspecting task/attempt states.
func (b *brain) lifecycleHTTP(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	b.mu.Lock()
	defer b.mu.Unlock()
	if taskID != "" {
		ts := b.ledger.TaskStateFor(taskID)
		if ts == nil {
			writeJSON(w, 404, map[string]any{"error": "task not found"})
			return
		}
		attempts := b.ledger.AttemptStatesFor(taskID)
		writeJSON(w, 200, map[string]any{
			"task":     ts,
			"attempts": attempts,
		})
		return
	}
	tasks, attempts, byState := b.ledger.LifecycleCoverage()
	writeJSON(w, 200, map[string]any{
		"task_count":    tasks,
		"attempt_count": attempts,
		"by_state":      byState,
		"interrupted":   len(b.ledger.InterruptedAttempts()),
		"resume_report": captaincode.FormatResumeReport(captaincode.EvaluateInterrupted(b.ledger, captaincode.DefaultResumePolicy(), time.Now())),
	})
}

// lifecycleSummary is a compact string for the brain log or `captain lifecycle` CLI.
func (b *brain) lifecycleSummary() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	tasks, attempts, byState := b.ledger.LifecycleCoverage()
	interrupted := len(b.ledger.InterruptedAttempts())
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d task(s), %d attempt(s)", tasks, attempts)
	if interrupted > 0 {
		fmt.Fprintf(&sb, ", %d interrupted", interrupted)
	}
	sb.WriteString("\n")
	for _, state := range []captaincode.LifecycleState{
		captaincode.StateRunning, captaincode.StateWaitingForInput,
		captaincode.StateCancelRequested, captaincode.StateInterrupted,
		captaincode.StateSucceeded, captaincode.StateFailed,
		captaincode.StateCancelled, captaincode.StateExhausted,
	} {
		if n := byState[state]; n > 0 {
			fmt.Fprintf(&sb, "  %s: %d\n", state, n)
		}
	}
	return sb.String()
}

var _ = json.Marshal

// resumeHTTP handles POST /v1/resume?task=<id> — the explicit resume action
// (ROADMAP M3.4). Creates a NEW attempt under an interrupted task, with a
// causal link to the interrupted attempt it resumes from. The old interrupted
// attempt is transitioned to failed; the new attempt starts as running under
// this brain's ownership. The budget is checked before resuming.
func (b *brain) resumeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	if taskID == "" {
		writeJSON(w, 400, map[string]any{"error": "missing task parameter"})
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	budget := b.ledger.BudgetFor(taskID)
	if budget != nil && budget.MaxAttempts > 0 && budget.SettledAttempts+budget.ReservedAttempts >= budget.MaxAttempts {
		writeJSON(w, 409, map[string]any{"error": "budget exhausted: no attempts remaining"})
		return
	}
	newAttempt, err := b.ledger.ResumeTask(taskID, b.processID)
	if err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error()})
		return
	}
	b.ledger.Save()
	writeJSON(w, 200, map[string]any{
		"task_id":        taskID,
		"attempt_id":     newAttempt.AttemptID,
		"parent_attempt": newAttempt.ParentAttempt,
		"state":          string(newAttempt.State),
		"leg":            string(newAttempt.Leg),
		"budget":         budget,
	})
}
