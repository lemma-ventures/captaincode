package captaincode

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLifecycleIsTerminal(t *testing.T) {
	terminal := []LifecycleState{StateSucceeded, StateFailed, StateCancelled, StateExhausted}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Fatalf("%s should be terminal", s)
		}
	}
	nonTerminal := []LifecycleState{StateAdmitted, StateRunning, StateWaitingForInput, StateCancelRequested, StateInterrupted}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Fatalf("%s should not be terminal", s)
		}
	}
}

func TestLifecycleCanTransition(t *testing.T) {
	cases := []struct {
		from, to LifecycleState
		want     bool
	}{
		{StateAdmitted, StateRunning, true},
		{StateAdmitted, StateCancelled, true},
		{StateAdmitted, StateInterrupted, true},
		{StateAdmitted, StateSucceeded, false},
		{StateRunning, StateWaitingForInput, true},
		{StateRunning, StateCancelRequested, true},
		{StateRunning, StateInterrupted, true},
		{StateRunning, StateSucceeded, true},
		{StateRunning, StateFailed, true},
		{StateRunning, StateExhausted, true},
		{StateRunning, StateAdmitted, false},
		{StateSucceeded, StateRunning, false},
		{StateFailed, StateRunning, false},
		{StateCancelled, StateRunning, false},
		{StateInterrupted, StateRunning, true},
		{StateInterrupted, StateCancelled, true},
		{StateInterrupted, StateFailed, true},
		{StateInterrupted, StateSucceeded, false},
		{StateWaitingForInput, StateRunning, true},
		{StateWaitingForInput, StateCancelRequested, true},
		{StateCancelRequested, StateCancelled, true},
		{StateCancelRequested, StateRunning, false},
	}
	for _, c := range cases {
		got := CanTransition(c.from, c.to)
		if got != c.want {
			t.Fatalf("CanTransition(%s, %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestLedgerRecordTaskState(t *testing.T) {
	l := &Ledger{TaskStates: []TaskState{}}
	now := time.Now()
	ts := TaskState{TaskID: "t1", Label: "test", State: StateAdmitted, StartedAt: now}
	l.RecordTaskState(ts)
	if got := l.TaskStateFor("t1"); got == nil || got.State != StateAdmitted {
		t.Fatalf("expected admitted task state, got %+v", got)
	}
	ts.State = StateRunning
	l.RecordTaskState(ts)
	if got := l.TaskStateFor("t1"); got == nil || got.State != StateRunning {
		t.Fatalf("expected running task state after update, got %+v", got)
	}
	if len(l.TaskStates) != 1 {
		t.Fatalf("expected 1 task state, got %d", len(l.TaskStates))
	}
}

func TestLedgerRecordAttemptState(t *testing.T) {
	l := &Ledger{AttemptStates: []AttemptState{}}
	now := time.Now()
	as := AttemptState{AttemptID: "a1", TaskID: "t1", State: StateRunning, ProcessID: "p1", StartedAt: now}
	l.RecordAttemptState(as)
	if got := l.AttemptStateFor("a1"); got == nil || got.State != StateRunning {
		t.Fatalf("expected running attempt, got %+v", got)
	}
	as.State = StateSucceeded
	l.RecordAttemptState(as)
	if got := l.AttemptStateFor("a1"); got == nil || got.State != StateSucceeded {
		t.Fatalf("expected succeeded attempt, got %+v", got)
	}
	if len(l.AttemptStates) != 1 {
		t.Fatalf("expected 1 attempt state, got %d", len(l.AttemptStates))
	}
}

func TestLedgerAttemptStatesFor(t *testing.T) {
	l := &Ledger{AttemptStates: []AttemptState{}}
	now := time.Now()
	l.RecordAttemptState(AttemptState{AttemptID: "a1", TaskID: "t1", State: StateRunning, StartedAt: now})
	l.RecordAttemptState(AttemptState{AttemptID: "a2", TaskID: "t1", State: StateSucceeded, StartedAt: now.Add(time.Second)})
	l.RecordAttemptState(AttemptState{AttemptID: "a3", TaskID: "t2", State: StateRunning, StartedAt: now})
	got := l.AttemptStatesFor("t1")
	if len(got) != 2 {
		t.Fatalf("expected 2 attempts for t1, got %d", len(got))
	}
	if got[0].AttemptID != "a1" {
		t.Fatalf("expected a1 first (oldest), got %s", got[0].AttemptID)
	}
}

func TestLedgerReconcileOnStartup(t *testing.T) {
	l := &Ledger{
		TaskStates: []TaskState{
			{TaskID: "t1", State: StateRunning, StartedAt: time.Now()},
			{TaskID: "t2", State: StateSucceeded, StartedAt: time.Now()},
			{TaskID: "t3", State: StateWaitingForInput, StartedAt: time.Now()},
		},
		AttemptStates: []AttemptState{
			{AttemptID: "a1", TaskID: "t1", State: StateRunning, StartedAt: time.Now()},
			{AttemptID: "a2", TaskID: "t2", State: StateSucceeded, StartedAt: time.Now()},
			{AttemptID: "a3", TaskID: "t3", State: StateWaitingForInput, StartedAt: time.Now()},
			{AttemptID: "a4", TaskID: "t1", State: StateCancelRequested, StartedAt: time.Now()},
		},
	}
	count := l.ReconcileOnStartup()
	if count != 3 {
		t.Fatalf("expected 3 interrupted attempts, got %d", count)
	}
	if ts := l.TaskStateFor("t1"); ts.State != StateInterrupted {
		t.Fatalf("t1 should be interrupted, got %s", ts.State)
	}
	if ts := l.TaskStateFor("t2"); ts.State != StateSucceeded {
		t.Fatalf("t2 should remain succeeded, got %s", ts.State)
	}
	if ts := l.TaskStateFor("t3"); ts.State != StateInterrupted {
		t.Fatalf("t3 should be interrupted, got %s", ts.State)
	}
	for _, as := range l.AttemptStates {
		if as.AttemptID == "a2" {
			if as.State != StateSucceeded {
				t.Fatalf("a2 should remain succeeded, got %s", as.State)
			}
		} else {
			if as.State != StateInterrupted {
				t.Fatalf("%s should be interrupted, got %s", as.AttemptID, as.State)
			}
			if as.InterruptReason == "" {
				t.Fatalf("%s should have interrupt reason", as.AttemptID)
			}
		}
	}
}

func TestLedgerReconcileIdempotent(t *testing.T) {
	l := &Ledger{
		TaskStates:    []TaskState{{TaskID: "t1", State: StateInterrupted, StartedAt: time.Now()}},
		AttemptStates: []AttemptState{{AttemptID: "a1", TaskID: "t1", State: StateInterrupted, StartedAt: time.Now()}},
	}
	count := l.ReconcileOnStartup()
	if count != 0 {
		t.Fatalf("expected 0 newly interrupted on second reconcile, got %d", count)
	}
}

func TestLedgerClaimOwnership(t *testing.T) {
	l := &Ledger{
		AttemptStates: []AttemptState{{AttemptID: "a1", TaskID: "t1", State: StateInterrupted, StartedAt: time.Now()}},
	}
	gen := l.ClaimOwnership("a1", "pid:1")
	if gen != 1 {
		t.Fatalf("first claim should return gen 1, got %d", gen)
	}
	if !l.VerifyOwnership("a1", "pid:1", 1) {
		t.Fatal("pid:1 should own at gen 1")
	}
	gen2 := l.ClaimOwnership("a1", "pid:2")
	if gen2 != 2 {
		t.Fatalf("second claim should return gen 2, got %d", gen2)
	}
	if l.VerifyOwnership("a1", "pid:1", 1) {
		t.Fatal("pid:1 should no longer own after pid:2 claimed")
	}
	if !l.VerifyOwnership("a1", "pid:2", 2) {
		t.Fatal("pid:2 should own at gen 2")
	}
}

func TestLedgerClaimOwnershipTerminalRefused(t *testing.T) {
	l := &Ledger{
		AttemptStates: []AttemptState{{AttemptID: "a1", TaskID: "t1", State: StateSucceeded, StartedAt: time.Now()}},
	}
	gen := l.ClaimOwnership("a1", "pid:1")
	if gen != 0 {
		t.Fatalf("claim on terminal attempt should return 0, got %d", gen)
	}
}

func TestLedgerTransitionAttempt(t *testing.T) {
	l := &Ledger{
		AttemptStates: []AttemptState{{AttemptID: "a1", TaskID: "t1", State: StateRunning, StartedAt: time.Now()}},
	}
	if err := l.TransitionAttempt("a1", StateSucceeded); err != nil {
		t.Fatalf("legal transition failed: %v", err)
	}
	if as := l.AttemptStateFor("a1"); as.State != StateSucceeded || as.TerminalAt.IsZero() {
		t.Fatalf("expected succeeded with terminal_at, got state=%s terminal=%v", as.State, as.TerminalAt)
	}
	if err := l.TransitionAttempt("a1", StateRunning); err == nil {
		t.Fatal("transition from terminal should fail")
	}
}

func TestLedgerTransitionAttemptIllegal(t *testing.T) {
	l := &Ledger{
		AttemptStates: []AttemptState{{AttemptID: "a1", TaskID: "t1", State: StateAdmitted, StartedAt: time.Now()}},
	}
	if err := l.TransitionAttempt("a1", StateSucceeded); err == nil {
		t.Fatal("admitted → succeeded should be illegal")
	}
}

func TestLedgerTransitionTask(t *testing.T) {
	l := &Ledger{
		TaskStates:    []TaskState{{TaskID: "t1", State: StateRunning, StartedAt: time.Now()}},
		AttemptStates: []AttemptState{{AttemptID: "a1", TaskID: "t1", State: StateRunning, StartedAt: time.Now()}},
	}
	if err := l.TransitionTask("t1", StateSucceeded); err == nil {
		t.Fatal("task with non-terminal attempt should not go terminal")
	}
	l.AttemptStates[0].State = StateSucceeded
	if err := l.TransitionTask("t1", StateSucceeded); err != nil {
		t.Fatalf("task with all terminal attempts should go terminal: %v", err)
	}
}

func TestLedgerInterruptedAttempts(t *testing.T) {
	l := &Ledger{
		AttemptStates: []AttemptState{
			{AttemptID: "a1", TaskID: "t1", State: StateInterrupted, StartedAt: time.Now(), UpdatedAt: time.Now().Add(time.Second)},
			{AttemptID: "a2", TaskID: "t1", State: StateSucceeded, StartedAt: time.Now()},
			{AttemptID: "a3", TaskID: "t2", State: StateInterrupted, StartedAt: time.Now(), UpdatedAt: time.Now()},
		},
	}
	got := l.InterruptedAttempts()
	if len(got) != 2 {
		t.Fatalf("expected 2 interrupted, got %d", len(got))
	}
}

func TestLedgerLifecycleCoverage(t *testing.T) {
	l := &Ledger{
		TaskStates: []TaskState{
			{TaskID: "t1", State: StateRunning},
			{TaskID: "t2", State: StateSucceeded},
		},
		AttemptStates: []AttemptState{
			{AttemptID: "a1", TaskID: "t1", State: StateRunning},
			{AttemptID: "a2", TaskID: "t2", State: StateSucceeded},
			{AttemptID: "a3", TaskID: "t1", State: StateInterrupted},
		},
	}
	tasks, attempts, byState := l.LifecycleCoverage()
	if tasks != 2 {
		t.Fatalf("expected 2 tasks, got %d", tasks)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if byState[StateRunning] != 2 {
		t.Fatalf("expected 2 running, got %d", byState[StateRunning])
	}
	if byState[StateSucceeded] != 2 {
		t.Fatalf("expected 2 succeeded, got %d", byState[StateSucceeded])
	}
	if byState[StateInterrupted] != 1 {
		t.Fatalf("expected 1 interrupted, got %d", byState[StateInterrupted])
	}
}

func TestLifecycleMaxCap(t *testing.T) {
	l := &Ledger{TaskStates: []TaskState{}}
	for i := 0; i < maxLifecycle+10; i++ {
		l.RecordTaskState(TaskState{TaskID: string(rune('a'+i%26)) + string(rune('a'+i/26)), State: StateAdmitted, StartedAt: time.Now()})
	}
	if len(l.TaskStates) > maxLifecycle {
		t.Fatalf("expected at most %d task states, got %d", maxLifecycle, len(l.TaskStates))
	}
}

func TestResumeTaskCreatesNewAttemptWithCausalLink(t *testing.T) {
	l := &Ledger{
		TaskStates:    []TaskState{{TaskID: "t1", State: StateInterrupted, StartedAt: time.Now()}},
		AttemptStates: []AttemptState{{AttemptID: "a1", TaskID: "t1", State: StateInterrupted, Leg: LegClaude, StartedAt: time.Now(), UpdatedAt: time.Now()}},
	}
	newAttempt, err := l.ResumeTask("t1", "pid:1")
	if err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}
	if newAttempt.AttemptID == "a1" {
		t.Fatal("new attempt should have a different ID from the interrupted one")
	}
	if newAttempt.ParentAttempt != "a1" {
		t.Fatalf("new attempt should link to interrupted attempt a1, got %s", newAttempt.ParentAttempt)
	}
	if newAttempt.State != StateRunning {
		t.Fatalf("new attempt should be running, got %s", newAttempt.State)
	}
	if newAttempt.ProcessID != "pid:1" {
		t.Fatalf("new attempt should be owned by pid:1, got %s", newAttempt.ProcessID)
	}
	if newAttempt.Leg != LegClaude {
		t.Fatalf("new attempt should inherit leg from interrupted attempt, got %s", newAttempt.Leg)
	}
	old := l.AttemptStateFor("a1")
	if old.State != StateFailed {
		t.Fatalf("interrupted attempt should be transitioned to failed, got %s", old.State)
	}
	ts := l.TaskStateFor("t1")
	if ts.State != StateRunning {
		t.Fatalf("task should be running after resume, got %s", ts.State)
	}
}

func TestResumeTaskNoInterruptedAttempts(t *testing.T) {
	l := &Ledger{
		TaskStates:    []TaskState{{TaskID: "t1", State: StateRunning, StartedAt: time.Now()}},
		AttemptStates: []AttemptState{{AttemptID: "a1", TaskID: "t1", State: StateRunning, StartedAt: time.Now()}},
	}
	_, err := l.ResumeTask("t1", "pid:1")
	if err == nil {
		t.Fatal("should fail when task has no interrupted attempts")
	}
}

func TestResumeTaskTerminalTask(t *testing.T) {
	l := &Ledger{
		TaskStates:    []TaskState{{TaskID: "t1", State: StateSucceeded, StartedAt: time.Now()}},
		AttemptStates: []AttemptState{{AttemptID: "a1", TaskID: "t1", State: StateInterrupted, StartedAt: time.Now(), UpdatedAt: time.Now()}},
	}
	_, err := l.ResumeTask("t1", "pid:1")
	if err == nil {
		t.Fatal("should fail when task is terminal")
	}
}

func TestResumeTaskTaskNotFound(t *testing.T) {
	l := &Ledger{TaskStates: []TaskState{}}
	_, err := l.ResumeTask("nonexistent", "pid:1")
	if err == nil {
		t.Fatal("should fail when task does not exist")
	}
}

// TestCheckpointUpdatesPhaseAndPartialOutput verifies that Checkpoint stamps
// the phase and partial output on an attempt at a safe boundary (M3.3).
func TestCheckpointUpdatesPhaseAndPartialOutput(t *testing.T) {
	l := &Ledger{}
	l.RecordAttemptState(AttemptState{
		AttemptID: "att1", TaskID: "t1", State: StateRunning,
		Leg: LegClaude, ProcessID: "pid:1", StartedAt: time.Now(),
	})
	l.Checkpoint("att1", PhaseProducing, "partial answer text")
	as := l.AttemptStateFor("att1")
	if as.CheckpointPhase != PhaseProducing {
		t.Fatalf("expected phase %s, got %s", PhaseProducing, as.CheckpointPhase)
	}
	if as.PartialOutput != "partial answer text" {
		t.Fatalf("expected partial output, got %q", as.PartialOutput)
	}
	if as.CheckpointAt.IsZero() {
		t.Fatal("checkpoint timestamp should be set")
	}
}

// TestCheckpointNotFoundIsNoop verifies that checkpointing a nonexistent
// attempt is a no-op, not a panic.
func TestCheckpointNotFoundIsNoop(t *testing.T) {
	l := &Ledger{}
	l.Checkpoint("nonexistent", PhaseDispatching, "")
	// no panic = pass
}

// TestReconcileCheckpoints verifies that after a brain restart, the survival
// of worktree and diff artifacts is checked for each interrupted attempt
// (M3.3: persisting checkpoints beyond artifact refs).
func TestReconcileCheckpoints(t *testing.T) {
	l := &Ledger{}
	// Attempt with surviving worktree and diff
	dir := t.TempDir()
	diffPath := dir + "/test.diff"
	os.WriteFile(diffPath, []byte("diff"), 0o644)
	l.RecordAttemptState(AttemptState{
		AttemptID: "att_survived", TaskID: "t1", State: StateRunning,
		WorktreeDir: dir, DiffPath: diffPath,
		CheckpointPhase: PhaseProducing, PartialOutput: "some text",
		StartedAt: time.Now(),
	})
	// Attempt with missing worktree
	l.RecordAttemptState(AttemptState{
		AttemptID: "att_missing_wt", TaskID: "t2", State: StateRunning,
		WorktreeDir: "/nonexistent/path", DiffPath: "",
		CheckpointPhase: PhaseDispatching,
		StartedAt:       time.Now(),
	})
	// Terminal attempt — should not appear in reconciliation
	l.RecordAttemptState(AttemptState{
		AttemptID: "att_terminal", TaskID: "t3", State: StateSucceeded,
		StartedAt: time.Now(),
	})
	l.ReconcileOnStartup()
	statuses := l.ReconcileCheckpoints()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 checkpoint statuses, got %d", len(statuses))
	}
	var survived, missing CheckpointStatus
	for _, cs := range statuses {
		switch cs.AttemptID {
		case "att_survived":
			survived = cs
		case "att_missing_wt":
			missing = cs
		}
	}
	if !survived.WorktreeSurvived {
		t.Error("expected worktree to survive")
	}
	if !survived.DiffSurvived {
		t.Error("expected diff to survive")
	}
	if !survived.HasPartialText {
		t.Error("expected partial text")
	}
	if missing.WorktreeSurvived {
		t.Error("expected worktree to be missing")
	}
}

// TestFormatCheckpointStatus verifies the rendering includes survival facts.
func TestFormatCheckpointStatus(t *testing.T) {
	cs := CheckpointStatus{
		AttemptID:        "att1",
		Phase:            PhaseProducing,
		WorktreeSurvived: true,
		DiffSurvived:     false,
		HasPartialText:   true,
	}
	s := FormatCheckpointStatus(cs)
	if !strings.Contains(s, "worktree=ok") {
		t.Fatalf("expected worktree=ok in: %s", s)
	}
	if !strings.Contains(s, "diff=missing") {
		t.Fatalf("expected diff=missing in: %s", s)
	}
	if !strings.Contains(s, "partial-text=yes") {
		t.Fatalf("expected partial-text=yes in: %s", s)
	}
}
