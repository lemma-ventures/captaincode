package captaincode

// Durable lifecycle (ROADMAP M3.3). The ledger already persists charges (the
// accounting tree), budgets (the resource envelope) and decisions (the routing
// rationale). What it does not persist is the EXECUTION STATE of each task and
// attempt: whether it is running, waiting, interrupted or terminal, which
// process owns it, and where its artifacts live. A brain restart loses all of
// that — an interrupted task has no record that it was interrupted, and nothing
// prevents a second brain process from resuming the same attempt.
//
// M3.3 adds the missing spine:
//
//   - LifecycleState is the legal state machine for a task/attempt:
//     admitted → running → waiting_for_input → terminal, with cancel_requested
//     and interrupted as intermediate states. A terminal attempt never silently
//     becomes a new attempt.
//   - AttemptState carries the process/session identity, an ownership generation
//     (so two brains cannot resume the same attempt), and artifact/checkpoint
//     references.
//   - ReconcileOnStartup walks the persisted states and marks any that were
//     running or waiting as interrupted, because the process that owned them is
//     gone. M3.3 does NOT auto-resume; it makes the interrupted state visible
//     so M3.4 can decide what to do about it.
//
// The design is deliberately separate from the Charge tree: a charge is an
// accounting record (what it cost), while an AttemptState is an execution
// record (where it was, who owned it, whether it finished). They share the
// same TaskID/AttemptID keys but serve different readers.

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// LifecycleVersion is stamped on every persisted state. Readers reject what
// they do not understand, the same contract the other versioned types carry.
const LifecycleVersion = 1

// LifecycleState is the execution state of a task or attempt. The states are
// the legal transitions defined in ROADMAP M3.3's lifecycle contract.
type LifecycleState string

const (
	StateAdmitted        LifecycleState = "admitted"          // task accepted, not yet dispatching
	StateRunning         LifecycleState = "running"           // a provider call is in flight
	StateWaitingForInput LifecycleState = "waiting_for_input" // paused, waiting for user or gate
	StateCancelRequested LifecycleState = "cancel_requested"  // cancellation signaled, not yet confirmed
	StateInterrupted     LifecycleState = "interrupted"       // process died or lost ownership; NOT terminal
	StateSucceeded       LifecycleState = "succeeded"         // terminal: objective met
	StateFailed          LifecycleState = "failed"            // terminal: objective not met
	StateCancelled       LifecycleState = "cancelled"         // terminal: user cancelled
	StateExhausted       LifecycleState = "exhausted"         // terminal: budget/quota exhausted
)

// IsTerminal reports whether a state is terminal: no further transitions are
// legal from it. A terminal attempt never silently becomes a new attempt.
func (s LifecycleState) IsTerminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled, StateExhausted:
		return true
	}
	return false
}

// legalTransitions defines the state machine. Each key maps to the set of
// states that may follow it. A transition not in this map is rejected.
var legalTransitions = map[LifecycleState]map[LifecycleState]bool{
	StateAdmitted: {
		StateRunning:     true,
		StateCancelled:   true,
		StateInterrupted: true,
		StateFailed:      true,
	},
	StateRunning: {
		StateWaitingForInput: true,
		StateCancelRequested: true,
		StateInterrupted:     true,
		StateSucceeded:       true,
		StateFailed:          true,
		StateExhausted:       true,
		StateCancelled:       true,
	},
	StateWaitingForInput: {
		StateRunning:         true,
		StateCancelRequested: true,
		StateInterrupted:     true,
		StateCancelled:       true,
		StateFailed:          true,
	},
	StateCancelRequested: {
		StateCancelled:   true,
		StateInterrupted: true,
		StateFailed:      true,
	},
	StateInterrupted: {
		StateRunning:   true, // resumption: a new attempt under the same task
		StateCancelled: true,
		StateFailed:    true,
	},
}

// CanTransition reports whether from → to is a legal transition.
func CanTransition(from, to LifecycleState) bool {
	if from == to {
		return true
	}
	allowed, ok := legalTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// AttemptState is the durable execution record for one attempt of one task.
// It is persisted in the ledger so a brain restart can see what was in flight
// and what owned it. The charge tree records what it cost; this records where
// it was in its lifecycle.
type AttemptState struct {
	Version         int            `json:"version"`
	TaskID          string         `json:"task_id"`
	AttemptID       string         `json:"attempt_id"`
	StageID         string         `json:"stage_id,omitempty"`
	State           LifecycleState `json:"state"`
	Leg             Leg            `json:"leg,omitempty"`
	ProcessID       string         `json:"process_id,omitempty"`   // brain PID that owns this attempt
	OwnerGen        int            `json:"owner_gen"`              // ownership generation: increments on each claim
	SessionID       string         `json:"session_id,omitempty"`   // provider session for reuse/resume
	WorktreeDir     string         `json:"worktree_dir,omitempty"` // M3.1 isolation directory
	DiffPath        string         `json:"diff_path,omitempty"`    // M3.2 artifact reference
	StartedAt       time.Time      `json:"started_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	TerminalAt      time.Time      `json:"terminal_at,omitempty"`
	StopReason      string         `json:"stop_reason,omitempty"`      // budget stop reason when terminal
	InterruptReason string         `json:"interrupt_reason,omitempty"` // why it was interrupted (crash, lost ownership)
	ParentAttempt   string         `json:"parent_attempt,omitempty"`   // interrupted attempt this resumes (causal link)
	// Checkpoint fields (M3.3): the recovery-relevant state captured at safe
	// boundaries so M3.4's resume policy can decide whether to retry. The
	// worktree dir and diff path are already above; these fields add what
	// the recovery decision needs beyond artifact references.
	BaseRevision    string    `json:"base_revision,omitempty"`    // commit sha the worktree was created from
	CheckpointPhase string    `json:"checkpoint_phase,omitempty"` // dispatching|producing|gating|reviewing
	CheckpointAt    time.Time `json:"checkpoint_at,omitempty"`    // when the checkpoint was last updated
	PartialOutput   string    `json:"partial_output,omitempty"`   // truncated text the worker produced before interruption
	// Solo artifact fields (M3.5): a solo turn has no IntegrationCandidate,
	// but it may still have produced file changes. ChangedFiles and DiffDigest
	// capture what the worker changed in the user's workspace, so the handoff
	// brief carries the same artifact evidence a parallel workflow does.
	ChangedFiles []string `json:"changed_files,omitempty"` // files the solo worker touched
	DiffDigest   string   `json:"diff_digest,omitempty"`   // sha256 of the solo worker's diff
}

// TaskState is the roll-up view of a task's lifecycle: its own state plus the
// states of its attempts. A task is terminal when all its attempts are
// terminal and it has no running work.
type TaskState struct {
	Version    int            `json:"version"`
	TaskID     string         `json:"task_id"`
	Label      string         `json:"label,omitempty"`
	State      LifecycleState `json:"state"`
	StartedAt  time.Time      `json:"started_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	TerminalAt time.Time      `json:"terminal_at,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
}

// maxLifecycle caps the persisted state log, the same way maxCharges and
// maxBudgets cap theirs. One TaskState per task plus one AttemptState per
// attempt: a few hundred is weeks of use.
const maxLifecycle = 500

// ---- ledger integration ----

// RecordTaskState stores or updates a task's lifecycle state. The task ID is
// the key: one state per task, updated in place as transitions occur.
func (l *Ledger) RecordTaskState(ts TaskState) {
	if ts.TaskID == "" {
		return
	}
	ts.Version = LifecycleVersion
	if ts.UpdatedAt.IsZero() {
		ts.UpdatedAt = time.Now()
	}
	for i, old := range l.TaskStates {
		if old.TaskID == ts.TaskID {
			l.TaskStates[i] = ts
			return
		}
	}
	l.TaskStates = append(l.TaskStates, ts)
	if len(l.TaskStates) > maxLifecycle {
		l.TaskStates = l.TaskStates[len(l.TaskStates)-maxLifecycle:]
	}
}

// TaskStateFor returns the lifecycle state for a task, or nil when none exists.
func (l *Ledger) TaskStateFor(taskID string) *TaskState {
	for i, ts := range l.TaskStates {
		if ts.TaskID == taskID {
			return &l.TaskStates[i]
		}
	}
	return nil
}

// RecordAttemptState stores or updates an attempt's lifecycle state. The
// attempt ID is the key: one state per attempt, updated in place.
func (l *Ledger) RecordAttemptState(as AttemptState) {
	if as.AttemptID == "" {
		return
	}
	as.Version = LifecycleVersion
	if as.UpdatedAt.IsZero() {
		as.UpdatedAt = time.Now()
	}
	for i, old := range l.AttemptStates {
		if old.AttemptID == as.AttemptID {
			l.AttemptStates[i] = as
			return
		}
	}
	l.AttemptStates = append(l.AttemptStates, as)
	if len(l.AttemptStates) > maxLifecycle {
		l.AttemptStates = l.AttemptStates[len(l.AttemptStates)-maxLifecycle:]
	}
}

// AttemptStateFor returns the lifecycle state for an attempt, or nil.
func (l *Ledger) AttemptStateFor(attemptID string) *AttemptState {
	for i, as := range l.AttemptStates {
		if as.AttemptID == attemptID {
			return &l.AttemptStates[i]
		}
	}
	return nil
}

// AttemptStatesFor returns all attempt states for a task, oldest first.
func (l *Ledger) AttemptStatesFor(taskID string) []AttemptState {
	var out []AttemptState
	for _, as := range l.AttemptStates {
		if as.TaskID == taskID {
			out = append(out, as)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// InterruptedAttempts returns all attempt states that are interrupted (the
// process that owned them is gone). These are the candidates for M3.4's
// recovery/resume decision.
func (l *Ledger) InterruptedAttempts() []AttemptState {
	var out []AttemptState
	for _, as := range l.AttemptStates {
		if as.State == StateInterrupted {
			out = append(out, as)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out
}

// ReconcileOnStartup walks persisted attempt states and marks any that were
// running, waiting, or cancel-requested as interrupted, because the process
// that owned them is gone (this is a fresh brain start). The ownership
// generation is NOT incremented: that happens when a new brain claims the
// attempt for resumption. M3.3 makes the interrupted state visible; M3.4
// decides what to do about it.
//
// Returns the count of newly-interrupted attempts for logging.
func (l *Ledger) ReconcileOnStartup() int {
	count := 0
	now := time.Now()
	for i := range l.AttemptStates {
		as := &l.AttemptStates[i]
		if as.State.IsTerminal() || as.State == StateInterrupted {
			continue
		}
		as.State = StateInterrupted
		as.InterruptReason = "brain process restarted"
		as.UpdatedAt = now
		count++
	}
	for i := range l.TaskStates {
		ts := &l.TaskStates[i]
		if ts.State.IsTerminal() || ts.State == StateInterrupted {
			continue
		}
		ts.State = StateInterrupted
		ts.UpdatedAt = now
	}
	return count
}

// ClaimOwnership attempts to claim an attempt for a new brain process. The
// ownership generation increments on each claim, so a brain that held a stale
// generation cannot resume an attempt a newer brain has since claimed. Returns
// the new generation, or 0 when the attempt is terminal (no resumption).
func (l *Ledger) ClaimOwnership(attemptID, processID string) int {
	as := l.AttemptStateFor(attemptID)
	if as == nil {
		return 0
	}
	if as.State.IsTerminal() {
		return 0
	}
	as.OwnerGen++
	as.ProcessID = processID
	as.UpdatedAt = time.Now()
	return as.OwnerGen
}

// VerifyOwnership reports whether processID still owns the attempt at the
// given generation. A mismatch means another process has since claimed it.
func (l *Ledger) VerifyOwnership(attemptID, processID string, gen int) bool {
	as := l.AttemptStateFor(attemptID)
	if as == nil {
		return false
	}
	return as.ProcessID == processID && as.OwnerGen == gen
}

// TransitionAttempt transitions an attempt from its current state to a new
// one, enforcing the legal state machine. Returns an error when the transition
// is not legal. The caller should persist the ledger after a successful
// transition.
func (l *Ledger) TransitionAttempt(attemptID string, to LifecycleState) error {
	as := l.AttemptStateFor(attemptID)
	if as == nil {
		return fmt.Errorf("lifecycle: attempt %s not found", attemptID)
	}
	if !CanTransition(as.State, to) {
		return fmt.Errorf("lifecycle: transition %s → %s not legal for attempt %s", as.State, to, attemptID)
	}
	as.State = to
	as.UpdatedAt = time.Now()
	if to.IsTerminal() {
		as.TerminalAt = time.Now()
	}
	return nil
}

// TransitionTask transitions a task's lifecycle state. A task may go to
// terminal only when all its attempts are terminal.
func (l *Ledger) TransitionTask(taskID string, to LifecycleState) error {
	ts := l.TaskStateFor(taskID)
	if ts == nil {
		return fmt.Errorf("lifecycle: task %s not found", taskID)
	}
	if !CanTransition(ts.State, to) {
		return fmt.Errorf("lifecycle: transition %s → %s not legal for task %s", ts.State, to, taskID)
	}
	if to.IsTerminal() {
		for _, as := range l.AttemptStatesFor(taskID) {
			if !as.State.IsTerminal() {
				return fmt.Errorf("lifecycle: task %s cannot go terminal: attempt %s is %s", taskID, as.AttemptID, as.State)
			}
		}
	}
	ts.State = to
	ts.UpdatedAt = time.Now()
	if to.IsTerminal() {
		ts.TerminalAt = time.Now()
	}
	return nil
}

// ResumeTask creates a NEW attempt under an interrupted task, with a causal
// link to the interrupted attempt it resumes from. This is the explicit resume
// action the roadmap describes: the policy recommends, the user decides, and
// this method acts. The old interrupted attempt is transitioned to `failed`
// (its process is gone); the new attempt starts as `running` under the current
// brain's ownership.
//
// Returns the new AttemptState, or an error when the task has no interrupted
// attempts, the task is terminal, or the budget is exhausted.
func (l *Ledger) ResumeTask(taskID, processID string) (AttemptState, error) {
	ts := l.TaskStateFor(taskID)
	if ts == nil {
		return AttemptState{}, fmt.Errorf("lifecycle: task %s not found", taskID)
	}
	if ts.State.IsTerminal() {
		return AttemptState{}, fmt.Errorf("lifecycle: task %s is terminal (%s)", taskID, ts.State)
	}
	interrupted := l.InterruptedAttempts()
	var candidate *AttemptState
	for i := range interrupted {
		if interrupted[i].TaskID == taskID {
			candidate = &interrupted[i]
			break
		}
	}
	if candidate == nil {
		return AttemptState{}, fmt.Errorf("lifecycle: task %s has no interrupted attempts to resume", taskID)
	}
	oldAttemptID := candidate.AttemptID
	l.TransitionAttempt(oldAttemptID, StateFailed)
	newAttempt := AttemptState{
		Version:       LifecycleVersion,
		TaskID:        taskID,
		AttemptID:     NewChargeID(KindAttempt),
		State:         StateRunning,
		Leg:           candidate.Leg,
		ProcessID:     processID,
		OwnerGen:      1,
		StartedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		ParentAttempt: oldAttemptID,
	}
	l.RecordAttemptState(newAttempt)
	if CanTransition(ts.State, StateRunning) {
		l.TransitionTask(taskID, StateRunning)
	}
	return newAttempt, nil
}

// CurrentProcessID returns a stable identifier for this brain process, for
// ownership stamps. It combines PID with start time so two sequential brains
// on the same PID are distinguishable.
func CurrentProcessID() string {
	return fmt.Sprintf("pid:%d@%d", os.Getpid(), time.Now().Unix())
}

// Checkpoint phases name what an attempt was doing when it was last checkpointed.
// They are the recovery-relevant signal M3.4 needs: an attempt that was
// "dispatching" when interrupted had no provider output to salvage, while one
// that was "producing" may have partial text worth recovering.
const (
	PhaseDispatching = "dispatching" // worker call started, no output yet
	PhaseProducing   = "producing"   // worker streaming output
	PhaseGating      = "gating"      // gate/review check running
	PhaseReviewing   = "reviewing"   // director review in progress
)

// Checkpoint updates the recovery-relevant fields on an attempt at a safe
// boundary. The caller passes the phase (what the attempt is doing now) and
// any partial output worth recovering. The base revision and worktree dir
// are already on the AttemptState; this method stamps the phase and timestamp
// so ReconcileCheckpoints can report what was happening when the process died.
func (l *Ledger) Checkpoint(attemptID, phase, partialOutput string) {
	as := l.AttemptStateFor(attemptID)
	if as == nil {
		return
	}
	as.CheckpointPhase = phase
	as.CheckpointAt = time.Now()
	if partialOutput != "" {
		as.PartialOutput = truncateStr(partialOutput, 2000)
	}
	as.UpdatedAt = time.Now()
}

// RecordSoloArtifact populates the M3.5 solo-turn artifact fields on an
// attempt: the changed files, diff digest, and diff path captured after a
// solo worker finishes. This gives the handoff brief the same artifact
// evidence a parallel workflow's IntegrationCandidate carries, without
// requiring a worktree. Called after the worker completes but before the
// attempt transitions to terminal.
func (l *Ledger) RecordSoloArtifact(attemptID string, changedFiles []string, diffDigest, diffPath string) {
	as := l.AttemptStateFor(attemptID)
	if as == nil {
		return
	}
	as.ChangedFiles = changedFiles
	as.DiffDigest = diffDigest
	as.DiffPath = diffPath
	as.UpdatedAt = time.Now()
}

// ReconcileCheckpoints walks interrupted attempts after a brain restart and
// verifies whether their worktrees and diff files still exist on disk. This
// is the "persisting checkpoints beyond artifact refs" step M3.3 names: the
// AttemptState carries paths, but paths are not survival — a worktree may
// have been manually removed, or a temp dir cleaned. The recovery policy
// (M3.4) reads this to decide whether a resume is even possible.
//
// Returns a slice of CheckpointStatus, one per interrupted attempt, carrying
// the attempt ID, phase, and whether the worktree and diff survived.
type CheckpointStatus struct {
	AttemptID        string
	TaskID           string
	Phase            string
	WorktreeSurvived bool
	DiffSurvived     bool
	HasPartialText   bool
}

func (l *Ledger) ReconcileCheckpoints() []CheckpointStatus {
	interrupted := l.InterruptedAttempts()
	out := make([]CheckpointStatus, 0, len(interrupted))
	for _, as := range interrupted {
		cs := CheckpointStatus{
			AttemptID:        as.AttemptID,
			TaskID:           as.TaskID,
			Phase:            as.CheckpointPhase,
			HasPartialText:   as.PartialOutput != "",
			WorktreeSurvived: as.WorktreeDir != "" && dirExists(as.WorktreeDir),
			DiffSurvived:     as.DiffPath != "" && fileExists(as.DiffPath),
		}
		out = append(out, cs)
	}
	return out
}

// dirExists reports whether a directory exists at path.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// FormatCheckpointStatus renders a CheckpointStatus for the resume report
// `captain lifecycle` surfaces on startup.
func FormatCheckpointStatus(cs CheckpointStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s: phase=%s", cs.AttemptID, cs.Phase)
	if cs.WorktreeSurvived {
		b.WriteString(", worktree=ok")
	} else {
		b.WriteString(", worktree=missing")
	}
	if cs.DiffSurvived {
		b.WriteString(", diff=ok")
	} else {
		b.WriteString(", diff=missing")
	}
	if cs.HasPartialText {
		b.WriteString(", partial-text=yes")
	}
	return b.String()
}

// LifecycleCoverage returns aggregate counts for `captain stats`: how many
// tasks/attemptes are in each state, for visibility into in-flight work.
func (l *Ledger) LifecycleCoverage() (tasks, attempts int, byState map[LifecycleState]int) {
	byState = map[LifecycleState]int{}
	for _, ts := range l.TaskStates {
		tasks++
		byState[ts.State]++
	}
	attempts = len(l.AttemptStates)
	for _, as := range l.AttemptStates {
		byState[as.State]++
	}
	return
}
