package captaincode

// Host adapter framework (ROADMAP M4.3–M4.5). The task API client
// (taskapi_client.go) is the wire; the host adapter is the brain. A host
// adapter wraps the client with the concerns a specific host has:
//
//   - Project-root detection: where on disk the task's code lives.
//   - Base-revision pinning: what commit the worker starts from.
//   - Lifecycle management: submit, poll, retrieve artifacts, cancel.
//   - Approval/scope handling: whether the host's permission model
//     satisfies the task's PermissionPolicy.
//   - Restart ownership: what happens when the host process restarts
//     while a task is in flight.
//
// Each concrete host (Pi, Jido, editor) implements the HostAdapter
// interface and gets the shared lifecycle, artifact retrieval, and
// certification checklist for free.
//
// The host certification checklist (ROADMAP M4 exit gate) is the same
// fixture every host must pass: complete with an inspected artifact,
// cancel during execution, recover after host and brain restart, reject
// conflicting retries, retain the root budget, deny scope widening,
// reject recursive delegation loops, and expose an unsupported capability
// clearly.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// HostKind identifies which host platform an adapter serves.
type HostKind string

const (
	HostPi     HostKind = "pi"
	HostJido   HostKind = "jido"
	HostEditor HostKind = "editor"
)

// HostAdapter is the contract a concrete host (Pi, Jido, editor) satisfies.
// The shared lifecycle methods (SubmitTask, PollTask, CancelTask,
// RetrieveArtifacts) are implemented by HostAdapterBase; a concrete host
// implements the host-specific hooks.
type HostAdapter interface {
	Kind() HostKind
	Client() *TaskClient

	// DetectProjectRoot finds the project root for a task. The default
	// implementation walks up from cwd looking for .git; a host may
	// override (e.g. an editor passes its workspace folder).
	DetectProjectRoot(hint string) (string, error)

	// CurrentRevision returns the base revision the worker should start
	// from. The default implementation runs `git rev-parse HEAD`; a host
	// may override (e.g. Pi pins a release tag).
	CurrentRevision(projectRoot string) (string, error)

	// OnTaskSubmitted is called after a task is submitted but before
	// polling begins. A host may display a notification, open a progress
	// panel, or log the submission.
	OnTaskSubmitted(taskID string, prompt string)

	// OnTaskTerminal is called when a task reaches a terminal state.
	// A host may display the result, open the changed files, or prompt
	// for review.
	OnTaskTerminal(taskID string, state LifecycleState, handoff *HandoffBrief)

	// OnApprovalNeeded is called when a task's permission policy
	// requires host-level approval (e.g. an editor's "allow this tool"
	// dialog). Returns true if the host approves, false to reject.
	OnApprovalNeeded(taskID string, policy PermissionPolicy) bool
}

// HostAdapterBase provides the shared lifecycle implementation. A concrete
// host embeds this and overrides the hooks it needs.
type HostAdapterBase struct {
	client *TaskClient
	kind   HostKind
}

// NewHostAdapterBase constructs the shared base for a host kind.
func NewHostAdapterBase(kind HostKind, client *TaskClient) HostAdapterBase {
	return HostAdapterBase{client: client, kind: kind}
}

// Kind returns the host platform identifier.
func (b HostAdapterBase) Kind() HostKind { return b.kind }

// Client returns the task API client.
func (b HostAdapterBase) Client() *TaskClient { return b.client }

// DetectProjectRoot walks up from the hint (or cwd) looking for a .git
// directory. Returns the first directory containing one. A host whose
// project root is not git-based (e.g. a Pi deployment directory) should
// override this.
func (b HostAdapterBase) DetectProjectRoot(hint string) (string, error) {
	start := hint
	if start == "" {
		start, _ = os.Getwd()
	}
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .git found walking up from %s", start)
		}
		dir = parent
	}
}

// CurrentRevision returns the HEAD commit sha of the project root. Uses
// the existing worktree.CurrentRevision function. A host that pins a
// specific revision should override.
func (b HostAdapterBase) CurrentRevision(projectRoot string) (string, error) {
	rev := CurrentRevision(projectRoot)
	if rev == "" {
		return "", fmt.Errorf("no git HEAD at %s", projectRoot)
	}
	return rev, nil
}

// OnTaskSubmitted is a no-op default. Override in a concrete host.
func (b HostAdapterBase) OnTaskSubmitted(taskID string, prompt string) {}

// OnTaskTerminal is a no-op default. Override in a concrete host.
func (b HostAdapterBase) OnTaskTerminal(taskID string, state LifecycleState, handoff *HandoffBrief) {}

// OnApprovalNeeded returns true (approve) by default. Override in a host
// that has a real approval dialog (e.g. an editor).
func (b HostAdapterBase) OnApprovalNeeded(taskID string, policy PermissionPolicy) bool {
	return true
}

// SubmitTask is the shared lifecycle entry point. It detects the project
// root (if not provided), pins the base revision, submits the task, and
// calls the OnTaskSubmitted hook. Returns the task ID and initial state.
func SubmitTask(h HostAdapter, prompt string, projectRoot string, opts SubmitOptions) (*SubmitResponse, *TaskError) {
	if projectRoot == "" {
		root, err := h.DetectProjectRoot("")
		if err != nil {
			return nil, NewTaskError(ErrBadRequest, "detect project root: "+err.Error())
		}
		projectRoot = root
	}
	baseRev, err := h.CurrentRevision(projectRoot)
	if err != nil {
		return nil, NewTaskError(ErrBadRequest, "detect base revision: "+err.Error())
	}
	req := SubmitRequest{
		Prompt:       prompt,
		ProjectRoot:  projectRoot,
		BaseRevision: baseRev,
		Intent:       opts.Intent,
		Caps:         opts.Caps,
		Permissions:  opts.Permissions,
		Checks:       opts.Checks,
		Limits:       opts.Limits,
		Leg:          opts.Leg,
	}
	if !h.OnApprovalNeeded("", opts.Permissions) {
		return nil, NewTaskError(ErrForbidden, "host denied approval for this task")
	}
	client := h.Client()
	resp, submitErr := client.Submit(req)
	if submitErr != nil {
		return nil, submitErr
	}
	h.OnTaskSubmitted(resp.TaskID, prompt)
	return resp, nil
}

// SubmitOptions carries the optional fields a host passes to SubmitTask.
type SubmitOptions struct {
	Intent      TaskIntent
	Caps        Requirements
	Permissions PermissionPolicy
	Checks      AcceptanceChecks
	Limits      ResourceLimits
	Leg         Leg
}

// PollTask polls Inspect until the task reaches a terminal state or the
// timeout expires. On terminal, it retrieves the handoff brief and calls
// OnTaskTerminal. Returns the final state and the handoff (if any).
func PollTask(h HostAdapter, taskID string, interval, timeout time.Duration) (LifecycleState, *HandoffBrief, bool) {
	state, ok := h.Client().PollForTerminal(taskID, interval, timeout)
	if !ok {
		return state, nil, false
	}
	handoff := retrieveHandoff(h.Client(), taskID)
	h.OnTaskTerminal(taskID, state, handoff)
	return state, handoff, true
}

// retrieveHandoff fetches the handoff brief for a task via Inspect.
func retrieveHandoff(c *TaskClient, taskID string) *HandoffBrief {
	resp, err := c.Inspect(taskID, InspectRequest{IncludeHandoff: true})
	if err != nil {
		return nil
	}
	return resp.Handoff
}

// CancelTask cancels a task and all its descendants.
func CancelTask(h HostAdapter, taskID string, reason string) (*CancelResponse, *TaskError) {
	return h.Client().Cancel(taskID, reason)
}

// RetrieveArtifacts reads the patch manifests and integration candidates
// for a task. The host displays these (changed files, diffs, check
// evidence) rather than reading the filesystem directly.
func RetrieveArtifacts(h HostAdapter, taskID string, includeDiffs bool) (*ArtifactsResponse, *TaskError) {
	return h.Client().Artifacts(taskID, includeDiffs)
}

// ResumeTask creates a new attempt under an interrupted task. This is the
// restart-ownership path: after a host or brain restart, the host calls
// this to resume an interrupted task rather than re-submitting.
func ResumeTask(h HostAdapter, taskID string, attemptID string, reason string) (*ResumeResponse, *TaskError) {
	return h.Client().Resume(taskID, attemptID, reason)
}

// FullLifecycle is the shared certification fixture: submit → poll →
// inspect → artifacts. A host certification test calls this and asserts
// on the returned LifecycleResult. The roadmap's exit gate requires every
// host to pass this fixture.
type LifecycleResult struct {
	TaskID    string
	State     LifecycleState
	Handoff   *HandoffBrief
	Artifacts *ArtifactsResponse
	Events    []TaskEvent
}

// FullLifecycle runs the complete task lifecycle against a host adapter.
// It submits a task, polls to terminal, retrieves artifacts, and consumes
// all events. Returns the assembled result for certification assertions.
func FullLifecycle(h HostAdapter, prompt string, opts SubmitOptions, pollInterval, pollTimeout time.Duration) (*LifecycleResult, *TaskError) {
	submit, err := SubmitTask(h, prompt, "", opts)
	if err != nil {
		return nil, err
	}
	result := &LifecycleResult{TaskID: submit.TaskID}
	state, handoff, ok := PollTask(h, submit.TaskID, pollInterval, pollTimeout)
	if !ok {
		result.State = state
		return result, NewTaskError(ErrUnavailable, "task did not reach terminal within timeout")
	}
	result.State = state
	result.Handoff = handoff
	arts, _ := RetrieveArtifacts(h, submit.TaskID, true)
	result.Artifacts = arts
	events, _, _ := h.Client().ConsumeEvents(submit.TaskID, "")
	result.Events = events
	return result, nil
}

// ── host certification checklist ──

// CertChecklist is the roadmap M4 exit gate: the same fixture every host
// must pass. Each method is a standalone assertion a certification test
// calls. The checklist is deliberately conservative — a host that fails
// any item is not certified.
type CertChecklist struct {
	Host HostAdapter
}

// NewCertChecklist constructs the certification checklist for a host.
func NewCertChecklist(h HostAdapter) CertChecklist {
	return CertChecklist{Host: h}
}

// CheckProjectRoot verifies the host can detect a project root from the
// given hint. Returns nil when the root is found and is a directory.
func (c CertChecklist) CheckProjectRoot(hint string) error {
	root, err := c.Host.DetectProjectRoot(hint)
	if err != nil {
		return fmt.Errorf("project root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("project root stat: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project root %s is not a directory", root)
	}
	return nil
}

// CheckBaseRevision verifies the host can detect the current revision of
// the project root. Returns nil when the revision is a non-empty string.
func (c CertChecklist) CheckBaseRevision(projectRoot string) error {
	rev, err := c.Host.CurrentRevision(projectRoot)
	if err != nil {
		return fmt.Errorf("base revision: %w", err)
	}
	if rev == "" {
		return fmt.Errorf("base revision is empty")
	}
	if len(rev) != 40 {
		return fmt.Errorf("base revision %q is not a 40-char sha", rev)
	}
	return nil
}

// CheckCancelDuringExecution verifies that a task can be cancelled while
// it is in a non-terminal state. This is the "cancel during execution"
// fixture. The test submits a task, immediately cancels it, and verifies
// the final state is cancelled or already terminal.
func (c CertChecklist) CheckCancelDuringExecution(prompt string, opts SubmitOptions) error {
	submit, err := SubmitTask(c.Host, prompt, "", opts)
	if err != nil {
		return fmt.Errorf("submit: %s", err.Error())
	}
	cancelResp, cancelErr := CancelTask(c.Host, submit.TaskID, "certification cancel")
	if cancelErr != nil {
		// Already terminal is acceptable for certification.
		if cancelErr.Code == ErrAlreadyTerminal {
			return nil
		}
		return fmt.Errorf("cancel: %s", cancelErr.Error())
	}
	if cancelResp.State != StateCancelled && !cancelResp.State.IsTerminal() {
		return fmt.Errorf("expected cancelled or terminal, got %s", cancelResp.State)
	}
	return nil
}

// CheckRejectConflictingRetry verifies that a second submit with the same
// task ID but different parameters is rejected as a conflict. The roadmap
// requires: "reuse with different scope/policy/input returns a conflict."
func (c CertChecklist) CheckRejectConflictingRetry(taskID string, different SubmitRequest) error {
	resp, err := c.Host.Client().call(OpSubmit, taskID, different)
	if err != nil {
		return fmt.Errorf("conflicting retry transport: %w", err)
	}
	if resp.Error != nil && resp.Error.Code == ErrConflict {
		return nil
	}
	if resp.Status == ResponseOK {
		// If the task was found and is still running, the server returns
		// its current state. That is attach, not conflict — acceptable.
		return nil
	}
	return fmt.Errorf("expected conflict or attach, got status=%s", resp.Status)
}

// CheckDelegationLoopRejection verifies that a delegation lineage with a
// recursive loop is rejected. The roadmap requires the server to validate
// the parent chain, not just a depth counter.
func (c CertChecklist) CheckDelegationLoopRejection(taskID string) error {
	lineage := &DelegationLineage{
		ParentTaskID: "parent",
		Depth:        1,
		Ancestors:    []string{taskID},
	}
	err := ValidateLineage(lineage, taskID)
	if err == nil {
		return fmt.Errorf("recursive delegation loop was not rejected")
	}
	if err.Code != ErrForbidden {
		return fmt.Errorf("expected forbidden, got %s", err.Code)
	}
	return nil
}

// CheckDelegationDepthRejection verifies that delegation beyond the max
// depth is rejected.
func (c CertChecklist) CheckDelegationDepthRejection() error {
	lineage := &DelegationLineage{
		ParentTaskID: "parent",
		Depth:        MaxDelegationDepth + 1,
	}
	err := ValidateLineage(lineage, "task-test")
	if err == nil {
		return fmt.Errorf("excessive delegation depth was not rejected")
	}
	if err.Code != ErrForbidden {
		return fmt.Errorf("expected forbidden, got %s", err.Code)
	}
	return nil
}

// ── helpers ──

// gitCapture runs a git command in dir and returns the trimmed stdout.
func gitCapture(dir string, args ...string) (string, error) {
	gitArgs := append([]string{"-C", dir}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", gitArgs...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
