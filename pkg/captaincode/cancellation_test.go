package captaincode

import (
	"context"
	"testing"
	"time"
)

func TestCancelTreeRegisterAndCancel(t *testing.T) {
	tree := NewCancelTree()
	ctx1, cancel1 := tree.Register("task-1", "worker", "worker:claude", context.Background())
	defer cancel1()
	ctx2, cancel2 := tree.Register("task-1", "gate", "gate", context.Background())
	defer cancel2()

	active := tree.Active("task-1")
	if len(active) != 2 {
		t.Fatalf("expected 2 active, got %d: %v", len(active), active)
	}

	cancelled := tree.Cancel("task-1")
	if len(cancelled) != 2 {
		t.Fatalf("expected 2 cancelled, got %d", len(cancelled))
	}

	select {
	case <-ctx1.Done():
	default:
		t.Fatal("ctx1 should be cancelled")
	}
	select {
	case <-ctx2.Done():
	default:
		t.Fatal("ctx2 should be cancelled")
	}
}

func TestCancelTreeCascadesToChildren(t *testing.T) {
	tree := NewCancelTree()
	ctxParent, cancelParent := tree.Register("root", "workflow", "workflow", context.Background())
	defer cancelParent()
	ctxChild, cancelChild := tree.RegisterChild("root", "stage-1", "worker", "worker:grok", context.Background())
	defer cancelChild()
	ctxGrandchild, cancelGrandchild := tree.RegisterChild("stage-1", "worker-1", "call", "call:grok", context.Background())
	defer cancelGrandchild()

	cancelled := tree.Cancel("root")
	if len(cancelled) != 3 {
		t.Fatalf("expected 3 cancelled (root + child + grandchild), got %d: %v", len(cancelled), cancelled)
	}

	for _, ctx := range []context.Context{ctxParent, ctxChild, ctxGrandchild} {
		select {
		case <-ctx.Done():
		default:
			t.Fatal("context should be cancelled")
		}
	}
}

func TestCancelTreeUnregisterWithoutCancelling(t *testing.T) {
	tree := NewCancelTree()
	ctx, cancel := tree.Register("task-1", "worker", "worker:claude", context.Background())
	defer cancel()

	tree.Unregister("task-1", "worker")

	if len(tree.Active("task-1")) != 0 {
		t.Fatal("should have no active after unregister")
	}

	select {
	case <-ctx.Done():
		t.Fatal("ctx should NOT be cancelled by unregister")
	default:
	}
}

func TestCancelTreeCancelAll(t *testing.T) {
	tree := NewCancelTree()
	ctx1, c1 := tree.Register("t1", "w", "w:1", context.Background())
	defer c1()
	ctx2, c2 := tree.Register("t2", "w", "w:2", context.Background())
	defer c2()

	tree.CancelAll()

	for _, ctx := range []context.Context{ctx1, ctx2} {
		select {
		case <-ctx.Done():
		default:
			t.Fatal("ctx should be cancelled")
		}
	}
	if len(tree.TaskIDs()) != 0 {
		t.Fatal("tree should be empty after CancelAll")
	}
}

func TestCancelTreeCancelIdleTask(t *testing.T) {
	tree := NewCancelTree()
	cancelled := tree.Cancel("nonexistent")
	if len(cancelled) != 0 {
		t.Fatalf("cancelling a task with no work should return empty, got %v", cancelled)
	}
}

func TestResumePolicyDecide(t *testing.T) {
	policy := DefaultResumePolicy()
	now := time.Now()

	interrupted := AttemptState{
		State:     StateInterrupted,
		UpdatedAt: now.Add(-1 * time.Hour),
	}
	if d := policy.Decide(interrupted, now); d != ResumeReview {
		t.Fatalf("recent interrupted should be review, got %s", d)
	}

	stale := AttemptState{
		State:     StateInterrupted,
		UpdatedAt: now.Add(-48 * time.Hour),
	}
	if d := policy.Decide(stale, now); d != ResumeAbandon {
		t.Fatalf("stale interrupted should be abandon, got %s", d)
	}

	running := AttemptState{
		State:     StateRunning,
		UpdatedAt: now,
	}
	if d := policy.Decide(running, now); d != ResumeSkip {
		t.Fatalf("non-interrupted should be skip, got %s", d)
	}
}

func TestEvaluateInterrupted(t *testing.T) {
	now := time.Now()
	l := &Ledger{
		AttemptStates: []AttemptState{
			{AttemptID: "a1", TaskID: "t1", State: StateInterrupted, Leg: LegClaude, UpdatedAt: now.Add(-1 * time.Hour)},
			{AttemptID: "a2", TaskID: "t2", State: StateInterrupted, Leg: LegGrok, UpdatedAt: now.Add(-48 * time.Hour)},
			{AttemptID: "a3", TaskID: "t3", State: StateSucceeded, Leg: LegCodex, UpdatedAt: now},
		},
	}
	report := EvaluateInterrupted(l, DefaultResumePolicy(), now)
	if len(report.Review) != 1 {
		t.Fatalf("expected 1 review, got %d", len(report.Review))
	}
	if report.Review[0].AttemptID != "a1" {
		t.Fatalf("expected a1 in review, got %s", report.Review[0].AttemptID)
	}
	if len(report.Abandon) != 1 {
		t.Fatalf("expected 1 abandon, got %d", len(report.Abandon))
	}
	if report.Abandon[0].AttemptID != "a2" {
		t.Fatalf("expected a2 in abandon, got %s", report.Abandon[0].AttemptID)
	}
}

func TestFormatResumeReportEmpty(t *testing.T) {
	if s := FormatResumeReport(ResumeReport{}); s != "" {
		t.Fatalf("empty report should be empty string, got %q", s)
	}
}

func TestFormatResumeReportWithReview(t *testing.T) {
	now := time.Now()
	r := ResumeReport{
		Review: []AttemptState{
			{AttemptID: "a1", TaskID: "t1", Leg: LegClaude, UpdatedAt: now.Add(-1 * time.Hour)},
		},
	}
	s := FormatResumeReport(r)
	if s == "" {
		t.Fatal("should not be empty")
	}
	if !contains(s, "a1") {
		t.Fatalf("should mention a1: %s", s)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ── M3.4 adapter-specific cancellation deadline tests ──

func TestDefaultCancelDeadlines(t *testing.T) {
	dl := DefaultCancelDeadlines()
	if dl[LegClaude].StopTimeout != 10*time.Second {
		t.Fatalf("claude stop timeout = %v, want 10s", dl[LegClaude].StopTimeout)
	}
	if dl[LegCodex].AckTimeout != time.Second {
		t.Fatalf("codex ack timeout = %v, want 1s", dl[LegCodex].AckTimeout)
	}
}

func TestDeadlineForFallback(t *testing.T) {
	dl := DefaultCancelDeadlines()
	// An unknown leg gets the default.
	d := DeadlineFor(LegKimi, dl)
	if d.StopTimeout != 10*time.Second {
		t.Fatalf("default stop timeout = %v, want 10s", d.StopTimeout)
	}
	// A known leg gets its specific deadline.
	d = DeadlineFor(LegCodexCLI, dl)
	if d.StopTimeout != 5*time.Second {
		t.Fatalf("codex-cli stop timeout = %v, want 5s", d.StopTimeout)
	}
	// Nil overrides → default.
	d = DeadlineFor(LegClaude, nil)
	if d.StopTimeout != 10*time.Second {
		t.Fatalf("default stop timeout = %v, want 10s", d.StopTimeout)
	}
}

func TestCancelWithDeadlineFiresAndReturns(t *testing.T) {
	tree := NewCancelTree()
	_, cancel1 := tree.Register("task-dl", "w1", "worker:claude", context.Background())
	defer cancel1()
	_, cancel2 := tree.Register("task-dl", "w2", "gate", context.Background())
	defer cancel2()

	dl := CancelDeadline{AckTimeout: 100 * time.Millisecond, StopTimeout: time.Second}
	result := tree.CancelWithDeadline("task-dl", dl)
	if len(result.Cancelled) != 2 {
		t.Fatalf("expected 2 cancelled, got %d: %v", len(result.Cancelled), result.Cancelled)
	}
	if len(result.Pending) != 0 {
		t.Fatalf("expected 0 pending, got %d: %v", len(result.Pending), result.Pending)
	}
}

func TestCancelWithDeadlineCascadesToChildren(t *testing.T) {
	tree := NewCancelTree()
	_, cancelP := tree.Register("root-dl", "wf", "workflow", context.Background())
	defer cancelP()
	_, cancelC := tree.RegisterChild("root-dl", "stage-dl", "w", "worker:grok", context.Background())
	defer cancelC()

	dl := CancelDeadline{AckTimeout: 100 * time.Millisecond, StopTimeout: time.Second}
	result := tree.CancelWithDeadline("root-dl", dl)
	if len(result.Cancelled) != 2 {
		t.Fatalf("expected 2 cancelled (root + child), got %d: %v", len(result.Cancelled), result.Cancelled)
	}
}

func TestCancelWithDeadlineIdleTask(t *testing.T) {
	tree := NewCancelTree()
	dl := CancelDeadline{AckTimeout: 100 * time.Millisecond, StopTimeout: time.Second}
	result := tree.CancelWithDeadline("nonexistent", dl)
	if len(result.Cancelled) != 0 {
		t.Fatalf("idle task should return 0 cancelled, got %d", len(result.Cancelled))
	}
}
