package main

// `captain handoff` (ROADMAP M3.5).
//
//	captain handoff <task-id>              # show the stored brief for one task
//	captain handoff                        # list all stored briefs
//	captain handoff <task-id> --build      # rebuild and store from current ledger
//	captain handoff <task-id> --format json # full brief as JSON for M4 hosts

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func cmdHandoff(args []string) {
	c := &http.Client{Timeout: 10 * time.Second}
	build := false
	jsonFormat := false
	var taskID string
	for _, a := range args {
		if a == "--build" {
			build = true
		} else if a == "--format" || strings.HasPrefix(a, "--format=") {
			if strings.HasPrefix(a, "--format=") {
				jsonFormat = strings.TrimPrefix(a, "--format=") == "json"
			} else {
				jsonFormat = true
			}
		} else if taskID == "" {
			taskID = strings.TrimSpace(a)
		}
	}
	if build && taskID != "" {
		url := fmt.Sprintf("http://127.0.0.1:14097/v1/handoff?task=%s", taskID)
		resp, err := c.Post(url, "application/json", strings.NewReader("{}"))
		if err != nil {
			fatal(fmt.Errorf("brain not reachable: %w", err))
		}
		defer resp.Body.Close()
		var brief struct {
			TaskID string `json:"task_id"`
			State  string `json:"state"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&brief)
		fmt.Printf("built handoff for task %s — %s\n", brief.TaskID, brief.State)
		return
	}
	if taskID != "" {
		resp, err := c.Get("http://127.0.0.1:14097/v1/handoff?task=" + taskID)
		if err != nil {
			fatal(fmt.Errorf("brain not reachable: %w", err))
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			fmt.Printf("no handoff for task %s.\n", taskID)
			return
		}
		body, _ := io.ReadAll(resp.Body)
		if jsonFormat {
			fmt.Println(string(body))
			return
		}
		var brief struct {
			TaskID       string `json:"task_id"`
			State        string `json:"state"`
			Requirements string `json:"requirements"`
		}
		_ = json.Unmarshal(body, &brief)
		fmt.Printf("task %s — %s\n", brief.TaskID, brief.State)
		if brief.Requirements != "" {
			peek := brief.Requirements
			if len(peek) > 200 {
				peek = peek[:200] + "…"
			}
			fmt.Printf("  requirements: %s\n", peek)
		}
		return
	}
	resp, err := c.Get("http://127.0.0.1:14097/v1/handoff")
	if err != nil {
		fatal(fmt.Errorf("brain not reachable: %w", err))
	}
	defer resp.Body.Close()
	var out struct {
		Count  int `json:"count"`
		Briefs []struct {
			TaskID string `json:"task_id"`
			State  string `json:"state"`
		} `json:"briefs"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Count == 0 {
		fmt.Println("no handoff briefs stored.")
		return
	}
	fmt.Printf("%d handoff brief(s):\n", out.Count)
	for _, h := range out.Briefs {
		fmt.Printf("  · %s — %s\n", h.TaskID, h.State)
	}
}
