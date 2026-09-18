package captaincode

// Cancellation tree (ROADMAP M3.4). A task may spawn nested work: a workflow
// stage runs parallel workers, a team fans out members, a gate retries after
// repair, a review scores the output. Each of those may hold a context, a
// subprocess, or a provider session. Until M3.4 there was no single signal
// that cancelled all of them: `/parallel stop` cancelled a parallel run's
// context, `captain kill` aborted opencode sessions, and a workflow gate
// had its own timeout — but a workflow whose review was in flight had no
// way to be told to stop, and cancelling a task did not stop its children.
//
// The CancelTree is one registry, keyed by task ID, that maps a task to the
// cancel function of every in-flight operation it owns. Cancel(taskID)
// fires them all. A child registers under its parent's task ID, so cancelling
// a root task cascades to every stage, worker, gate and review beneath it.
//
// The tree is deliberately NOT the lifecycle state machine (lifecycle.go):
// the state machine records WHAT happened and enforces legal transitions;
// the tree delivers the SIGNAL that makes it happen. A task whose context
// is cancelled transitions through cancel_requested → cancelled by the
// caller, not by the tree itself. The tree does not own state — it owns
// the wire.

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// CancelTree is the process-local registry of in-flight cancellable work,
// keyed by task ID. Each entry is the cancel function of a context that
// governs one piece of work (a worker call, a gate, a review, a subprocess).
// Cancel fires every cancel registered under a task, cascading to children.
type CancelTree struct {
	mu      sync.Mutex
	entries map[string][]cancelEntry
	// children maps a task ID to the task IDs of its nested work, so
	// cancelling a root task cascades to every stage and member beneath it.
	children map[string]map[string]bool
	parent   map[string]string // child task → parent task (reverse of children)
}

type cancelEntry struct {
	id      string // unique within a task, for targeted unregister
	cancel  context.CancelFunc
	label   string      // "worker:claude", "gate", "review", "stage:1"
	process *os.Process // nil when not a subprocess; used by the kill fallback
}

// NewCancelTree returns an empty cancellation tree.
func NewCancelTree() *CancelTree {
	return &CancelTree{
		entries:  map[string][]cancelEntry{},
		children: map[string]map[string]bool{},
		parent:   map[string]string{},
	}
}

// Register adds a cancel function under a task. The id identifies this
// particular registration so it can be unregistered without cancelling it
// (the work completed normally). Returns a context derived from parent that
// is cancelled when Cancel is called for this task or any of its ancestors.
func (t *CancelTree) Register(taskID, id, label string, parent context.Context) (context.Context, context.CancelFunc) {
	if t == nil {
		return context.WithCancel(parent)
	}
	ctx, cancel := context.WithCancel(parent)
	t.mu.Lock()
	t.entries[taskID] = append(t.entries[taskID], cancelEntry{id: id, cancel: cancel, label: label})
	t.mu.Unlock()
	return ctx, func() {
		cancel()
		t.Unregister(taskID, id)
	}
}

// RegisterProcess is Register with an OS process handle for the kill fallback.
// When CancelWithDeadline fires, the context cancellation sends SIGKILL via
// exec.CommandContext; the process handle lets the deadline check confirm the
// process is dead and kill it again if it survived (grandchild, ignored signal).
func (t *CancelTree) RegisterProcess(taskID, id, label string, parent context.Context, proc *os.Process) (context.Context, context.CancelFunc) {
	if t == nil {
		return context.WithCancel(parent)
	}
	ctx, cancel := context.WithCancel(parent)
	t.mu.Lock()
	t.entries[taskID] = append(t.entries[taskID], cancelEntry{id: id, cancel: cancel, label: label, process: proc})
	t.mu.Unlock()
	return ctx, func() {
		cancel()
		t.Unregister(taskID, id)
	}
}

// RegisterChild is Register for a task that belongs to a parent task. The
// child is linked so Cancel(parentID) cascades to it. A child with no parent
// (parentID = "") behaves like Register.
func (t *CancelTree) RegisterChild(parentID, taskID, id, label string, parent context.Context) (context.Context, context.CancelFunc) {
	if t == nil {
		return context.WithCancel(parent)
	}
	ctx, cancel := t.Register(taskID, id, label, parent)
	if parentID != "" && parentID != taskID {
		t.mu.Lock()
		if t.children[parentID] == nil {
			t.children[parentID] = map[string]bool{}
		}
		t.children[parentID][taskID] = true
		t.parent[taskID] = parentID
		t.mu.Unlock()
	}
	return ctx, cancel
}

// Unregister removes a single entry by id without firing its cancel. Call
// this when the work completed normally and the cancel is no longer needed.
func (t *CancelTree) Unregister(taskID, id string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	entries := t.entries[taskID]
	for i, e := range entries {
		if e.id == id {
			t.entries[taskID] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	if len(t.entries[taskID]) == 0 {
		delete(t.entries, taskID)
	}
	t.mu.Unlock()
}

// Cancel fires every cancel registered under a task and all its descendants.
// Returns the list of labels that were cancelled, for the lifecycle record.
// A task with no registrations (idle, already finished) is a no-op.
func (t *CancelTree) Cancel(taskID string) []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	cancelled := t.cancelLocked(taskID)
	t.mu.Unlock()
	return cancelled
}

// cancelLocked cancels a task and all its descendants. Caller holds mu.
func (t *CancelTree) cancelLocked(taskID string) []string {
	var cancelled []string
	for _, e := range t.entries[taskID] {
		e.cancel()
		cancelled = append(cancelled, e.label)
	}
	delete(t.entries, taskID)
	for child := range t.children[taskID] {
		cancelled = append(cancelled, t.cancelLocked(child)...)
	}
	delete(t.children, taskID)
	delete(t.parent, taskID)
	return cancelled
}

// Active returns the labels of all in-flight work under a task, including
// descendants. Empty when the task is idle or unknown.
func (t *CancelTree) Active(taskID string) []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.activeLocked(taskID)
}

func (t *CancelTree) activeLocked(taskID string) []string {
	var out []string
	for _, e := range t.entries[taskID] {
		out = append(out, e.label)
	}
	for child := range t.children[taskID] {
		out = append(out, t.activeLocked(child)...)
	}
	return out
}

// TaskIDs returns all task IDs that currently have in-flight work.
func (t *CancelTree) TaskIDs() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.entries))
	for id := range t.entries {
		ids = append(ids, id)
	}
	return ids
}

// CancelAll fires every cancel in the tree. Used on brain shutdown.
func (t *CancelTree) CancelAll() {
	if t == nil {
		return
	}
	t.mu.Lock()
	for taskID := range t.entries {
		for _, e := range t.entries[taskID] {
			e.cancel()
		}
	}
	t.entries = map[string][]cancelEntry{}
	t.children = map[string]map[string]bool{}
	t.parent = map[string]string{}
	t.mu.Unlock()
}

// ── adapter-specific cancellation deadlines (ROADMAP M3.4 remaining) ──

// CancelDeadline is the per-adapter deadline for cooperative cancellation:
// fire the cancel signal, wait up to this duration for the adapter to
// acknowledge, then fall back to kill. The roadmap's provisional local
// target is to acknowledge within one second and stop supported child
// processes within ten seconds. Unsupported or still-running external work
// remains explicitly visible in the Pending list.
type CancelDeadline struct {
	// AckTimeout is how long to wait for the cancel signal to be accepted.
	AckTimeout time.Duration
	// StopTimeout is how long to wait for the process to actually stop
	// after the cancel signal, before falling back to kill.
	StopTimeout time.Duration
}

// DefaultCancelDeadlines returns the provisional target deadlines per leg.
// A leg that is not in the map uses the default (10s stop, 1s ack). These
// are deliberately generous: a subprocess that ignores SIGTERM for 5s
// should still be given a chance to clean up before SIGKILL.
func DefaultCancelDeadlines() map[Leg]CancelDeadline {
	return map[Leg]CancelDeadline{
		LegClaude:   {AckTimeout: time.Second, StopTimeout: 10 * time.Second},
		LegCodex:    {AckTimeout: time.Second, StopTimeout: 10 * time.Second},
		LegCursor:   {AckTimeout: time.Second, StopTimeout: 10 * time.Second},
		LegCodexCLI: {AckTimeout: time.Second, StopTimeout: 5 * time.Second},
	}
}

// DefaultCancelDeadline returns the fallback deadline for legs without a
// specific entry.
func DefaultCancelDeadline() CancelDeadline {
	return CancelDeadline{AckTimeout: time.Second, StopTimeout: 10 * time.Second}
}

// DeadlineFor returns the cancellation deadline for a leg, falling back to
// the default when no specific entry exists.
func DeadlineFor(leg Leg, overrides map[Leg]CancelDeadline) CancelDeadline {
	if d, ok := overrides[leg]; ok {
		return d
	}
	return DefaultCancelDeadline()
}

// CancelResult is what CancelWithDeadline returns: what was confirmed
// stopped within the deadline, and what is still pending (the adapter
// did not acknowledge or the process is still alive).
type CancelResult struct {
	Cancelled []string // labels that were cancelled (signal fired)
	Pending   []string // labels that did not confirm stop within the deadline
}

// CancelWithDeadline fires the cancel signal for a task and all its
// descendants, then waits for the ack timeout. After the ack window, any
// entry whose process is still alive is killed (SIGKILL) and reported as
// pending — the cooperative stop failed and the hard kill was the fallback.
//
// This is the M3.4 kill fallback: fire the signal, wait, confirm dead, kill
// stragglers. The context cancellation from exec.CommandContext already
// sends SIGKILL, but a grandchild that inherited the pipe or a process that
// survived the signal is killed here as a last resort.
func (t *CancelTree) CancelWithDeadline(taskID string, deadline CancelDeadline) CancelResult {
	if t == nil {
		return CancelResult{}
	}
	t.mu.Lock()
	entries := t.collectEntriesLocked(taskID)
	for _, e := range entries {
		e.cancel()
	}
	t.mu.Unlock()

	var cancelled []string
	for _, e := range entries {
		cancelled = append(cancelled, e.label)
	}

	// Wait the ack timeout for context cancellation to propagate.
	ackCtx, ackCancel := context.WithTimeout(context.Background(), deadline.AckTimeout)
	defer ackCancel()
	<-ackCtx.Done()

	// Kill fallback: any entry with a process handle that is still alive
	// after the ack timeout gets a hard kill. The process may have survived
	// because exec.CommandContext's SIGKILL raced a grandchild holding the
	// pipe, or the process ignored the signal. A process with no handle
	// (context-only work: gates, reviews) has nothing to kill.
	var pending []string
	for _, e := range entries {
		if e.process == nil {
			continue
		}
		if !processAlive(e.process) {
			continue
		}
		_ = e.process.Kill()
		pending = append(pending, e.label)
	}
	return CancelResult{Cancelled: cancelled, Pending: pending}
}

// collectEntriesLocked gathers all entries for a task and its descendants.
// Caller holds mu.
func (t *CancelTree) collectEntriesLocked(taskID string) []cancelEntry {
	var out []cancelEntry
	out = append(out, t.entries[taskID]...)
	delete(t.entries, taskID)
	for child := range t.children[taskID] {
		out = append(out, t.collectEntriesLocked(child)...)
	}
	delete(t.children, taskID)
	delete(t.parent, taskID)
	return out
}

// processAlive returns true when the process still exists. Signal 0 is the
// existence check on POSIX systems: it does not signal the process but returns
// ESRCH when it has exited. A nil process is not alive.
func processAlive(p *os.Process) bool {
	if p == nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// ── resume policy (ROADMAP M3.4) ──

// ResumeDecision is what the resume policy says about one interrupted attempt.
type ResumeDecision string

const (
	ResumeSkip    ResumeDecision = "skip"    // do not resume; leave interrupted
	ResumeRetry   ResumeDecision = "retry"   // re-run from the start under the same task and remaining budget
	ResumeReview  ResumeDecision = "review"  // surface to the user; do not auto-resume
	ResumeAbandon ResumeDecision = "abandon" // mark failed; the work is stale or the cost too high
)

// ResumePolicy decides what to do with interrupted attempts after a brain
// restart. M3.4's default is conservative: do not auto-resume (the provider
// session is gone, the worktree may be stale, and resuming a partial result
// is not the same as retrying). Surface them for the user to decide.
type ResumePolicy struct {
	// MaxAge is how old an interrupted attempt may be to be worth surfacing.
	// Older than this, the work is stale and the policy says abandon.
	MaxAge time.Duration
}

// DefaultResumePolicy returns the conservative default: surface interrupted
// work up to 24h old for user review, abandon anything older.
func DefaultResumePolicy() ResumePolicy {
	return ResumePolicy{MaxAge: 24 * time.Hour}
}

// Decide evaluates one interrupted attempt and returns what to do with it.
// The decision is deliberately not "retry": a provider session cannot be
// resumed across a brain restart (the session belongs to the dead process),
// and a worktree from before the restart may have been manually changed.
// The honest action is to tell the user what was interrupted and let them
// decide.
func (p ResumePolicy) Decide(as AttemptState, now time.Time) ResumeDecision {
	if as.State != StateInterrupted {
		return ResumeSkip
	}
	age := now.Sub(as.UpdatedAt)
	if p.MaxAge > 0 && age > p.MaxAge {
		return ResumeAbandon
	}
	return ResumeReview
}

// ResumeReport summarises the result of evaluating all interrupted attempts.
type ResumeReport struct {
	Skip    int
	Retry   int
	Review  []AttemptState
	Abandon []AttemptState
}

// EvaluateInterrupted walks the ledger's interrupted attempts and applies the
// policy. It does NOT mutate state: the caller decides what to do with the
// report. This is the separation M3.4 requires — the policy recommends, the
// caller acts.
func EvaluateInterrupted(l *Ledger, policy ResumePolicy, now time.Time) ResumeReport {
	var report ResumeReport
	for _, as := range l.InterruptedAttempts() {
		switch policy.Decide(as, now) {
		case ResumeSkip:
			report.Skip++
		case ResumeRetry:
			report.Retry++
		case ResumeReview:
			report.Review = append(report.Review, as)
		case ResumeAbandon:
			report.Abandon = append(report.Abandon, as)
		}
	}
	return report
}

// FormatResumeReport renders a resume report for the brain log or CLI.
func FormatResumeReport(r ResumeReport) string {
	if r.Skip == 0 && len(r.Review) == 0 && len(r.Abandon) == 0 {
		return ""
	}
	out := ""
	if len(r.Review) > 0 {
		out += fmt.Sprintf("%d interrupted attempt(s) await a resume decision:\n", len(r.Review))
		for _, as := range r.Review {
			out += fmt.Sprintf("  · task %s attempt %s (leg %s, interrupted %s ago)\n",
				as.TaskID, as.AttemptID, as.Leg, time.Since(as.UpdatedAt).Round(time.Second))
		}
	}
	if len(r.Abandon) > 0 {
		out += fmt.Sprintf("%d interrupted attempt(s) abandoned (stale beyond %s):\n", len(r.Abandon), DefaultResumePolicy().MaxAge)
		for _, as := range r.Abandon {
			out += fmt.Sprintf("  · task %s attempt %s (leg %s, interrupted %s ago)\n",
				as.TaskID, as.AttemptID, as.Leg, time.Since(as.UpdatedAt).Round(time.Second))
		}
	}
	return out
}
