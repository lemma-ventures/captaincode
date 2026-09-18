package captaincode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// testTaskServer builds a minimal test server that speaks the task API
// envelope. It is enough to exercise the client and the host adapter
// lifecycle without a real brain.
func testTaskServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/task", func(w http.ResponseWriter, r *http.Request) {
		var req TaskRequest
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Op {
		case OpPlan:
			body, _ := json.Marshal(PlanResponse{
				Leg:       "claude",
				Class:     "medium",
				Rationale: "test plan",
			})
			writeTestTaskOK(w, req, "", body)
		case OpSubmit:
			taskID := req.TaskID
			if taskID == "" {
				taskID = "task-test-0001"
			}
			body, _ := json.Marshal(SubmitResponse{
				TaskID: taskID,
				State:  StateAdmitted,
			})
			writeTestTaskOK(w, req, taskID, body)
		case OpInspect:
			taskID := req.TaskID
			var bodyReq InspectRequest
			if len(req.Body) > 0 {
				json.Unmarshal(req.Body, &bodyReq)
			}
			resp := InspectResponse{
				Task: &TaskState{
					TaskID:    taskID,
					State:     StateSucceeded,
					StartedAt: time.Now(),
				},
			}
			if bodyReq.IncludeHandoff {
				resp.Handoff = &HandoffBrief{
					TaskID:  taskID,
					State:   StateSucceeded,
					Version: 1,
				}
			}
			body, _ := json.Marshal(resp)
			writeTestTaskOK(w, req, taskID, body)
		case OpEvents:
			body, _ := json.Marshal(EventsResponse{
				Events: []TaskEvent{
					{Seq: 1, Kind: EventTaskAdmitted, Detail: "admitted"},
					{Seq: 2, Kind: EventTaskRunning, Detail: "running"},
					{Seq: 3, Kind: EventTaskTerminal, Detail: "succeeded"},
				},
			})
			writeTestTaskOK(w, req, req.TaskID, body)
		case OpArtifacts:
			body, _ := json.Marshal(ArtifactsResponse{})
			writeTestTaskOK(w, req, req.TaskID, body)
		case OpCancel:
			body, _ := json.Marshal(CancelResponse{
				TaskID:    req.TaskID,
				Cancelled: []string{req.TaskID},
				State:     StateCancelled,
			})
			writeTestTaskOK(w, req, req.TaskID, body)
		case OpResume:
			body, _ := json.Marshal(ResumeResponse{
				TaskID:    req.TaskID,
				AttemptID: "att-new",
				State:     StateRunning,
			})
			writeTestTaskOK(w, req, req.TaskID, body)
		default:
			writeTestTaskError(w, ErrBadRequest, "unknown op")
		}
	})
	return httptest.NewServer(mux)
}

func writeTestTaskOK(w http.ResponseWriter, req TaskRequest, taskID string, body json.RawMessage) {
	json.NewEncoder(w).Encode(TaskResponse{
		Version:   TaskAPIVersion,
		Status:    ResponseOK,
		Op:        req.Op,
		TaskID:    taskID,
		RequestID: req.RequestID,
		Body:      body,
	})
}

func writeTestTaskError(w http.ResponseWriter, code ErrCode, msg string) {
	w.WriteHeader(400)
	json.NewEncoder(w).Encode(TaskResponse{
		Version: TaskAPIVersion,
		Status:  ResponseError,
		Error:   NewTaskError(code, msg),
	})
}

func testClient(s *httptest.Server) *TaskClient {
	return NewTaskClient(s.URL, "")
}

// ── TaskClient tests ──

func TestTaskClientPlan(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := testClient(s)
	resp, err := c.Plan(PlanRequest{Prompt: "fix the bug"})
	if err != nil {
		t.Fatalf("plan error: %v", err)
	}
	if resp.Leg != "claude" {
		t.Fatalf("expected leg claude, got %s", resp.Leg)
	}
	if resp.Rationale != "test plan" {
		t.Fatalf("expected test plan, got %s", resp.Rationale)
	}
}

func TestTaskClientSubmit(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := testClient(s)
	resp, err := c.Submit(SubmitRequest{Prompt: "fix the bug"})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	if resp.TaskID != "task-test-0001" {
		t.Fatalf("expected task-test-0001, got %s", resp.TaskID)
	}
	if resp.State != StateAdmitted {
		t.Fatalf("expected admitted, got %s", resp.State)
	}
}

func TestTaskClientInspect(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := testClient(s)
	resp, err := c.Inspect("task-1", InspectRequest{IncludeCharges: true})
	if err != nil {
		t.Fatalf("inspect error: %v", err)
	}
	if resp.Task == nil {
		t.Fatal("expected task")
	}
	if resp.Task.State != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", resp.Task.State)
	}
}

func TestTaskClientEvents(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := testClient(s)
	resp, err := c.Events("task-1", "", 10)
	if err != nil {
		t.Fatalf("events error: %v", err)
	}
	if len(resp.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(resp.Events))
	}
}

func TestTaskClientArtifacts(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := testClient(s)
	resp, err := c.Artifacts("task-1", true)
	if err != nil {
		t.Fatalf("artifacts error: %v", err)
	}
	if resp.Integration != nil {
		t.Fatal("expected nil integration on test server")
	}
}

func TestTaskClientCancel(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := testClient(s)
	resp, err := c.Cancel("task-1", "test cancel")
	if err != nil {
		t.Fatalf("cancel error: %v", err)
	}
	if resp.State != StateCancelled {
		t.Fatalf("expected cancelled, got %s", resp.State)
	}
}

func TestTaskClientResume(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := testClient(s)
	resp, err := c.Resume("task-1", "att-1", "test resume")
	if err != nil {
		t.Fatalf("resume error: %v", err)
	}
	if resp.AttemptID != "att-new" {
		t.Fatalf("expected att-new, got %s", resp.AttemptID)
	}
}

func TestTaskClientConsumeEvents(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := testClient(s)
	events, cursor, err := c.ConsumeEvents("task-1", "")
	if err != nil {
		t.Fatalf("consume events error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if cursor != "" {
		t.Fatalf("expected empty cursor, got %s", cursor)
	}
}

func TestTaskClientPollForTerminal(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := testClient(s)
	state, ok := c.PollForTerminal("task-1", 50*time.Millisecond, 2*time.Second)
	if !ok {
		t.Fatal("expected to reach terminal")
	}
	if state != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", state)
	}
}

func TestTaskClientBrainUnreachable(t *testing.T) {
	c := NewTaskClient("http://127.0.0.1:1", "")
	_, err := c.Plan(PlanRequest{Prompt: "test"})
	if err == nil {
		t.Fatal("expected error for unreachable brain")
	}
	if err.Code != ErrUnavailable {
		t.Fatalf("expected unavailable, got %s", err.Code)
	}
}

// ── Host adapter tests ──

func TestHostAdapterBaseKind(t *testing.T) {
	base := NewHostAdapterBase(HostPi, testClient(testTaskServer()))
	if base.Kind() != HostPi {
		t.Fatalf("expected pi, got %s", base.Kind())
	}
}

func TestHostAdapterBaseDetectProjectRoot(t *testing.T) {
	base := NewHostAdapterBase(HostPi, testClient(testTaskServer()))
	root, err := base.DetectProjectRoot("")
	if err != nil {
		t.Fatalf("detect root: %v", err)
	}
	if root == "" {
		t.Fatal("expected non-empty root")
	}
}

func TestHostAdapterBaseCurrentRevision(t *testing.T) {
	base := NewHostAdapterBase(HostPi, testClient(testTaskServer()))
	root, _ := base.DetectProjectRoot("")
	rev, err := base.CurrentRevision(root)
	if err != nil {
		t.Fatalf("current revision: %v", err)
	}
	if len(rev) != 40 {
		t.Fatalf("expected 40-char sha, got %d: %s", len(rev), rev)
	}
}

func TestHostAdapterBaseApprovalDefault(t *testing.T) {
	base := NewHostAdapterBase(HostPi, testClient(testTaskServer()))
	if !base.OnApprovalNeeded("task-1", PermissionPolicy{}) {
		t.Fatal("default should approve")
	}
}

// ── Pi adapter tests ──

func TestPiAdapterKind(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	a := NewPiAdapter(testClient(s))
	if a.Kind() != HostPi {
		t.Fatalf("expected pi, got %s", a.Kind())
	}
}

func TestPiAdapterFullLifecycle(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	a := NewPiAdapter(testClient(s))
	result, err := FullLifecycle(a, "fix the bug", SubmitOptions{}, 50*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatalf("full lifecycle error: %v", err)
	}
	if result.TaskID == "" {
		t.Fatal("expected task ID")
	}
	if result.State != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", result.State)
	}
	if len(result.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(result.Events))
	}
}

func TestPiAdapterCancel(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	a := NewPiAdapter(testClient(s))
	resp, err := CancelTask(a, "task-test-1", "test")
	if err != nil {
		t.Fatalf("cancel error: %v", err)
	}
	if resp.State != StateCancelled {
		t.Fatalf("expected cancelled, got %s", resp.State)
	}
}

// ── Jido adapter tests ──

func TestJidoAdapterKind(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	a := NewJidoAdapter(testClient(s))
	if a.Kind() != HostJido {
		t.Fatalf("expected jido, got %s", a.Kind())
	}
}

func TestJidoAdapterEventConsumption(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	a := NewJidoAdapter(testClient(s))
	events, cursor, state := a.ConsumeTaskEvents("task-1", "", 50*time.Millisecond, 2*time.Second)
	if state != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", state)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if cursor != "" {
		t.Fatalf("expected empty cursor at end, got %s", cursor)
	}
}

func TestJidoAdapterRestartRecovery(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	a := NewJidoAdapter(testClient(s))
	a.stateFile = t.TempDir() + "/tasks.json"
	a.recordTask("task-restart-1")
	rec := a.RecoverOnStartup()
	if len(rec.Terminal) != 1 || rec.Terminal[0] != "task-restart-1" {
		t.Fatalf("expected 1 terminal task, got %+v", rec)
	}
}

// ── Editor adapter tests ──

type testApprover struct {
	approved bool
}

func (a testApprover) ApproveTask(plan *PlanResponse, policy PermissionPolicy) bool {
	return a.approved
}

func TestEditorAdapterKind(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	a := NewEditorAdapter(testClient(s), "/tmp", nil)
	if a.Kind() != HostEditor {
		t.Fatalf("expected editor, got %s", a.Kind())
	}
}

func TestEditorAdapterDetectProjectRoot(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	root, _ := os.Getwd()
	a := NewEditorAdapter(testClient(s), root, nil)
	got, err := a.DetectProjectRoot("")
	if err != nil {
		t.Fatalf("detect root: %v", err)
	}
	if got != root {
		t.Fatalf("expected %s, got %s", root, got)
	}
}

func TestEditorAdapterApprovalRejected(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	root, _ := os.Getwd()
	a := NewEditorAdapter(testClient(s), root, testApprover{approved: false})
	_, err := EditorPlanAndApprove(a, "fix the bug", SubmitOptions{})
	if err == nil {
		t.Fatal("expected error for rejected approval")
	}
	if err.Code != ErrForbidden {
		t.Fatalf("expected forbidden, got %s", err.Code)
	}
}

func TestEditorAdapterApprovalAccepted(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	root, _ := os.Getwd()
	a := NewEditorAdapter(testClient(s), root, testApprover{approved: true})
	resp, err := EditorPlanAndApprove(a, "fix the bug", SubmitOptions{})
	if err != nil {
		t.Fatalf("plan and approve error: %v", err)
	}
	if resp.TaskID == "" {
		t.Fatal("expected task ID")
	}
}

func TestEditorAdapterFullLifecycle(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	root, _ := os.Getwd()
	a := NewEditorAdapter(testClient(s), root, testApprover{approved: true})
	submit, err := EditorPlanAndApprove(a, "fix the bug", SubmitOptions{})
	if err != nil {
		t.Fatalf("plan and approve: %v", err)
	}
	state, handoff, ok := PollTask(a, submit.TaskID, 50*time.Millisecond, 5*time.Second)
	if !ok {
		t.Fatalf("task did not reach terminal")
	}
	if state != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", state)
	}
	if handoff == nil {
		t.Fatal("expected handoff")
	}
}

// ── Certification checklist tests ──

func TestCertChecklistProjectRoot(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := NewCertChecklist(NewPiAdapter(testClient(s)))
	if err := c.CheckProjectRoot(""); err != nil {
		t.Fatalf("project root check: %v", err)
	}
}

func TestCertChecklistBaseRevision(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := NewCertChecklist(NewPiAdapter(testClient(s)))
	root, _ := c.Host.DetectProjectRoot("")
	if err := c.CheckBaseRevision(root); err != nil {
		t.Fatalf("base revision check: %v", err)
	}
}

func TestCertChecklistCancelDuringExecution(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := NewCertChecklist(NewPiAdapter(testClient(s)))
	if err := c.CheckCancelDuringExecution("fix the bug", SubmitOptions{}); err != nil {
		t.Fatalf("cancel check: %v", err)
	}
}

func TestCertChecklistDelegationLoopRejection(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := NewCertChecklist(NewPiAdapter(testClient(s)))
	if err := c.CheckDelegationLoopRejection("task-loop-1"); err != nil {
		t.Fatalf("delegation loop check: %v", err)
	}
}

func TestCertChecklistDelegationDepthRejection(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := NewCertChecklist(NewPiAdapter(testClient(s)))
	if err := c.CheckDelegationDepthRejection(); err != nil {
		t.Fatalf("delegation depth check: %v", err)
	}
}

func TestCertChecklistFullSuite(t *testing.T) {
	s := testTaskServer()
	defer s.Close()
	c := NewCertChecklist(NewPiAdapter(testClient(s)))

	if err := c.CheckProjectRoot(""); err != nil {
		t.Errorf("project root: %v", err)
	}
	root, _ := c.Host.DetectProjectRoot("")
	if err := c.CheckBaseRevision(root); err != nil {
		t.Errorf("base revision: %v", err)
	}
	if err := c.CheckCancelDuringExecution("fix the bug", SubmitOptions{}); err != nil {
		t.Errorf("cancel: %v", err)
	}
	if err := c.CheckDelegationLoopRejection("task-suite-1"); err != nil {
		t.Errorf("delegation loop: %v", err)
	}
	if err := c.CheckDelegationDepthRejection(); err != nil {
		t.Errorf("delegation depth: %v", err)
	}
}
