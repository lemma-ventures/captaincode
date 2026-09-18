package main

// `captain resume <task-id>` (ROADMAP M3.4).
//
//	captain resume <task-id>   # resume an interrupted task: create a new
//	                          # attempt with a causal link to the interrupted
//	                          # one, under the same task and remaining budget
//	captain resume             # list interrupted tasks awaiting a decision
//
// The brain serves POST /v1/resume?task=<id> so the CLI and the TUI can
// resume an interrupted task while the brain is running, without a restart.
// The policy recommends (captain lifecycle shows interrupted attempts); the
// user decides (this command acts).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func cmdResume(args []string) {
	c := &http.Client{Timeout: 10 * time.Second}
	if len(args) == 0 {
		resp, err := c.Get("http://127.0.0.1:14097/v1/lifecycle")
		if err != nil {
			fatal(fmt.Errorf("brain not reachable: %w", err))
		}
		defer resp.Body.Close()
		var out struct {
			Interrupted  int    `json:"interrupted"`
			ResumeReport string `json:"resume_report"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if out.Interrupted == 0 {
			fmt.Println("no interrupted tasks.")
			return
		}
		fmt.Print(out.ResumeReport)
		fmt.Println("\nresume one: captain resume <task-id>")
		return
	}
	taskID := strings.TrimSpace(args[0])
	resp, err := c.Post(fmt.Sprintf("http://127.0.0.1:14097/v1/resume?task=%s", taskID), "application/json", strings.NewReader("{}"))
	if err != nil {
		fatal(fmt.Errorf("brain not reachable: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		fatal(fmt.Errorf("resume failed: %s", e.Error))
	}
	var out struct {
		TaskID        string `json:"task_id"`
		AttemptID     string `json:"attempt_id"`
		ParentAttempt string `json:"parent_attempt"`
		State         string `json:"state"`
		Leg           string `json:"leg"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("resumed task %s\n", out.TaskID)
	fmt.Printf("  new attempt: %s (state: %s)\n", out.AttemptID, out.State)
	if out.Leg != "" {
		fmt.Printf("  leg: %s\n", out.Leg)
	}
	if out.ParentAttempt != "" {
		fmt.Printf("  resumed from: %s\n", out.ParentAttempt)
	}
}
