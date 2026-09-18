package captaincode

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCheckCompatibility(t *testing.T) {
	if e := CheckCompatibility(TaskAPIVersion); e != nil {
		t.Fatalf("current version %d should be compatible: %v", TaskAPIVersion, e)
	}
	if e := CheckCompatibility(TaskAPIMinVersion); e != nil {
		t.Fatalf("min version %d should be compatible: %v", TaskAPIMinVersion, e)
	}
	if e := CheckCompatibility(TaskAPIVersion + 1); e == nil {
		t.Fatal("future version should be rejected")
	}
	if e := CheckCompatibility(TaskAPIMinVersion - 1); e == nil {
		t.Fatal("below min version should be rejected")
	}
	if e := CheckCompatibility(0); e == nil {
		t.Fatal("version 0 should be rejected")
	}
	if e := CheckCompatibility(TaskAPIVersion + 1); e.Code != ErrUnsupportedVersion {
		t.Fatalf("expected %s, got %s", ErrUnsupportedVersion, e.Code)
	}
}

func TestTaskErrorImplementsError(t *testing.T) {
	e := NewTaskError(ErrNotFound, "task missing")
	if e.Error() != "not_found: task missing" {
		t.Fatalf("unexpected error string: %s", e.Error())
	}
}

func TestValidateLineage(t *testing.T) {
	if e := ValidateLineage(nil, "task-1"); e != nil {
		t.Fatalf("nil lineage should pass: %v", e)
	}
	lineage := &DelegationLineage{ParentTaskID: "task-parent", Depth: 1}
	if e := ValidateLineage(lineage, "task-1"); e != nil {
		t.Fatalf("depth 1 should pass: %v", e)
	}
	deep := &DelegationLineage{Depth: MaxDelegationDepth + 1}
	if e := ValidateLineage(deep, "task-1"); e == nil {
		t.Fatal("excessive depth should be rejected")
	}
	if e := ValidateLineage(deep, "task-1"); e.Code != ErrForbidden {
		t.Fatalf("expected forbidden, got %s", e.Code)
	}
	cyclic := &DelegationLineage{
		ParentTaskID: "task-parent",
		Depth:        2,
		Ancestors:    []string{"task-a", "task-1", "task-b"},
	}
	if e := ValidateLineage(cyclic, "task-1"); e == nil {
		t.Fatal("cyclic lineage should be rejected")
	}
	if e := ValidateLineage(cyclic, "task-1"); e.Code != ErrForbidden {
		t.Fatalf("expected forbidden for cycle, got %s", e.Code)
	}
}

func TestIsDuplicateSubmit(t *testing.T) {
	a := SubmitRequest{Prompt: "fix the bug", Leg: "claude"}
	b := a
	if !IsDuplicateSubmit(a, b) {
		t.Fatal("identical requests should be duplicates")
	}
	b.Prompt = "different"
	if IsDuplicateSubmit(a, b) {
		t.Fatal("different prompts should not be duplicates")
	}
	b = a
	b.Leg = "grok"
	if IsDuplicateSubmit(a, b) {
		t.Fatal("different legs should not be duplicates")
	}
}

func TestNewRequestID(t *testing.T) {
	id1 := NewRequestID()
	id2 := NewRequestID()
	if id1 == id2 {
		t.Fatal("request IDs should be unique")
	}
	if len(id1) < 10 {
		t.Fatalf("request ID too short: %s", id1)
	}
}

func TestNewTaskID(t *testing.T) {
	id1 := NewTaskID()
	id2 := NewTaskID()
	if id1 == id2 {
		t.Fatal("task IDs should be unique")
	}
}

func TestEncodeDecodeCursor(t *testing.T) {
	if DecodeCursor("") != 0 {
		t.Fatal("empty cursor should decode to 0")
	}
	for _, seq := range []int64{1, 42, 1000, 999999} {
		c := EncodeCursor(seq)
		got := DecodeCursor(c)
		if got != seq {
			t.Fatalf("cursor round-trip failed: encoded %s from %d, decoded %d", c, seq, got)
		}
	}
}

func TestTaskRequestEnvelopeJSON(t *testing.T) {
	body, _ := json.Marshal(PlanRequest{Prompt: "fix auth", ProjectRoot: "/tmp/repo"})
	req := TaskRequest{
		Version:   TaskAPIVersion,
		Op:        OpPlan,
		RequestID: NewRequestID(),
		Body:      body,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var got TaskRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if got.Version != TaskAPIVersion {
		t.Fatalf("version mismatch: %d", got.Version)
	}
	if got.Op != OpPlan {
		t.Fatalf("op mismatch: %s", got.Op)
	}
	var planBody PlanRequest
	if err := json.Unmarshal(got.Body, &planBody); err != nil {
		t.Fatalf("unmarshal plan body: %v", err)
	}
	if planBody.Prompt != "fix auth" {
		t.Fatalf("prompt mismatch: %s", planBody.Prompt)
	}
}

func TestTaskResponseEnvelopeJSON(t *testing.T) {
	resp := TaskResponse{
		Version:   TaskAPIVersion,
		Status:    ResponseOK,
		Op:        OpSubmit,
		TaskID:    "task-abc",
		RequestID: "req-123",
	}
	body, _ := json.Marshal(SubmitResponse{TaskID: "task-abc", State: StateAdmitted})
	resp.Body = body
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var got TaskResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Status != ResponseOK {
		t.Fatalf("status mismatch: %s", got.Status)
	}
	if got.TaskID != "task-abc" {
		t.Fatalf("task ID mismatch: %s", got.TaskID)
	}
	var submitBody SubmitResponse
	if err := json.Unmarshal(got.Body, &submitBody); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}
	if submitBody.State != StateAdmitted {
		t.Fatalf("state mismatch: %s", submitBody.State)
	}
}

func TestTaskResponseErrorEnvelope(t *testing.T) {
	resp := TaskResponse{
		Version: TaskAPIVersion,
		Status:  ResponseError,
		Op:      OpSubmit,
		Error:   NewTaskError(ErrConflict, "prompt differs from existing task"),
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	var got TaskResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if got.Error == nil {
		t.Fatal("error should be present")
	}
	if got.Error.Code != ErrConflict {
		t.Fatalf("error code mismatch: %s", got.Error.Code)
	}
	if got.Status != ResponseError {
		t.Fatalf("status should be error: %s", got.Status)
	}
}

func TestEventBatchCursor(t *testing.T) {
	events := make([]TaskEvent, 0, 25)
	base := time.Now()
	for i := 1; i <= 25; i++ {
		events = append(events, TaskEvent{
			Seq:  int64(i),
			At:   base.Add(time.Duration(i) * time.Second),
			Kind: EventAttemptStart,
		})
	}
	limit := 10
	cursor := ""
	var got []TaskEvent
	for {
		startSeq := DecodeCursor(cursor)
		end := startSeq + int64(limit)
		if end > int64(len(events)) {
			end = int64(len(events))
		}
		batch := events[startSeq:end]
		got = append(got, batch...)
		hasMore := end < int64(len(events))
		if !hasMore {
			break
		}
		cursor = EncodeCursor(end)
	}
	if len(got) != 25 {
		t.Fatalf("expected 25 events across batches, got %d", len(got))
	}
	for i, e := range got {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, expected %d", i, e.Seq, i+1)
		}
	}
}

func TestPlanRequestRationale(t *testing.T) {
	req := PlanRequest{
		Prompt:      "add a login form",
		ProjectRoot: "/home/user/repo",
		Intent:      TaskIntent{Class: ClassMedium, Domain: DomainCode},
		Caps:        Requirements{Require: []Capability{CapTools}},
		Permissions: PermissionPolicy{Strict: true, Required: Requirements{Require: []Capability{CapTools, CapPermissions}}},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PlanRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Prompt != "add a login form" {
		t.Fatalf("prompt: %s", got.Prompt)
	}
	if !got.Permissions.Strict {
		t.Fatal("strict should be true")
	}
	if got.Intent.Class != ClassMedium {
		t.Fatalf("class: %s", got.Intent.Class)
	}
}

func TestResourceLimitsRoundTrip(t *testing.T) {
	limits := ResourceLimits{MaxAttempts: 12, MaxCostUSD: 5.0, MaxWallMs: 600000}
	data, _ := json.Marshal(limits)
	var got ResourceLimits
	json.Unmarshal(data, &got)
	if got.MaxAttempts != 12 {
		t.Fatalf("max attempts: %d", got.MaxAttempts)
	}
	if got.MaxCostUSD != 5.0 {
		t.Fatalf("max cost: %f", got.MaxCostUSD)
	}
	if got.MaxWallMs != 600000 {
		t.Fatalf("max wall: %d", got.MaxWallMs)
	}
}

func TestDelegationLineageAncestors(t *testing.T) {
	lineage := &DelegationLineage{
		ParentTaskID: "task-root",
		Depth:        2,
		Ancestors:    []string{"task-root", "task-mid"},
	}
	if e := ValidateLineage(lineage, "task-leaf"); e != nil {
		t.Fatalf("valid lineage rejected: %v", e)
	}
	lineage.Ancestors = append(lineage.Ancestors, "task-leaf")
	if e := ValidateLineage(lineage, "task-leaf"); e == nil {
		t.Fatal("self-referencing ancestry should be rejected")
	}
}

func TestAcceptanceChecksRoundTrip(t *testing.T) {
	checks := AcceptanceChecks{
		Command:       []string{"go", "test", "./..."},
		MustChange:    []string{"auth.go"},
		MustNotChange: []string{"go.mod"},
		ReviewBlinded: true,
	}
	data, _ := json.Marshal(checks)
	var got AcceptanceChecks
	json.Unmarshal(data, &got)
	if len(got.Command) != 3 || got.Command[0] != "go" {
		t.Fatalf("command round-trip failed: %v", got.Command)
	}
	if !got.ReviewBlinded {
		t.Fatal("review_blinded should be true")
	}
	if len(got.MustChange) != 1 || got.MustChange[0] != "auth.go" {
		t.Fatalf("must_change round-trip failed: %v", got.MustChange)
	}
}
