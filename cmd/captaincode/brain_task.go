package main

// Versioned task API HTTP handler (ROADMAP M4.1).
//
// POST /v1/task accepts a TaskRequest envelope with a protocol version, an
// operation, and an operation-specific body. Every response is a TaskResponse
// envelope with the same protocol version, a status ("ok" or "error"), and
// either a body or a structured error.
//
// Operations: plan, submit, inspect, events, artifacts, cancel, resume.
//
// The envelope is the contract. A host that reads the protocol version can
// refuse what it does not understand. A response with an error code can be
// handled programmatically rather than string-matched. The existing ad-hoc
// endpoints (/v1/route, /v1/assess, /v1/cancel, /v1/lifecycle, /v1/handoff)
// remain for the TUI; /v1/task is the portable surface for Pi, Jido and
// editor hosts.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func (b *brain) taskAPIHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, taskAPIError(captaincode.ErrMethodNotAllowed, "POST only"))
		return
	}
	if e := captaincode.CheckAuth(captaincode.DefaultTaskAuthPolicy(), r.RemoteAddr, r.Header.Get("Authorization")); e != nil {
		writeJSON(w, 401, taskAPIError(e.Code, e.Message))
		return
	}
	var req captaincode.TaskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "bad json: "+err.Error()))
		return
	}
	if e := captaincode.CheckCompatibility(req.Version); e != nil {
		writeJSON(w, 400, taskAPIError(e.Code, e.Message))
		return
	}
	if req.RequestID == "" {
		req.RequestID = captaincode.NewRequestID()
	}
	switch req.Op {
	case captaincode.OpPlan:
		b.taskPlan(w, r, req)
	case captaincode.OpSubmit:
		b.taskSubmit(w, r, req)
	case captaincode.OpInspect:
		b.taskInspect(w, r, req)
	case captaincode.OpEvents:
		b.taskEvents(w, r, req)
	case captaincode.OpArtifacts:
		b.taskArtifacts(w, r, req)
	case captaincode.OpCancel:
		b.taskCancel(w, r, req)
	case captaincode.OpResume:
		b.taskResume(w, r, req)
	default:
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "unknown op: "+string(req.Op)))
	}
}

func (b *brain) taskPlan(w http.ResponseWriter, _ *http.Request, req captaincode.TaskRequest) {
	var body captaincode.PlanRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "bad plan body: "+err.Error()))
		return
	}
	if body.Prompt == "" {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "prompt required"))
		return
	}
	resp, fail := b.decideRoute(routeReq{
		Task:     body.Prompt,
		Prefer:   body.Intent.Prefer,
		planOnly: true,
	})
	if fail != nil {
		writeJSON(w, fail.code, taskAPIError(captaincode.ErrInternal, fail.msg))
		return
	}
	plan := captaincode.PlanResponse{
		Leg:       captaincode.Leg(resp.Leg),
		Class:     captaincode.Class(resp.Class),
		Rationale: resp.Rationale,
	}
	b.mu.Lock()
	if body.Intent.Class == "" {
		body.Intent.Class = captaincode.Class(resp.Class)
	}
	dec, hasDec := b.pendingDecisions[strings.TrimSpace(body.Prompt)]
	b.mu.Unlock()
	if hasDec {
		plan.Decision = &dec.dec
	}
	b.writeTaskOK(w, req, captaincode.OpPlan, "", plan)
}

func (b *brain) taskSubmit(w http.ResponseWriter, _ *http.Request, req captaincode.TaskRequest) {
	var body captaincode.SubmitRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "bad submit body: "+err.Error()))
		return
	}
	if body.Prompt == "" {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "prompt required"))
		return
	}
	if e := captaincode.ValidateLineage(req.Lineage, req.TaskID); e != nil {
		writeJSON(w, 403, taskAPIError(e.Code, e.Message))
		return
	}
	if req.TaskID != "" {
		b.mu.Lock()
		ts := b.ledger.TaskStateFor(req.TaskID)
		b.mu.Unlock()
		if ts != nil {
			if ts.State.IsTerminal() {
				writeJSON(w, 409, taskAPIError(captaincode.ErrAlreadyTerminal,
					"task "+req.TaskID+" is terminal: "+string(ts.State)))
				return
			}
			b.writeTaskOK(w, req, captaincode.OpSubmit, req.TaskID, captaincode.SubmitResponse{
				TaskID: req.TaskID, State: ts.State,
			})
			return
		}
	}
	taskID := req.TaskID
	if taskID == "" {
		taskID = captaincode.NewTaskID()
	}
	b.mu.Lock()
	b.ledger.RecordTaskState(captaincode.TaskState{
		TaskID:    taskID,
		State:     captaincode.StateAdmitted,
		StartedAt: time.Now(),
	})
	b.ledger.Save()
	b.mu.Unlock()
	b.writeTaskOK(w, req, captaincode.OpSubmit, taskID, captaincode.SubmitResponse{
		TaskID: taskID, State: captaincode.StateAdmitted,
	})
}

func (b *brain) taskInspect(w http.ResponseWriter, _ *http.Request, req captaincode.TaskRequest) {
	if req.TaskID == "" {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "task_id required"))
		return
	}
	var body captaincode.InspectRequest
	if len(req.Body) > 0 {
		json.Unmarshal(req.Body, &body)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ts := b.ledger.TaskStateFor(req.TaskID)
	if ts == nil {
		writeJSON(w, 404, taskAPIError(captaincode.ErrNotFound, "task not found"))
		return
	}
	resp := captaincode.InspectResponse{
		Task:     ts,
		Attempts: b.ledger.AttemptStatesFor(req.TaskID),
	}
	if body.IncludeCharges {
		resp.Charges = b.ledger.ChargesFor(req.TaskID)
	}
	if body.IncludeBudget {
		resp.Budget = b.ledger.BudgetFor(req.TaskID)
	}
	if body.IncludeDecision {
		for _, d := range b.ledger.Decisions {
			if d.TaskID == req.TaskID {
				resp.Decision = &d
				break
			}
		}
	}
	if body.IncludeHandoff {
		resp.Handoff = b.ledger.HandoffFor(req.TaskID)
	}
	b.writeTaskOK(w, req, captaincode.OpInspect, req.TaskID, resp)
}

func (b *brain) taskEvents(w http.ResponseWriter, _ *http.Request, req captaincode.TaskRequest) {
	if req.TaskID == "" {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "task_id required"))
		return
	}
	var body captaincode.EventsRequest
	if len(req.Body) > 0 {
		json.Unmarshal(req.Body, &body)
	}
	limit := body.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	b.mu.Lock()
	fromSeq := captaincode.DecodeCursor(body.Cursor)
	var events []captaincode.TaskEvent
	for _, ev := range b.ledger.Events {
		if ev.TaskID != req.TaskID {
			continue
		}
		seq := ev.At.UnixNano()
		if seq <= fromSeq {
			continue
		}
		events = append(events, captaincode.TaskEvent{
			Seq:    seq,
			At:     ev.At,
			Kind:   captaincode.EventCharge,
			Leg:    ev.Leg,
			Detail: ev.Task,
		})
		if len(events) >= limit {
			break
		}
	}
	b.mu.Unlock()
	hasMore := len(events) >= limit
	var nextCursor string
	if hasMore && len(events) > 0 {
		nextCursor = captaincode.EncodeCursor(events[len(events)-1].Seq)
	}
	b.writeTaskOK(w, req, captaincode.OpEvents, req.TaskID, captaincode.EventsResponse{
		Events:     events,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	})
}

func (b *brain) taskArtifacts(w http.ResponseWriter, _ *http.Request, req captaincode.TaskRequest) {
	if req.TaskID == "" {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "task_id required"))
		return
	}
	b.imu.Lock()
	ic := b.lastIntegrations[req.TaskID]
	b.imu.Unlock()
	resp := captaincode.ArtifactsResponse{}
	if ic.TaskID != "" {
		resp.Integration = &ic
		resp.Manifests = ic.Manifests
	}
	b.writeTaskOK(w, req, captaincode.OpArtifacts, req.TaskID, resp)
}

func (b *brain) taskCancel(w http.ResponseWriter, _ *http.Request, req captaincode.TaskRequest) {
	if req.TaskID == "" {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "task_id required"))
		return
	}
	var body captaincode.CancelRequest
	if len(req.Body) > 0 {
		json.Unmarshal(req.Body, &body)
	}
	result := b.cancelTaskWithDeadline(req.TaskID)
	b.mu.Lock()
	finalState := captaincode.StateCancelled
	if ts := b.ledger.TaskStateFor(req.TaskID); ts != nil {
		finalState = ts.State
	}
	b.mu.Unlock()
	b.writeTaskOK(w, req, captaincode.OpCancel, req.TaskID, captaincode.CancelResponse{
		TaskID:    req.TaskID,
		Cancelled: result.Cancelled,
		State:     finalState,
	})
}

func (b *brain) taskResume(w http.ResponseWriter, _ *http.Request, req captaincode.TaskRequest) {
	if req.TaskID == "" {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "task_id required"))
		return
	}
	var body captaincode.ResumeRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "bad resume body: "+err.Error()))
		return
	}
	if body.AttemptID == "" {
		writeJSON(w, 400, taskAPIError(captaincode.ErrBadRequest, "attempt_id required"))
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	as := b.ledger.AttemptStateFor(body.AttemptID)
	if as == nil {
		writeJSON(w, 404, taskAPIError(captaincode.ErrNotFound, "attempt not found"))
		return
	}
	if as.State != captaincode.StateInterrupted {
		writeJSON(w, 409, taskAPIError(captaincode.ErrConflict,
			"attempt is "+string(as.State)+", not interrupted"))
		return
	}
	if as.TaskID != req.TaskID {
		writeJSON(w, 409, taskAPIError(captaincode.ErrConflict,
			"attempt belongs to task "+as.TaskID+", not "+req.TaskID))
		return
	}
	budget := b.ledger.BudgetFor(req.TaskID)
	if budget != nil && budget.MaxAttempts > 0 && budget.SettledAttempts >= budget.MaxAttempts {
		writeJSON(w, 409, taskAPIError(captaincode.ErrExhausted,
			"budget exhausted: no attempts remaining"))
		return
	}
	gen := b.ledger.ClaimOwnership(body.AttemptID, b.processID)
	if gen == 0 {
		writeJSON(w, 409, taskAPIError(captaincode.ErrConflict, "could not claim ownership"))
		return
	}
	b.ledger.TransitionAttempt(body.AttemptID, captaincode.StateRunning)
	b.ledger.Save()
	ts := b.ledger.TaskStateFor(req.TaskID)
	taskState := captaincode.StateRunning
	if ts != nil {
		if captaincode.CanTransition(ts.State, captaincode.StateRunning) {
			b.ledger.TransitionTask(req.TaskID, captaincode.StateRunning)
		} else {
			taskState = ts.State
		}
	}
	b.writeTaskOK(w, req, captaincode.OpResume, req.TaskID, captaincode.ResumeResponse{
		TaskID:        req.TaskID,
		AttemptID:     body.AttemptID,
		ParentAttempt: body.AttemptID,
		State:         taskState,
		Budget:        budget,
	})
}

// writeTaskOK sends a successful TaskResponse envelope.
func (b *brain) writeTaskOK(w http.ResponseWriter, req captaincode.TaskRequest, op captaincode.Operation, taskID string, body any) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		writeJSON(w, 500, taskAPIError(captaincode.ErrInternal, "marshal body: "+err.Error()))
		return
	}
	writeJSON(w, 200, captaincode.TaskResponse{
		Version:   captaincode.TaskAPIVersion,
		Status:    captaincode.ResponseOK,
		Op:        op,
		TaskID:    taskID,
		RequestID: req.RequestID,
		Body:      bodyJSON,
	})
}

// taskAPIError builds a TaskResponse envelope with an error for direct HTTP
// responses (before the handler can construct a full response).
func taskAPIError(code captaincode.ErrCode, msg string) captaincode.TaskResponse {
	return captaincode.TaskResponse{
		Version: captaincode.TaskAPIVersion,
		Status:  captaincode.ResponseError,
		Error:   captaincode.NewTaskError(code, msg),
	}
}
