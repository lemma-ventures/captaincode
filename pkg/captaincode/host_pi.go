package captaincode

// Pi host integration (ROADMAP M4.3). Pi is a lightweight terminal host
// that delegates coding tasks to Captain through the task API. The
// integration pins a supported extension path, handles project-root
// detection, displays artifacts, and runs the lifecycle certification
// suite.
//
// Pi's shape is the simplest host: it has no approval dialog (the operator
// is already at a terminal), no editor workspace concept, and no event
// streaming consumer. It submits a task, polls to terminal, and prints the
// result. The value is that the same task API surface a Jido agent or an
// editor uses is available from a device or a CI runner that has nothing
// but curl and git.
//
// The extension path is pinned: Pi does not auto-discover the brain. The
// operator sets CAPTAIN_BRAIN_URL (or accepts the localhost default) and
// CAPTAIN_TASK_TOKEN (or accepts loopback-only). The pinned extension path
// is the install recipe, not a runtime discovery protocol.

import (
	"encoding/json"
	"fmt"
	"time"
)

// PiAdapter is the host adapter for a Pi terminal host. It embeds
// HostAdapterBase for the shared lifecycle and overrides the hooks that
// matter to a terminal: OnTaskSubmitted prints the task ID, OnTaskTerminal
// prints the result summary.
type PiAdapter struct {
	HostAdapterBase
}

// NewPiAdapter constructs a Pi adapter with the given task API client.
func NewPiAdapter(client *TaskClient) *PiAdapter {
	return &PiAdapter{
		HostAdapterBase: NewHostAdapterBase(HostPi, client),
	}
}

// OnTaskSubmitted prints the task ID and prompt to stdout. A terminal host
// does not have a notification system; the task ID is the handle the
// operator uses to inspect, cancel, or resume.
func (a *PiAdapter) OnTaskSubmitted(taskID string, prompt string) {
	shortPrompt := prompt
	if len(shortPrompt) > 80 {
		shortPrompt = shortPrompt[:77] + "..."
	}
	fmt.Printf("pi: task %s submitted (%s)\n", taskID, shortPrompt)
}

// OnTaskTerminal prints the task's final state and, if available, the
// handoff brief's artifact summary. A terminal host shows the changed
// files and check results rather than opening an editor.
func (a *PiAdapter) OnTaskTerminal(taskID string, state LifecycleState, handoff *HandoffBrief) {
	fmt.Printf("pi: task %s %s\n", taskID, state)
	if handoff == nil {
		return
	}
	for _, art := range handoff.Artifacts {
		fmt.Printf("  changed: %d files (%s)\n", len(art.ChangedFiles), art.DiffDigest)
	}
	for _, fc := range handoff.FailedChecks {
		fmt.Printf("  failed:  %s (exit %d)\n", fc.Command, fc.ExitCode)
	}
	if handoff.Budget != nil {
		fmt.Printf("  budget:  %d attempts, $%.4f\n", handoff.Budget.AttemptsUsed, handoff.Budget.CostUSD)
	}
}

// PiRun is the one-shot entry point: submit a task, poll to terminal,
// retrieve artifacts, and print the result. This is the shape a CI runner
// or a device script calls.
func PiRun(prompt string, opts SubmitOptions, pollInterval, pollTimeout time.Duration) (*LifecycleResult, *TaskError) {
	adapter := NewPiAdapter(DefaultTaskClient())
	return FullLifecycle(adapter, prompt, opts, pollInterval, pollTimeout)
}

// PiInspect prints a task's full state as JSON. This is the shape a
// terminal host uses to inspect a task after a disconnect.
func PiInspect(taskID string) error {
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

// PiArtifacts prints a task's artifacts as JSON.
func PiArtifacts(taskID string) error {
	client := DefaultTaskClient()
	resp, err := client.Artifacts(taskID, true)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
	return nil
}

// PiCancel cancels a task.
func PiCancel(taskID string, reason string) error {
	client := DefaultTaskClient()
	resp, err := client.Cancel(taskID, reason)
	if err != nil {
		return err
	}
	fmt.Printf("cancelled: %s (state: %s)\n", taskID, resp.State)
	return nil
}

// PiResume resumes an interrupted task.
func PiResume(taskID string, attemptID string) error {
	client := DefaultTaskClient()
	resp, err := client.Resume(taskID, attemptID, "pi resume")
	if err != nil {
		return err
	}
	fmt.Printf("resumed: %s (attempt: %s, state: %s)\n", taskID, resp.AttemptID, resp.State)
	return nil
}
