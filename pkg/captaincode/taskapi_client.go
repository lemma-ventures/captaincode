package captaincode

// Task API HTTP client (ROADMAP M4.3–M4.5). The brain exposes a versioned
// task API at POST /v1/task (M4.1) with seven operations: plan, submit,
// inspect, events, artifacts, cancel, resume. A host adapter — Pi, Jido or
// an editor — needs a client that speaks that envelope, handles the
// structured error taxonomy, and manages the lifecycle (submit, poll,
// retrieve artifacts, cancel).
//
// This client is deliberately thin: it constructs the TaskRequest envelope,
// POSTs it, and decodes the TaskResponse. It does not retry, cache, or
// interpret business logic — a host that wants policy lives in the adapter
// layer (host_adapter.go), not here. The client is the wire; the adapter
// is the brain.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// TaskClient is the HTTP client for the versioned task API. It targets the
// brain's /v1/task endpoint. The brain URL defaults to
// http://127.0.0.1:14097 (CAPTAIN_BRAIN_URL overrides). An optional token
// is sent as a Bearer header when CAPTAIN_TASK_TOKEN is set.
type TaskClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// DefaultTaskClient reads CAPTAIN_BRAIN_URL and CAPTAIN_TASK_TOKEN from the
// environment. When the brain URL is unset, it defaults to localhost:14097.
func DefaultTaskClient() *TaskClient {
	base := os.Getenv("CAPTAIN_BRAIN_URL")
	if base == "" {
		base = "http://127.0.0.1:14097"
	}
	return &TaskClient{
		BaseURL: base,
		Token:   os.Getenv("CAPTAIN_TASK_TOKEN"),
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// NewTaskClient constructs a client with an explicit URL and token.
func NewTaskClient(baseURL, token string) *TaskClient {
	return &TaskClient{
		BaseURL: baseURL,
		Token:   token,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// call sends a TaskRequest envelope and decodes the TaskResponse. It returns
// the response envelope and a Go error only when the HTTP transport itself
// fails (network unreachable, timeout). A response with Status="error" is
// returned as a valid TaskResponse whose Error field carries the structured
// error — the caller switches on ErrCode, not on a Go error string.
func (c *TaskClient) call(op Operation, taskID string, body any) (*TaskResponse, error) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	req := TaskRequest{
		Version:   TaskAPIVersion,
		Op:        op,
		RequestID: NewRequestID(),
		TaskID:    taskID,
		Body:      bodyJSON,
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, c.BaseURL+"/v1/task", bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var tr TaskResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w (body: %s)", err, string(raw))
	}
	return &tr, nil
}

// Plan asks the server to assess a task and return a routing plan without
// executing it. Plan may consume a bounded planning allowance (a director
// call) but cannot run a worker.
func (c *TaskClient) Plan(req PlanRequest) (*PlanResponse, *TaskError) {
	tr, err := c.call(OpPlan, "", req)
	if err != nil {
		return nil, NewTaskError(ErrUnavailable, err.Error())
	}
	if tr.Error != nil {
		return nil, tr.Error
	}
	var resp PlanResponse
	if err := json.Unmarshal(tr.Body, &resp); err != nil {
		return nil, NewTaskError(ErrInternal, "unmarshal plan: "+err.Error())
	}
	return &resp, nil
}

// Submit sends a task for execution and returns the durable task ID. A
// duplicate identical request (same RequestID) attaches to the existing
// task; reuse with different scope is a conflict.
func (c *TaskClient) Submit(req SubmitRequest) (*SubmitResponse, *TaskError) {
	tr, err := c.call(OpSubmit, "", req)
	if err != nil {
		return nil, NewTaskError(ErrUnavailable, err.Error())
	}
	if tr.Error != nil {
		return nil, tr.Error
	}
	var resp SubmitResponse
	if err := json.Unmarshal(tr.Body, &resp); err != nil {
		return nil, NewTaskError(ErrInternal, "unmarshal submit: "+err.Error())
	}
	return &resp, nil
}

// Inspect reads a task's full state: lifecycle, charges, budget, decision,
// and handoff brief.
func (c *TaskClient) Inspect(taskID string, req InspectRequest) (*InspectResponse, *TaskError) {
	tr, err := c.call(OpInspect, taskID, req)
	if err != nil {
		return nil, NewTaskError(ErrUnavailable, err.Error())
	}
	if tr.Error != nil {
		return nil, tr.Error
	}
	var resp InspectResponse
	if err := json.Unmarshal(tr.Body, &resp); err != nil {
		return nil, NewTaskError(ErrInternal, "unmarshal inspect: "+err.Error())
	}
	return &resp, nil
}

// Events reads a bounded batch of task events from a cursor. Pass the
// NextCursor from the previous batch to resume. Disconnect resumes
// observation, not execution.
func (c *TaskClient) Events(taskID string, cursor string, limit int) (*EventsResponse, *TaskError) {
	tr, err := c.call(OpEvents, taskID, EventsRequest{Cursor: cursor, Limit: limit})
	if err != nil {
		return nil, NewTaskError(ErrUnavailable, err.Error())
	}
	if tr.Error != nil {
		return nil, tr.Error
	}
	var resp EventsResponse
	if err := json.Unmarshal(tr.Body, &resp); err != nil {
		return nil, NewTaskError(ErrInternal, "unmarshal events: "+err.Error())
	}
	return &resp, nil
}

// Artifacts reads the patch manifests and integration candidates for a task.
// The response carries digests and changed-file lists, not file contents.
func (c *TaskClient) Artifacts(taskID string, includeDiffs bool) (*ArtifactsResponse, *TaskError) {
	tr, err := c.call(OpArtifacts, taskID, ArtifactsRequest{IncludeDiffs: includeDiffs})
	if err != nil {
		return nil, NewTaskError(ErrUnavailable, err.Error())
	}
	if tr.Error != nil {
		return nil, tr.Error
	}
	var resp ArtifactsResponse
	if err := json.Unmarshal(tr.Body, &resp); err != nil {
		return nil, NewTaskError(ErrInternal, "unmarshal artifacts: "+err.Error())
	}
	return &resp, nil
}

// Cancel cancels a task and all its descendants.
func (c *TaskClient) Cancel(taskID string, reason string) (*CancelResponse, *TaskError) {
	tr, err := c.call(OpCancel, taskID, CancelRequest{Reason: reason})
	if err != nil {
		return nil, NewTaskError(ErrUnavailable, err.Error())
	}
	if tr.Error != nil {
		return nil, tr.Error
	}
	var resp CancelResponse
	if err := json.Unmarshal(tr.Body, &resp); err != nil {
		return nil, NewTaskError(ErrInternal, "unmarshal cancel: "+err.Error())
	}
	return &resp, nil
}

// Resume creates a new attempt under an interrupted task. The new attempt
// shares the task's remaining budget and has a causal link to the
// interrupted attempt.
func (c *TaskClient) Resume(taskID string, attemptID string, reason string) (*ResumeResponse, *TaskError) {
	tr, err := c.call(OpResume, taskID, ResumeRequest{AttemptID: attemptID, Reason: reason})
	if err != nil {
		return nil, NewTaskError(ErrUnavailable, err.Error())
	}
	if tr.Error != nil {
		return nil, tr.Error
	}
	var resp ResumeResponse
	if err := json.Unmarshal(tr.Body, &resp); err != nil {
		return nil, NewTaskError(ErrInternal, "unmarshal resume: "+err.Error())
	}
	return &resp, nil
}

// IsTerminal reports whether a lifecycle state is terminal (succeeded,
// failed, cancelled, exhausted). A host polls Inspect until this returns
// true.
func IsTerminal(s LifecycleState) bool {
	return s.IsTerminal()
}

// PollForTerminal calls Inspect repeatedly with the given interval until the
// task reaches a terminal state or the timeout expires. Returns the final
// state and a boolean indicating whether the task reached terminal (false
// on timeout).
func (c *TaskClient) PollForTerminal(taskID string, interval, timeout time.Duration) (LifecycleState, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := c.Inspect(taskID, InspectRequest{})
		if err != nil {
			time.Sleep(interval)
			continue
		}
		if resp.Task != nil && resp.Task.State.IsTerminal() {
			return resp.Task.State, true
		}
		time.Sleep(interval)
	}
	return "", false
}

// ConsumeEvents reads all events from a cursor, following NextCursor until
// HasMore is false. This is the pattern a host uses when it reconnects after
// a disconnect: pass the last cursor to resume observation.
func (c *TaskClient) ConsumeEvents(taskID string, cursor string) ([]TaskEvent, string, *TaskError) {
	var all []TaskEvent
	for {
		resp, err := c.Events(taskID, cursor, 100)
		if err != nil {
			return all, cursor, err
		}
		all = append(all, resp.Events...)
		if !resp.HasMore {
			return all, "", nil
		}
		cursor = resp.NextCursor
	}
}
