package captaincode

// Jido host integration (ROADMAP M4.4). Jido is an automation agent that
// delegates coding tasks to Captain through the task API and consumes
// events asynchronously. Unlike Pi (one-shot terminal), Jido maintains a
// long-running connection, polls the event stream, and manages task
// lifecycle across restarts.
//
// The integration pins a client/application example, handles polling or
// event consumption, defines restart ownership, and runs lifecycle tests.
//
// Jido's shape is the event-driven host: it submits a task and then polls
// the event stream (Events operation with cursor) rather than blocking on
// Inspect. When the host process restarts, it passes the last cursor back
// to resume observation — the roadmap's "disconnect resumes observation,
// not execution" principle.
//
// Restart ownership is explicit: a Jido process records its task IDs
// before it exits. On restart, it inspects each task to determine whether
// it is still running, interrupted, or terminal, and resumes interrupted
// tasks via the Resume operation.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// JidoAdapter is the host adapter for a Jido automation agent. It embeds
// HostAdapterBase and adds event-stream consumption and restart ownership.
type JidoAdapter struct {
	HostAdapterBase
	stateFile string
}

// NewJidoAdapter constructs a Jido adapter with the given task API client.
// The state file persists task IDs across restarts.
func NewJidoAdapter(client *TaskClient) *JidoAdapter {
	stateDir := os.Getenv("CAPTAIN_JIDO_STATE")
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".captaincode", "jido")
	}
	return &JidoAdapter{
		HostAdapterBase: NewHostAdapterBase(HostJido, client),
		stateFile:       filepath.Join(stateDir, "tasks.json"),
	}
}

// OnTaskSubmitted records the task ID to the state file so a restart can
// recover it. The state file is a simple JSON array of task IDs.
func (a *JidoAdapter) OnTaskSubmitted(taskID string, prompt string) {
	a.recordTask(taskID)
	fmt.Printf("jido: task %s submitted\n", taskID)
}

// OnTaskTerminal removes the task from the state file and prints the
// result. A terminal task no longer needs restart recovery.
func (a *JidoAdapter) OnTaskTerminal(taskID string, state LifecycleState, handoff *HandoffBrief) {
	a.removeTask(taskID)
	fmt.Printf("jido: task %s %s\n", taskID, state)
}

// recordTask appends a task ID to the state file.
func (a *JidoAdapter) recordTask(taskID string) {
	tasks := a.loadTasks()
	tasks = append(tasks, taskID)
	a.saveTasks(tasks)
}

// removeTask removes a task ID from the state file.
func (a *JidoAdapter) removeTask(taskID string) {
	tasks := a.loadTasks()
	var filtered []string
	for _, t := range tasks {
		if t != taskID {
			filtered = append(filtered, t)
		}
	}
	a.saveTasks(filtered)
}

// loadTasks reads the state file. Returns an empty slice when the file
// does not exist (first run or after cleanup).
func (a *JidoAdapter) loadTasks() []string {
	data, err := os.ReadFile(a.stateFile)
	if err != nil {
		return nil
	}
	var tasks []string
	json.Unmarshal(data, &tasks)
	return tasks
}

// saveTasks writes the state file.
func (a *JidoAdapter) saveTasks(tasks []string) {
	dir := filepath.Dir(a.stateFile)
	os.MkdirAll(dir, 0o755)
	data, _ := json.Marshal(tasks)
	os.WriteFile(a.stateFile, data, 0o644)
}

// RecoverOnStartup is the restart-ownership path. After a Jido process
// restart, it reads its state file, inspects each task, and reports which
// are still running, interrupted, or terminal. Interrupted tasks are
// candidates for ResumeTask.
type JidoRecovery struct {
	Running     []string
	Interrupted []string
	Terminal    []string
}

// RecoverOnStartup inspects all persisted task IDs and classifies them.
func (a *JidoAdapter) RecoverOnStartup() JidoRecovery {
	tasks := a.loadTasks()
	var rec JidoRecovery
	for _, taskID := range tasks {
		resp, err := a.Client().Inspect(taskID, InspectRequest{})
		if err != nil {
			rec.Terminal = append(rec.Terminal, taskID)
			continue
		}
		if resp.Task == nil {
			rec.Terminal = append(rec.Terminal, taskID)
			continue
		}
		state := resp.Task.State
		switch {
		case state.IsTerminal():
			rec.Terminal = append(rec.Terminal, taskID)
		case state == StateInterrupted:
			rec.Interrupted = append(rec.Interrupted, taskID)
		default:
			rec.Running = append(rec.Running, taskID)
		}
	}
	return rec
}

// ConsumeTaskEvents polls the event stream for a task until it reaches a
// terminal state. This is the event-consumption pattern Jido uses instead
// of blocking on Inspect. The cursor is persisted between polls so a
// disconnect resumes observation.
func (a *JidoAdapter) ConsumeTaskEvents(taskID string, cursor string, pollInterval, timeout time.Duration) ([]TaskEvent, string, LifecycleState) {
	deadline := time.Now().Add(timeout)
	var all []TaskEvent
	for time.Now().Before(deadline) {
		events, nextCursor, err := a.Client().ConsumeEvents(taskID, cursor)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		all = append(all, events...)
		cursor = nextCursor
		resp, err := a.Client().Inspect(taskID, InspectRequest{})
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		if resp.Task != nil && resp.Task.State.IsTerminal() {
			return all, cursor, resp.Task.State
		}
		if nextCursor == "" {
			time.Sleep(pollInterval)
		}
	}
	return all, cursor, ""
}

// JidoRun is the one-shot entry point: submit, consume events to terminal,
// print result.
func JidoRun(prompt string, opts SubmitOptions, pollInterval, pollTimeout time.Duration) (*LifecycleResult, *TaskError) {
	adapter := NewJidoAdapter(DefaultTaskClient())
	submit, err := SubmitTask(adapter, prompt, "", opts)
	if err != nil {
		return nil, err
	}
	events, _, state := adapter.ConsumeTaskEvents(submit.TaskID, "", pollInterval, pollTimeout)
	if state == "" {
		return &LifecycleResult{TaskID: submit.TaskID, Events: events}, NewTaskError(ErrUnavailable, "task did not reach terminal within timeout")
	}
	handoff := retrieveHandoff(adapter.Client(), submit.TaskID)
	arts, _ := RetrieveArtifacts(adapter, submit.TaskID, true)
	adapter.OnTaskTerminal(submit.TaskID, state, handoff)
	return &LifecycleResult{
		TaskID:    submit.TaskID,
		State:     state,
		Handoff:   handoff,
		Artifacts: arts,
		Events:    events,
	}, nil
}

// JidoRecover prints the recovery report for persisted tasks.
func JidoRecover() error {
	adapter := NewJidoAdapter(DefaultTaskClient())
	rec := adapter.RecoverOnStartup()
	out, _ := json.MarshalIndent(rec, "", "  ")
	fmt.Println(string(out))
	return nil
}
