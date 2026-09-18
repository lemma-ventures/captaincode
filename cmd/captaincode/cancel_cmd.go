package main

// `captain cancel` and `captain lifecycle` (ROADMAP M3.4).
//
//	captain cancel <task-id>   # cancel a task and all its nested work
//	captain cancel             # list what's in flight
//	captain lifecycle           # summary of task/attempt states
//	captain lifecycle <task-id> # one task's states and attempts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func cmdCancel(args []string) {
	c := &http.Client{Timeout: 10 * time.Second}
	if len(args) == 0 {
		resp, err := c.Get("http://127.0.0.1:14097/v1/cancel")
		if err != nil {
			fatal(fmt.Errorf("brain not reachable: %w", err))
		}
		defer resp.Body.Close()
		var out struct {
			Count int `json:"count"`
			Tasks []struct {
				TaskID string   `json:"task_id"`
				Active []string `json:"active"`
			} `json:"tasks"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if out.Count == 0 {
			fmt.Println("no tasks in flight.")
			return
		}
		fmt.Printf("%d task(s) in flight:\n", out.Count)
		for _, t := range out.Tasks {
			fmt.Printf("  · %s (%s)\n", t.TaskID, strings.Join(t.Active, ", "))
		}
		fmt.Println("\ncancel one: captain cancel <task-id>")
		return
	}
	taskID := strings.TrimSpace(args[0])
	url := fmt.Sprintf("http://127.0.0.1:14097/v1/cancel?task=%s", taskID)
	resp, err := c.Post(url, "application/json", strings.NewReader("{}"))
	if err != nil {
		fatal(fmt.Errorf("brain not reachable: %w", err))
	}
	defer resp.Body.Close()
	var out struct {
		TaskID    string   `json:"task_id"`
		Cancelled []string `json:"cancelled"`
		Pending   []string `json:"pending"`
		Count     int      `json:"count"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Count == 0 {
		fmt.Printf("task %s has no in-flight work (idle or already finished).\n", taskID)
		return
	}
	fmt.Printf("cancelled %d item(s) under task %s:\n", out.Count, taskID)
	for _, label := range out.Cancelled {
		fmt.Printf("  · %s\n", label)
	}
	if len(out.Pending) > 0 {
		fmt.Printf("\n%d process(es) killed after deadline (did not stop in time):\n", len(out.Pending))
		for _, label := range out.Pending {
			fmt.Printf("  · %s\n", label)
		}
	}
}

func cmdLifecycle(args []string) {
	c := &http.Client{Timeout: 5 * time.Second}
	if len(args) > 0 {
		taskID := strings.TrimSpace(args[0])
		resp, err := c.Get("http://127.0.0.1:14097/v1/lifecycle?task=" + taskID)
		if err != nil {
			fatal(fmt.Errorf("brain not reachable: %w", err))
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			fmt.Printf("task %s not found.\n", taskID)
			return
		}
		var out struct {
			Task struct {
				TaskID    string `json:"task_id"`
				State     string `json:"state"`
				Label     string `json:"label"`
				StartedAt string `json:"started_at"`
			} `json:"task"`
			Attempts []struct {
				AttemptID string `json:"attempt_id"`
				State     string `json:"state"`
				Leg       string `json:"leg"`
				ProcessID string `json:"process_id"`
			} `json:"attempts"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		fmt.Printf("task %s — %s\n", out.Task.TaskID, out.Task.State)
		if out.Task.Label != "" {
			fmt.Printf("  label: %s\n", out.Task.Label)
		}
		fmt.Printf("  started: %s\n", out.Task.StartedAt)
		for _, a := range out.Attempts {
			fmt.Printf("  attempt %s — %s (leg: %s, owner: %s)\n", a.AttemptID, a.State, a.Leg, a.ProcessID)
		}
		return
	}
	resp, err := c.Get("http://127.0.0.1:14097/v1/lifecycle")
	if err != nil {
		fatal(fmt.Errorf("brain not reachable: %w", err))
	}
	defer resp.Body.Close()
	var out struct {
		TaskCount    int            `json:"task_count"`
		AttemptCount int            `json:"attempt_count"`
		ByState      map[string]int `json:"by_state"`
		Interrupted  int            `json:"interrupted"`
		ResumeReport string         `json:"resume_report"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("%d task(s), %d attempt(s)", out.TaskCount, out.AttemptCount)
	if out.Interrupted > 0 {
		fmt.Printf(", %d interrupted", out.Interrupted)
	}
	fmt.Println()
	for state, n := range out.ByState {
		if n > 0 {
			fmt.Printf("  %s: %d\n", state, n)
		}
	}
	if out.ResumeReport != "" {
		fmt.Print("\n" + out.ResumeReport)
	}
}
