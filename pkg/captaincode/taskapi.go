package captaincode

// Versioned task API (ROADMAP M4.1). The brain's existing HTTP surface —
// /v1/route, /v1/assess, /v1/cancel, /v1/lifecycle, /v1/handoff — is ad-hoc:
// each endpoint has its own request/response shape, no protocol version, no
// structured error taxonomy, and no envelope a host adapter can rely on. A
// caller from Pi, Jido or an editor cannot depend on a contract that is
// implicit in handler code.
//
// M4.1 formalizes that surface into a versioned task API with seven
// operations — plan, submit, inspect, events, artifacts, cancel, resume —
// each carried in a stamped envelope with a protocol version, a request
// identity, a task identity and a structured error channel. The envelope is
// the contract: a host that reads the protocol version can refuse what it
// does not understand, and a response that carries an error code can be
// handled programmatically rather than string-matched.
//
// Design rules from the roadmap:
//
//   - Protocol version on every request and response. A server that receives
//     a version it does not support rejects it with ErrUnsupportedVersion,
//     not a 500. A client that receives a response version it does not
//     understand refuses the body.
//   - Plan may consume a bounded planning allowance but cannot execute.
//     Submission returns a durable task ID. A duplicate identical request
//     attaches to that task; reuse with different scope/policy/input is a
//     conflict.
//   - Disconnect semantics are distinct from cancellation. Reconnect resumes
//     observation (events), not execution.
//   - Delegation lineage is server-validated. A worker-supplied depth counter
//     is not the only recursion protection; the server carries the parent
//     chain.
//   - Error taxonomy is machine-readable. An error code is a stable token;
//     the message is human context.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"
)

// TaskAPIVersion is the current protocol version. Bumped when a breaking
// change is made to any request or response shape. A server that receives a
// request at a version it does not support rejects it with
// ErrUnsupportedVersion.
const TaskAPIVersion = 1

// TaskAPIMinVersion is the oldest protocol version the current server accepts.
// A request below this is rejected. Raising it is a breaking change that
// requires a version bump.
const TaskAPIMinVersion = 1

// Operation is the task API verb: what the caller wants to do.
type Operation string

const (
	OpPlan      Operation = "plan"      // assess complexity, pick a worker, return a plan (no execution)
	OpSubmit    Operation = "submit"    // submit a task for execution, return a durable task ID
	OpInspect   Operation = "inspect"   // read task/attempt state, charges, budget, decision
	OpEvents    Operation = "events"    // read a bounded batch of task events from a cursor
	OpArtifacts Operation = "artifacts" // read patch manifests and integration candidates
	OpCancel    Operation = "cancel"    // cancel a task and all its descendants
	OpResume    Operation = "resume"    // create a new attempt under an interrupted task
)

// ── envelope ──

// TaskRequest is the envelope every operation arrives in. The body is
// operation-specific; the envelope carries the protocol version, the request
// identity (for idempotency and tracing), and the delegation lineage.
type TaskRequest struct {
	Version   int                `json:"version"`
	Op        Operation          `json:"op"`
	RequestID string             `json:"request_id"`
	TaskID    string             `json:"task_id,omitempty"`
	Lineage   *DelegationLineage `json:"lineage,omitempty"`
	Body      json.RawMessage    `json:"body,omitempty"`
}

// DelegationLineage carries the parent chain for nested delegation. The
// server validates depth and rejects recursive loops. A worker-supplied
// depth counter is not the only recursion protection: the server checks the
// parent chain against its own task store.
type DelegationLineage struct {
	ParentTaskID string   `json:"parent_task_id"`
	Depth        int      `json:"depth"`
	Ancestors    []string `json:"ancestors,omitempty"`
}

// TaskResponse is the envelope every operation returns. The status is "ok"
// or "error"; the body is operation-specific on success, and the error is
// structured on failure.
type TaskResponse struct {
	Version   int             `json:"version"`
	Status    string          `json:"status"` // "ok" | "error"
	Op        Operation       `json:"op"`
	TaskID    string          `json:"task_id,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
	Error     *TaskError      `json:"error,omitempty"`
}

const (
	ResponseOK    = "ok"
	ResponseError = "error"
)

// ── error taxonomy ──

// ErrCode is a machine-readable error token. Stable across versions; a client
// can switch on it. The message is human context, not a contract.
type ErrCode string

const (
	ErrUnsupportedVersion ErrCode = "unsupported_version"
	ErrBadRequest         ErrCode = "bad_request"
	ErrNotFound           ErrCode = "not_found"
	ErrConflict           ErrCode = "conflict"
	ErrMethodNotAllowed   ErrCode = "method_not_allowed"
	ErrExhausted          ErrCode = "exhausted"
	ErrAlreadyTerminal    ErrCode = "already_terminal"
	ErrUnauthorized       ErrCode = "unauthorized"
	ErrForbidden          ErrCode = "forbidden"
	ErrInternal           ErrCode = "internal"
	ErrUnavailable        ErrCode = "unavailable"
)

// TaskError is the structured error a response carries on failure. The code
// is the stable token; the message is context; details is optional structured
// data (e.g. the legal transitions for a bad state change).
type TaskError struct {
	Code    ErrCode         `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

// NewTaskError builds a TaskError with a message.
func NewTaskError(code ErrCode, msg string) *TaskError {
	return &TaskError{Code: code, Message: msg}
}

// Error implements error so TaskError can be used directly.
func (e *TaskError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// CheckCompatibility validates that a request's protocol version is in the
// server's supported range. A version above the server's current is rejected
// (the server cannot speak a future protocol); a version below the minimum
// is rejected (the contract has changed). Returns nil when the version is
// acceptable.
func CheckCompatibility(reqVersion int) *TaskError {
	if reqVersion < TaskAPIMinVersion {
		return NewTaskError(ErrUnsupportedVersion, fmt.Sprintf(
			"protocol version %d is below minimum %d", reqVersion, TaskAPIMinVersion))
	}
	if reqVersion > TaskAPIVersion {
		return NewTaskError(ErrUnsupportedVersion, fmt.Sprintf(
			"protocol version %d is above supported %d", reqVersion, TaskAPIVersion))
	}
	return nil
}

// ── operation bodies ──

// PlanRequest asks the server to assess a task and return a routing plan
// without executing it. Plan may consume a bounded planning allowance (a
// director call) but cannot run a worker.
type PlanRequest struct {
	Prompt       string           `json:"prompt"`
	ProjectRoot  string           `json:"project_root,omitempty"`
	BaseRevision string           `json:"base_revision,omitempty"`
	Intent       TaskIntent       `json:"intent"`
	Caps         Requirements     `json:"caps,omitempty"`
	Permissions  PermissionPolicy `json:"permissions,omitempty"`
}

// PlanResponse is what the server returns for a plan: the selected leg, the
// rationale, the class, and the estimated cost/duration if samples support
// it. No task ID is returned because plan does not execute.
type PlanResponse struct {
	Leg                  Leg       `json:"leg"`
	Class                Class     `json:"class"`
	Domain               Domain    `json:"domain"`
	Rationale            string    `json:"rationale"`
	Decision             *Decision `json:"decision,omitempty"`
	EstCostUSD           float64   `json:"est_cost_usd,omitempty"`
	EstDurationMs        int64     `json:"est_duration_ms,omitempty"`
	EstDurationSupported bool      `json:"est_duration_supported"`
}

// SubmitRequest submits a task for execution. The server returns a durable
// task ID. A duplicate identical request (same RequestID) attaches to the
// existing task; reuse with different scope/policy/input is a conflict.
type SubmitRequest struct {
	Prompt       string           `json:"prompt"`
	ProjectRoot  string           `json:"project_root,omitempty"`
	BaseRevision string           `json:"base_revision,omitempty"`
	Intent       TaskIntent       `json:"intent"`
	Caps         Requirements     `json:"caps,omitempty"`
	Permissions  PermissionPolicy `json:"permissions,omitempty"`
	Checks       AcceptanceChecks `json:"checks,omitempty"`
	Limits       ResourceLimits   `json:"limits,omitempty"`
	Leg          Leg              `json:"leg,omitempty"`
}

// SubmitResponse returns the durable task ID and the initial lifecycle state.
type SubmitResponse struct {
	TaskID  string         `json:"task_id"`
	State   LifecycleState `json:"state"`
	Attempt string         `json:"attempt_id,omitempty"`
}

// InspectRequest reads a task's full state: lifecycle, charges, budget,
// decision, and handoff brief if one exists.
type InspectRequest struct {
	IncludeCharges  bool `json:"include_charges"`
	IncludeBudget   bool `json:"include_budget"`
	IncludeDecision bool `json:"include_decision"`
	IncludeHandoff  bool `json:"include_handoff"`
}

// InspectResponse carries everything the server knows about a task.
type InspectResponse struct {
	Task     *TaskState     `json:"task,omitempty"`
	Attempts []AttemptState `json:"attempts,omitempty"`
	Charges  []Charge       `json:"charges,omitempty"`
	Budget   *Budget        `json:"budget,omitempty"`
	Decision *Decision      `json:"decision,omitempty"`
	Handoff  *HandoffBrief  `json:"handoff,omitempty"`
}

// EventsRequest reads a bounded batch of task events from a cursor. The
// cursor is opaque; the client passes back the NextCursor from the previous
// batch. An empty cursor starts from the beginning. Disconnect resumes
// observation (events), not execution.
type EventsRequest struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// EventsResponse is a bounded batch of events. HasMore is true when more
// events exist after this batch; NextCursor is the opaque cursor to pass
// back. When HasMore is false, NextCursor is empty.
type EventsResponse struct {
	Events     []TaskEvent `json:"events"`
	HasMore    bool        `json:"has_more"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// ArtifactsRequest reads the patch manifests and integration candidates for
// a task. Artifact references do not allow arbitrary filesystem reads; the
// response carries digests and changed-file lists, not file contents.
type ArtifactsRequest struct {
	IncludeDiffs bool `json:"include_diffs"`
}

// ArtifactsResponse carries the integration candidate and its manifests.
type ArtifactsResponse struct {
	Integration *IntegrationCandidate `json:"integration,omitempty"`
	Manifests   []PatchManifest       `json:"manifests,omitempty"`
}

// CancelRequest cancels a task and all its descendants.
type CancelRequest struct {
	Reason string `json:"reason,omitempty"`
}

// CancelResponse reports what was cancelled.
type CancelResponse struct {
	TaskID    string         `json:"task_id"`
	Cancelled []string       `json:"cancelled"`
	State     LifecycleState `json:"state"`
}

// ResumeRequest creates a new attempt under an interrupted task. The new
// attempt shares the task's remaining budget and has a causal link to the
// interrupted attempt. The server checks ownership before resuming.
type ResumeRequest struct {
	AttemptID string `json:"attempt_id"` // the interrupted attempt to resume from
	Reason    string `json:"reason,omitempty"`
}

// ResumeResponse returns the new attempt ID and the lifecycle state.
type ResumeResponse struct {
	TaskID        string         `json:"task_id"`
	AttemptID     string         `json:"attempt_id"`
	ParentAttempt string         `json:"parent_attempt,omitempty"`
	State         LifecycleState `json:"state"`
	Budget        *Budget        `json:"budget,omitempty"`
}

// ── supporting types ──

// TaskIntent carries what the task is: the prompt is already in the request
// body, so Intent carries the hints a plan needs — class, domain, and
// whether the caller wants a specific leg.
type TaskIntent struct {
	Class    Class  `json:"class,omitempty"`
	Domain   Domain `json:"domain,omitempty"`
	Prefer   string `json:"prefer,omitempty"`
	Frontier bool   `json:"frontier,omitempty"`
}

// PermissionPolicy defines the capability/permission scope for a task. A
// strict scope request must reject an adapter that cannot enforce it, rather
// than rely on prompt instructions.
type PermissionPolicy struct {
	Strict     bool         `json:"strict"`
	Required   Requirements `json:"required,omitempty"`
	ReadOnly   bool         `json:"read_only,omitempty"`
	AllowTools []string     `json:"allow_tools,omitempty"`
	DenyTools  []string     `json:"deny_tools,omitempty"`
}

// AcceptanceChecks defines the checks that decide acceptance for a submitted
// task. The command and exit code are the deterministic gate; the
// must_change/must_not_change globs are the artifact constraints.
type AcceptanceChecks struct {
	Command       []string `json:"command,omitempty"`
	MustChange    []string `json:"must_change,omitempty"`
	MustNotChange []string `json:"must_not_change,omitempty"`
	ReviewBlinded bool     `json:"review_blinded"`
}

// ResourceLimits defines the aggregate ceiling for a task. These map to the
// M2.4 Budget type.
type ResourceLimits struct {
	MaxAttempts int     `json:"max_attempts,omitempty"`
	MaxCostUSD  float64 `json:"max_cost_usd,omitempty"`
	MaxWallMs   int64   `json:"max_wall_ms,omitempty"`
}

// TaskEvent is one event in the bounded event stream. Events are derived
// from the ledger's existing Event, Charge, and lifecycle state transitions,
// projected into a uniform shape the host can consume without knowing the
// ledger's internal types.
type TaskEvent struct {
	Seq       int64          `json:"seq"`
	At        time.Time      `json:"at"`
	Kind      TaskEventKind  `json:"kind"`
	AttemptID string         `json:"attempt_id,omitempty"`
	StageID   string         `json:"stage_id,omitempty"`
	Leg       Leg            `json:"leg,omitempty"`
	State     LifecycleState `json:"state,omitempty"`
	Detail    string         `json:"detail,omitempty"`
}

// TaskEventKind classifies an event for the stream.
type TaskEventKind string

const (
	EventTaskAdmitted    TaskEventKind = "task_admitted"
	EventTaskRunning     TaskEventKind = "task_running"
	EventTaskTerminal    TaskEventKind = "task_terminal"
	EventAttemptStart    TaskEventKind = "attempt_start"
	EventAttemptState    TaskEventKind = "attempt_state"
	EventAttemptTerminal TaskEventKind = "attempt_terminal"
	EventCharge          TaskEventKind = "charge"
	EventDecision        TaskEventKind = "decision"
	EventBudget          TaskEventKind = "budget"
	EventCancel          TaskEventKind = "cancel"
	EventHandoff         TaskEventKind = "handoff"
)

// ── authentication ──

// TaskAuthPolicy defines the access control for the task API endpoint. The
// roadmap is explicit: "loopback binding is not an auth policy." A host
// token or loopback-only enforcement is needed before the endpoint is
// exposed beyond localhost.
//
// When Token is set, every request must carry it as a Bearer token in the
// Authorization header. The loopback restriction is lifted, because the
// operator explicitly chose to expose the endpoint and the token is the
// gate.
//
// When Token is empty, the endpoint is loopback-only: requests from non-
// loopback addresses are rejected with ErrUnauthorized. This is the default
// — safe for local development, the TUI, and MCP stdio.
type TaskAuthPolicy struct {
	Token        string
	LoopbackOnly bool
}

// DefaultTaskAuthPolicy reads CAPTAIN_TASK_TOKEN. When set, token-based auth
// is active and loopback restriction is lifted. When unset, the endpoint is
// loopback-only.
func DefaultTaskAuthPolicy() TaskAuthPolicy {
	token := os.Getenv("CAPTAIN_TASK_TOKEN")
	return TaskAuthPolicy{
		Token:        token,
		LoopbackOnly: token == "",
	}
}

// CheckAuth validates a request against the auth policy. remoteAddr is the
// connecting address (r.RemoteAddr). authHeader is the Authorization header
// value. Returns nil when the request is authorized, or a TaskError when
// it is not.
func CheckAuth(policy TaskAuthPolicy, remoteAddr string, authHeader string) *TaskError {
	if policy.Token != "" {
		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			return NewTaskError(ErrUnauthorized, "missing or malformed Authorization header")
		}
		if strings.TrimPrefix(authHeader, prefix) != policy.Token {
			return NewTaskError(ErrUnauthorized, "invalid token")
		}
		return nil
	}
	if policy.LoopbackOnly && !isLoopback(remoteAddr) {
		return NewTaskError(ErrUnauthorized, "task API is loopback-only; set CAPTAIN_TASK_TOKEN to expose it")
	}
	return nil
}

// isLoopback reports whether an address is a loopback address. The remote
// address from net/http is host:port; we strip the port before checking.
func isLoopback(addr string) bool {
	host := addr
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		host = addr[:idx]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// ── request ID generation ──

// NewRequestID generates a request identity for idempotency and tracing.
// A client may supply its own; when absent the server generates one.
func NewRequestID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "req-" + hex.EncodeToString(b)
}

// NewTaskID generates a durable task identity.
func NewTaskID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "task-" + hex.EncodeToString(b)
}

// ── delegation validation ──

// MaxDelegationDepth is the recursion limit for nested delegation. A worker
// that delegates to a sub-worker that delegates again cannot exceed this
// depth. The server checks the lineage, not just a worker-supplied counter.
const MaxDelegationDepth = 4

// ValidateLineage checks a delegation chain for recursion and depth. Returns
// an error when the chain is too deep or contains a cycle (the task ID
// appears in its own ancestor list).
func ValidateLineage(lineage *DelegationLineage, taskID string) *TaskError {
	if lineage == nil {
		return nil
	}
	if lineage.Depth > MaxDelegationDepth {
		return NewTaskError(ErrForbidden, fmt.Sprintf(
			"delegation depth %d exceeds maximum %d", lineage.Depth, MaxDelegationDepth))
	}
	for _, ancestor := range lineage.Ancestors {
		if ancestor == taskID {
			return NewTaskError(ErrForbidden, "recursive delegation loop detected")
		}
	}
	return nil
}

// ── duplicate detection ──

// IsDuplicateSubmit checks whether a submit request is identical to a
// previous one. A duplicate identical request attaches to the existing task;
// reuse with different scope/policy/input is a conflict. The comparison is
// on the fields that define the work: prompt, leg, caps, permissions, checks,
// and limits.
func IsDuplicateSubmit(a, b SubmitRequest) bool {
	if a.Prompt != b.Prompt || a.Leg != b.Leg {
		return false
	}
	if a.Intent != b.Intent {
		return false
	}
	if !reflect.DeepEqual(a.Caps, b.Caps) {
		return false
	}
	if !reflect.DeepEqual(a.Permissions, b.Permissions) {
		return false
	}
	if !reflect.DeepEqual(a.Checks, b.Checks) {
		return false
	}
	if !reflect.DeepEqual(a.Limits, b.Limits) {
		return false
	}
	return true
}

// ── event stream cursor ──

// EncodeCursor produces an opaque cursor from a sequence number. The cursor
// is base64-ish hex so the client treats it as opaque.
func EncodeCursor(seq int64) string {
	return fmt.Sprintf("c-%d", seq)
}

// DecodeCursor parses an opaque cursor back into a sequence number. Returns
// 0 for an empty cursor (start from the beginning).
func DecodeCursor(cursor string) int64 {
	if cursor == "" {
		return 0
	}
	var seq int64
	fmt.Sscanf(cursor, "c-%d", &seq)
	return seq
}
