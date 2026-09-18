package captaincode

import (
	"testing"
	"time"
)

func testLedgerWithTask(t *testing.T) *Ledger {
	t.Helper()
	l := &Ledger{}
	taskID := "task-test-1"
	ts := TaskState{
		Version:   LifecycleVersion,
		TaskID:    taskID,
		State:     StateSucceeded,
		StartedAt: time.Now().Add(-5 * time.Minute),
		UpdatedAt: time.Now(),
	}
	l.RecordTaskState(ts)
	attempts := []AttemptState{
		{
			Version:    LifecycleVersion,
			TaskID:     taskID,
			AttemptID:  "att-1",
			StageID:    "stage:1",
			State:      StateSucceeded,
			Leg:        "claude",
			StartedAt:  time.Now().Add(-5 * time.Minute),
			UpdatedAt:  time.Now(),
			TerminalAt: time.Now(),
		},
		{
			Version:    LifecycleVersion,
			TaskID:     taskID,
			AttemptID:  "att-2",
			StageID:    "stage:2",
			State:      StateFailed,
			Leg:        "grok",
			StartedAt:  time.Now().Add(-3 * time.Minute),
			UpdatedAt:  time.Now(),
			TerminalAt: time.Now(),
		},
	}
	for _, as := range attempts {
		l.RecordAttemptState(as)
	}
	l.RecordBudget(&Budget{
		Version:         BudgetVersion,
		TaskID:          taskID,
		MaxAttempts:     10,
		SettledAttempts: 2,
		SettledCostUSD:  0.15,
		StartedAt:       time.Now().Add(-5 * time.Minute),
	})
	l.Charges = []Charge{
		{Version: AccountingVersion, ID: "c-1", TaskID: taskID, Kind: KindCall, Leg: "claude", Label: "worker", Usage: Usage{Status: UsageMeasured, Total: 500, CostUSD: 0.10, CostStatus: UsageMeasured}},
		{Version: AccountingVersion, ID: "c-2", TaskID: taskID, Kind: KindCall, Leg: "grok", Label: "worker", Usage: Usage{Status: UsageUnknown}},
	}
	return l
}

func TestBuildHandoffBriefBasic(t *testing.T) {
	l := testLedgerWithTask(t)
	taskID := "task-test-1"
	brief := BuildHandoffBrief(l, taskID, "fix the bug in auth.go", nil)
	if brief.TaskID != taskID {
		t.Fatalf("expected task_id %s, got %s", taskID, brief.TaskID)
	}
	if brief.State != StateSucceeded {
		t.Fatalf("expected state %s, got %s", StateSucceeded, brief.State)
	}
	if brief.Requirements != "fix the bug in auth.go" {
		t.Fatalf("expected requirements, got %q", brief.Requirements)
	}
	if len(brief.Work) != 2 {
		t.Fatalf("expected 2 work entries, got %d", len(brief.Work))
	}
	if brief.Work[0].Leg != "claude" {
		t.Fatalf("expected first work leg claude, got %s", brief.Work[0].Leg)
	}
	if brief.Work[1].State != StateFailed {
		t.Fatalf("expected second work state failed, got %s", brief.Work[1].State)
	}
}

func TestBuildHandoffBriefBudget(t *testing.T) {
	l := testLedgerWithTask(t)
	brief := BuildHandoffBrief(l, "task-test-1", "test", nil)
	if brief.Budget == nil {
		t.Fatal("expected budget summary")
	}
	if brief.Budget.AttemptsUsed != 2 {
		t.Fatalf("expected 2 attempts used, got %d", brief.Budget.AttemptsUsed)
	}
	if brief.Budget.AttemptsMax != 10 {
		t.Fatalf("expected max 10, got %d", brief.Budget.AttemptsMax)
	}
	if brief.Budget.UnknownUsage != 1 {
		t.Fatalf("expected 1 unknown usage call, got %d", brief.Budget.UnknownUsage)
	}
}

func TestBuildHandoffBriefUncertainSideEffects(t *testing.T) {
	l := testLedgerWithTask(t)
	brief := BuildHandoffBrief(l, "task-test-1", "test", nil)
	if len(brief.UncertainSideEffects) == 0 {
		t.Fatal("expected uncertain side effects for unknown usage call")
	}
	found := false
	for _, u := range brief.UncertainSideEffects {
		if contains(u, "unknown usage") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("uncertain side effects do not mention unknown usage: %v", brief.UncertainSideEffects)
	}
}

func TestBuildHandoffBriefWithIntegration(t *testing.T) {
	l := testLedgerWithTask(t)
	manifest := PatchManifest{
		Version:      ArtifactVersion,
		TaskID:       "task-test-1",
		AttemptID:    "att-1",
		Leg:          "claude",
		ChangedFiles: []string{"auth.go", "auth_test.go"},
		DiffDigest:   "abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
	}
	manifest.RecordCheck([]string{"go", "test", "./..."}, 0, true, "PASS")
	ic := BuildIntegrationCandidate("task-test-1", "stage:1", "rev-1", []PatchManifest{manifest})
	brief := BuildHandoffBrief(l, "task-test-1", "fix auth", &ic)
	if brief.Integration == nil {
		t.Fatal("expected integration summary")
	}
	if brief.Integration.Status != IntegrationClean {
		t.Fatalf("expected clean integration, got %s", brief.Integration.Status)
	}
	if len(brief.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(brief.Artifacts))
	}
	if !brief.Artifacts[0].CheckPassed {
		t.Fatal("expected check passed")
	}
	if len(brief.FailedChecks) != 0 {
		t.Fatalf("expected 0 failed checks, got %d", len(brief.FailedChecks))
	}
}

func TestBuildHandoffBriefFailedChecks(t *testing.T) {
	l := testLedgerWithTask(t)
	manifest := PatchManifest{
		Version:      ArtifactVersion,
		TaskID:       "task-test-1",
		AttemptID:    "att-2",
		Leg:          "grok",
		ChangedFiles: []string{"server.go"},
		DiffDigest:   "def456def456def456def456def456def456def456def456def456def456def456def4",
	}
	manifest.RecordCheck([]string{"go", "test", "./..."}, 1, false, "FAIL: server_test.go:12")
	ic := BuildIntegrationCandidate("task-test-1", "stage:1", "rev-1", []PatchManifest{manifest})
	brief := BuildHandoffBrief(l, "task-test-1", "fix server", &ic)
	if len(brief.FailedChecks) != 1 {
		t.Fatalf("expected 1 failed check, got %d", len(brief.FailedChecks))
	}
	if brief.FailedChecks[0].ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", brief.FailedChecks[0].ExitCode)
	}
	if brief.Artifacts[0].CheckPassed {
		t.Fatal("expected check failed")
	}
}

func TestBuildHandoffBriefRemainingActions(t *testing.T) {
	l := &Ledger{}
	taskID := "task-interrupted"
	l.RecordTaskState(TaskState{
		Version:   LifecycleVersion,
		TaskID:    taskID,
		State:     StateInterrupted,
		StartedAt: time.Now().Add(-10 * time.Minute),
	})
	l.RecordAttemptState(AttemptState{
		Version:     LifecycleVersion,
		TaskID:      taskID,
		AttemptID:   "att-x",
		State:       StateInterrupted,
		Leg:         "codex",
		WorktreeDir: "/tmp/wt-att-x",
		StartedAt:   time.Now().Add(-10 * time.Minute),
		UpdatedAt:   time.Now(),
	})
	brief := BuildHandoffBrief(l, taskID, "task with interrupted attempt", nil)
	if len(brief.RemainingActions) == 0 {
		t.Fatal("expected remaining actions for interrupted task")
	}
	found := false
	for _, a := range brief.RemainingActions {
		if contains(a, "interrupted") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("remaining actions do not mention interrupted: %v", brief.RemainingActions)
	}
	if len(brief.UncertainSideEffects) == 0 {
		t.Fatal("expected uncertain side effects for interrupted attempt")
	}
}

func TestRecordAndRetrieveHandoff(t *testing.T) {
	l := &Ledger{}
	brief := HandoffBrief{
		Version:      HandoffVersion,
		TaskID:       "task-store",
		Requirements: "test task",
		State:        StateSucceeded,
		CreatedAt:    time.Now(),
	}
	l.RecordHandoff(brief)
	got := l.HandoffFor("task-store")
	if got == nil {
		t.Fatal("expected to retrieve stored handoff")
	}
	if got.Requirements != "test task" {
		t.Fatalf("expected requirements 'test task', got %q", got.Requirements)
	}
	brief2 := brief
	brief2.State = StateFailed
	l.RecordHandoff(brief2)
	got2 := l.HandoffFor("task-store")
	if got2.State != StateFailed {
		t.Fatalf("expected updated state %s, got %s", StateFailed, got2.State)
	}
}

func TestHandoffCoverage(t *testing.T) {
	l := &Ledger{}
	l.RecordHandoff(HandoffBrief{Version: HandoffVersion, TaskID: "t1", State: StateSucceeded})
	l.RecordHandoff(HandoffBrief{Version: HandoffVersion, TaskID: "t2", State: StateFailed})
	l.RecordHandoff(HandoffBrief{Version: HandoffVersion, TaskID: "t3", State: StateSucceeded})
	briefs, byState := l.HandoffCoverage()
	if briefs != 3 {
		t.Fatalf("expected 3 briefs, got %d", briefs)
	}
	if byState[StateSucceeded] != 2 {
		t.Fatalf("expected 2 succeeded, got %d", byState[StateSucceeded])
	}
	if byState[StateFailed] != 1 {
		t.Fatalf("expected 1 failed, got %d", byState[StateFailed])
	}
}

func TestFormatHandoffBrief(t *testing.T) {
	l := testLedgerWithTask(t)
	brief := BuildHandoffBrief(l, "task-test-1", "fix the bug", nil)
	out := FormatHandoffBrief(brief)
	if !contains(out, "task-test-1") {
		t.Fatal("formatted brief missing task ID")
	}
	if !contains(out, "succeeded") {
		t.Fatal("formatted brief missing state")
	}
	if !contains(out, "fix the bug") {
		t.Fatal("formatted brief missing requirements")
	}
	if !contains(out, "work:") {
		t.Fatal("formatted brief missing work section")
	}
	if !contains(out, "budget:") {
		t.Fatal("formatted brief missing budget section")
	}
}

func TestFormatHandoffBriefFailedChecks(t *testing.T) {
	l := testLedgerWithTask(t)
	manifest := PatchManifest{
		Version:      ArtifactVersion,
		TaskID:       "task-test-1",
		AttemptID:    "att-2",
		Leg:          "grok",
		ChangedFiles: []string{"server.go"},
	}
	manifest.RecordCheck([]string{"go", "test"}, 1, false, "FAIL")
	ic := BuildIntegrationCandidate("task-test-1", "stage:1", "rev-1", []PatchManifest{manifest})
	brief := BuildHandoffBrief(l, "task-test-1", "fix", &ic)
	out := FormatHandoffBrief(brief)
	if !contains(out, "failed checks:") {
		t.Fatal("formatted brief missing failed checks section")
	}
	if !contains(out, "exit 1") {
		t.Fatal("formatted brief missing exit code")
	}
}

func TestBuildHandoffBriefSoloArtifact(t *testing.T) {
	l := &Ledger{}
	taskID := "task-solo-art"
	l.RecordTaskState(TaskState{
		Version: LifecycleVersion, TaskID: taskID, State: StateSucceeded,
		StartedAt: time.Now().Add(-3 * time.Minute),
	})
	l.RecordAttemptState(AttemptState{
		Version:      LifecycleVersion,
		TaskID:       taskID,
		AttemptID:    "att-solo",
		State:        StateSucceeded,
		Leg:          "claude",
		StartedAt:    time.Now().Add(-3 * time.Minute),
		TerminalAt:   time.Now(),
		ChangedFiles: []string{"auth.go", "auth_test.go"},
		DiffDigest:   "abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
		DiffPath:     "/tmp/solo-claude.abc123def456.diff",
	})
	brief := BuildHandoffBrief(l, taskID, "fix the auth bug", nil)
	if len(brief.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact from solo turn, got %d", len(brief.Artifacts))
	}
	a := brief.Artifacts[0]
	if a.Leg != "claude" {
		t.Fatalf("expected leg claude, got %s", a.Leg)
	}
	if len(a.ChangedFiles) != 2 {
		t.Fatalf("expected 2 changed files, got %v", a.ChangedFiles)
	}
	if a.DiffDigest == "" {
		t.Fatal("expected non-empty diff digest")
	}
	if a.DiffPath != "/tmp/solo-claude.abc123def456.diff" {
		t.Fatalf("expected diff path, got %s", a.DiffPath)
	}
}

func TestBuildHandoffBriefSoloFailedChecksFromOutcome(t *testing.T) {
	l := &Ledger{}
	taskID := "task-solo-fail"
	l.RecordTaskState(TaskState{
		Version: LifecycleVersion, TaskID: taskID, State: StateSucceeded,
		StartedAt: time.Now().Add(-2 * time.Minute),
	})
	l.RecordAttemptState(AttemptState{
		Version:    LifecycleVersion,
		TaskID:     taskID,
		AttemptID:  "att-solo-fail",
		State:      StateSucceeded,
		Leg:        "glm",
		StartedAt:  time.Now().Add(-2 * time.Minute),
		TerminalAt: time.Now(),
	})
	l.RecordOutcome(OutcomeEvidence{
		TaskID: taskID,
		Status: AcceptancePending,
	})
	l.RecordCheckResult(taskID, CheckResult{
		Command:  "director:assess",
		ExitCode: 1,
		Passed:   false,
		Source:   "solo",
		At:       time.Now(),
	})
	brief := BuildHandoffBrief(l, taskID, "fix the thing", nil)
	if len(brief.FailedChecks) != 1 {
		t.Fatalf("expected 1 failed check from solo outcome, got %d", len(brief.FailedChecks))
	}
	if brief.FailedChecks[0].ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", brief.FailedChecks[0].ExitCode)
	}
}

func TestBuildHandoffBriefSoloNoArtifact(t *testing.T) {
	l := &Ledger{}
	taskID := "task-solo-text-only"
	l.RecordTaskState(TaskState{
		Version: LifecycleVersion, TaskID: taskID, State: StateSucceeded,
		StartedAt: time.Now().Add(-1 * time.Minute),
	})
	l.RecordAttemptState(AttemptState{
		Version:    LifecycleVersion,
		TaskID:     taskID,
		AttemptID:  "att-text",
		State:      StateSucceeded,
		Leg:        "grok",
		StartedAt:  time.Now().Add(-1 * time.Minute),
		TerminalAt: time.Now(),
	})
	brief := BuildHandoffBrief(l, taskID, "answer a question", nil)
	if len(brief.Artifacts) != 0 {
		t.Fatalf("expected 0 artifacts for text-only solo turn, got %d", len(brief.Artifacts))
	}
}
