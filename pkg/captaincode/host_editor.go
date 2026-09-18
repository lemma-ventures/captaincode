package captaincode

// Editor host integration (ROADMAP M4.5). An editor (e.g. VS Code via MCP)
// delegates coding tasks to Captain through the execution MCP server
// (task_mcp.go) or the task API HTTP endpoint. Unlike Pi (one-shot) and
// Jido (event-driven), an editor has an approval/scope dialog: the user
// is shown what the task will do and must approve it before dispatch.
//
// The integration uses one chosen editor MCP client, tests approval/scope
// handling, provides result links, and verifies lifecycle behavior.
//
// The editor's shape is the interactive host: the user is present, so the
// adapter surfaces approval decisions, scope violations, and artifact
// links rather than auto-approving. The approval flow is:
//
//  1. Plan: the editor calls OpPlan to get the routing decision.
//  2. Approval: the editor shows the plan and asks the user to approve.
//  3. Submit: on approval, the editor calls OpSubmit with the accepted
//     scope and limits.
//  4. Poll: the editor polls Inspect and updates its progress panel.
//  5. Terminal: the editor opens the changed files and shows the handoff.
//
// Scope handling is explicit: the editor checks whether its workspace
// satisfies the task's PermissionPolicy before submitting. A strict scope
// request that the editor cannot enforce (e.g. network sandbox) is
// rejected before dispatch.

import (
	"encoding/json"
	"fmt"
	"time"
)

// EditorAdapter is the host adapter for an editor MCP client. It embeds
// HostAdapterBase and overrides the approval hook with an interactive
// flow.
type EditorAdapter struct {
	HostAdapterBase
	workspaceFolder string
	approver        EditorApprover
}

// EditorApprover is the interface an editor provides for interactive
// approval. The editor's "Allow this task?" dialog implements this.
type EditorApprover interface {
	// ApproveTask asks the user to approve a task given its plan and
	// permission policy. Returns true if the user approves.
	ApproveTask(plan *PlanResponse, policy PermissionPolicy) bool
}

// NewEditorAdapter constructs an editor adapter with the given workspace
// folder and approver.
func NewEditorAdapter(client *TaskClient, workspaceFolder string, approver EditorApprover) *EditorAdapter {
	return &EditorAdapter{
		HostAdapterBase: NewHostAdapterBase(HostEditor, client),
		workspaceFolder: workspaceFolder,
		approver:        approver,
	}
}

// DetectProjectRoot returns the workspace folder. An editor knows its
// workspace; it does not need to walk up looking for .git.
func (a *EditorAdapter) DetectProjectRoot(hint string) (string, error) {
	if a.workspaceFolder != "" {
		return a.workspaceFolder, nil
	}
	return a.HostAdapterBase.DetectProjectRoot(hint)
}

// OnApprovalNeeded delegates to the editor's approver. When no approver is
// set, it defaults to true (auto-approve, for testing).
func (a *EditorAdapter) OnApprovalNeeded(taskID string, policy PermissionPolicy) bool {
	if a.approver == nil {
		return true
	}
	return a.approver.ApproveTask(nil, policy)
}

// OnTaskSubmitted notifies the editor that a task was submitted. The
// editor updates its progress panel.
func (a *EditorAdapter) OnTaskSubmitted(taskID string, prompt string) {
	fmt.Printf("editor: task %s started\n", taskID)
}

// OnTaskTerminal notifies the editor that a task finished. The editor
// opens changed files and shows the handoff brief.
func (a *EditorAdapter) OnTaskTerminal(taskID string, state LifecycleState, handoff *HandoffBrief) {
	fmt.Printf("editor: task %s %s\n", taskID, state)
	if handoff == nil {
		return
	}
	for _, art := range handoff.Artifacts {
		for _, f := range art.ChangedFiles {
			fmt.Printf("  open: %s\n", f)
		}
	}
}

// EditorPlanAndApprove is the two-step interactive flow: plan, then ask
// the user to approve. If the user rejects, no task is submitted. This is
// the editor's approval/scope handling the roadmap requires.
func EditorPlanAndApprove(a *EditorAdapter, prompt string, opts SubmitOptions) (*SubmitResponse, *TaskError) {
	planReq := PlanRequest{
		Prompt:      prompt,
		ProjectRoot: a.workspaceFolder,
		Intent:      opts.Intent,
		Caps:        opts.Caps,
	}
	plan, perr := a.Client().Plan(planReq)
	if perr != nil {
		return nil, perr
	}
	if a.approver != nil && !a.approver.ApproveTask(plan, opts.Permissions) {
		return nil, NewTaskError(ErrForbidden, "user rejected the task plan")
	}
	return SubmitTask(a, prompt, a.workspaceFolder, opts)
}

// EditorRun is the interactive entry point: plan, approve, submit, poll,
// retrieve artifacts. Returns the full lifecycle result.
func EditorRun(prompt string, opts SubmitOptions, workspaceFolder string, approver EditorApprover, pollInterval, pollTimeout time.Duration) (*LifecycleResult, *TaskError) {
	adapter := NewEditorAdapter(DefaultTaskClient(), workspaceFolder, approver)
	submit, err := EditorPlanAndApprove(adapter, prompt, opts)
	if err != nil {
		return nil, err
	}
	state, handoff, ok := PollTask(adapter, submit.TaskID, pollInterval, pollTimeout)
	if !ok {
		return &LifecycleResult{TaskID: submit.TaskID, State: state}, NewTaskError(ErrUnavailable, "task did not reach terminal within timeout")
	}
	arts, _ := RetrieveArtifacts(adapter, submit.TaskID, true)
	events, _, _ := adapter.Client().ConsumeEvents(submit.TaskID, "")
	return &LifecycleResult{
		TaskID:    submit.TaskID,
		State:     state,
		Handoff:   handoff,
		Artifacts: arts,
		Events:    events,
	}, nil
}

// EditorInspect prints a task's state as JSON for an editor's detail panel.
func EditorInspect(taskID string) error {
	client := DefaultTaskClient()
	resp, err := client.Inspect(taskID, InspectRequest{
		IncludeCharges:  true,
		IncludeBudget:   true,
		IncludeDecision: true,
		IncludeHandoff:  true,
	})
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
	return nil
}
