package main

// `captain task` CLI (ROADMAP M4.1).
//
//	captain task plan <prompt>                     # plan a task without executing
//	captain task submit <prompt>                   # submit a task for execution
//	captain task inspect <task-id>                 # read task state
//	captain task events <task-id>                  # read task events
//	captain task artifacts <task-id>               # read patch manifests
//	captain task cancel <task-id>                  # cancel a task
//	captain task resume <task-id> <attempt-id>     # resume an interrupted attempt

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdTask(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: captain task <plan|submit|inspect|events|artifacts|cancel|resume|mcp|fixture> ...")
		os.Exit(2)
	}
	op := strings.ToLower(args[0])
	rest := args[1:]
	switch op {
	case "plan":
		cmdTaskPlan(rest)
	case "submit":
		cmdTaskSubmit(rest)
	case "inspect":
		cmdTaskInspect(rest)
	case "events":
		cmdTaskEvents(rest)
	case "artifacts":
		cmdTaskArtifacts(rest)
	case "cancel":
		cmdTaskCancel(rest)
	case "resume":
		cmdTaskResume(rest)
	case "mcp":
		cmdTaskMCP()
	case "fixture":
		cmdTaskFixture(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown task op: %s\n", op)
		os.Exit(2)
	}
}

func cmdTaskPlan(args []string) {
	fs := flag.NewFlagSet("task plan", flag.ExitOnError)
	prefer := fs.String("prefer", "", "quality | speed | save | frontier")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: captain task plan <prompt>")
		os.Exit(2)
	}
	prompt := strings.Join(fs.Args(), " ")
	body := captaincode.PlanRequest{
		Prompt: prompt,
		Intent: captaincode.TaskIntent{Prefer: *prefer},
	}
	if *prefer == "frontier" {
		body.Intent.Frontier = true
	}
	bodyJSON, _ := json.Marshal(body)
	sendTaskRequest(captaincode.OpPlan, "", bodyJSON, func(resp *captaincode.TaskResponse) {
		var plan captaincode.PlanResponse
		json.Unmarshal(resp.Body, &plan)
		fmt.Printf("leg: %s\n", plan.Leg)
		fmt.Printf("class: %s\n", plan.Class)
		if plan.Domain != "" {
			fmt.Printf("domain: %s\n", plan.Domain)
		}
		fmt.Printf("rationale: %s\n", plan.Rationale)
	})
}

func cmdTaskSubmit(args []string) {
	fs := flag.NewFlagSet("task submit", flag.ExitOnError)
	leg := fs.String("leg", "", "force a specific leg")
	taskID := fs.String("task-id", "", "reuse an existing task ID")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: captain task submit <prompt>")
		os.Exit(2)
	}
	prompt := strings.Join(fs.Args(), " ")
	body := captaincode.SubmitRequest{
		Prompt: prompt,
		Leg:    captaincode.Leg(*leg),
	}
	bodyJSON, _ := json.Marshal(body)
	sendTaskRequest(captaincode.OpSubmit, *taskID, bodyJSON, func(resp *captaincode.TaskResponse) {
		var sub captaincode.SubmitResponse
		json.Unmarshal(resp.Body, &sub)
		fmt.Printf("task_id: %s\n", sub.TaskID)
		fmt.Printf("state: %s\n", sub.State)
		if sub.Attempt != "" {
			fmt.Printf("attempt: %s\n", sub.Attempt)
		}
	})
}

func cmdTaskInspect(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: captain task inspect <task-id>")
		os.Exit(2)
	}
	taskID := strings.TrimSpace(args[0])
	body := captaincode.InspectRequest{
		IncludeCharges:  true,
		IncludeBudget:   true,
		IncludeDecision: true,
		IncludeHandoff:  true,
	}
	bodyJSON, _ := json.Marshal(body)
	sendTaskRequest(captaincode.OpInspect, taskID, bodyJSON, func(resp *captaincode.TaskResponse) {
		var insp captaincode.InspectResponse
		json.Unmarshal(resp.Body, &insp)
		if insp.Task != nil {
			fmt.Printf("task: %s — %s\n", insp.Task.TaskID, insp.Task.State)
			if insp.Task.StopReason != "" {
				fmt.Printf("  stop: %s\n", insp.Task.StopReason)
			}
		}
		for _, a := range insp.Attempts {
			fmt.Printf("  attempt %s — %s (leg: %s)\n", a.AttemptID, a.State, a.Leg)
		}
		if insp.Budget != nil {
			fmt.Printf("  budget: %d/%d attempts", insp.Budget.SettledAttempts, insp.Budget.MaxAttempts)
			if insp.Budget.SettledCostUSD > 0 {
				fmt.Printf(", $%.4f", insp.Budget.SettledCostUSD)
			}
			fmt.Println()
		}
		if insp.Decision != nil {
			fmt.Printf("  decision: %s (path: %s)\n", insp.Decision.Chosen, insp.Decision.Path)
		}
		if insp.Handoff != nil {
			fmt.Print("\n" + captaincode.FormatHandoffBrief(*insp.Handoff))
		}
	})
}

func cmdTaskEvents(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: captain task events <task-id>")
		os.Exit(2)
	}
	taskID := strings.TrimSpace(args[0])
	body := captaincode.EventsRequest{Limit: 50}
	bodyJSON, _ := json.Marshal(body)
	sendTaskRequest(captaincode.OpEvents, taskID, bodyJSON, func(resp *captaincode.TaskResponse) {
		var evs captaincode.EventsResponse
		json.Unmarshal(resp.Body, &evs)
		if len(evs.Events) == 0 {
			fmt.Println("no events.")
			return
		}
		for _, e := range evs.Events {
			fmt.Printf("  %s [%s] %s", e.At.Format("15:04:05"), e.Kind, e.Leg)
			if e.Detail != "" {
				fmt.Printf(" — %s", truncate(e.Detail, 60))
			}
			fmt.Println()
		}
		if evs.HasMore {
			fmt.Printf("\nmore events: captain task events %s (cursor: %s)\n", taskID, evs.NextCursor)
		}
	})
}

func cmdTaskArtifacts(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: captain task artifacts <task-id>")
		os.Exit(2)
	}
	taskID := strings.TrimSpace(args[0])
	bodyJSON, _ := json.Marshal(captaincode.ArtifactsRequest{})
	sendTaskRequest(captaincode.OpArtifacts, taskID, bodyJSON, func(resp *captaincode.TaskResponse) {
		var arts captaincode.ArtifactsResponse
		json.Unmarshal(resp.Body, &arts)
		if arts.Integration == nil {
			fmt.Println("no artifacts for task.")
			return
		}
		fmt.Printf("integration: %s\n", arts.Integration.Status)
		for _, m := range arts.Manifests {
			fmt.Printf("  · %s — %d file(s)", m.Leg, len(m.ChangedFiles))
			if m.Check != nil {
				if m.Check.Passed {
					fmt.Print(" [check passed]")
				} else {
					fmt.Print(" [check failed]")
				}
			}
			fmt.Println()
		}
		if len(arts.Integration.Conflicts) > 0 {
			fmt.Println("conflicts:")
			for _, c := range arts.Integration.Conflicts {
				fmt.Printf("  · %s (%s)\n", c.File, strings.Join(c.Workers, ", "))
			}
		}
		if arts.Integration.Winner != "" {
			fmt.Printf("director's call: %s's changes land - %s\n", arts.Integration.Winner, arts.Integration.Ruling)
			if len(arts.Integration.Dropped) > 0 {
				fmt.Printf("set aside: %s\n", strings.Join(arts.Integration.Dropped, ", "))
			}
		}
	})
}

func cmdTaskCancel(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: captain task cancel <task-id>")
		os.Exit(2)
	}
	taskID := strings.TrimSpace(args[0])
	bodyJSON, _ := json.Marshal(captaincode.CancelRequest{})
	sendTaskRequest(captaincode.OpCancel, taskID, bodyJSON, func(resp *captaincode.TaskResponse) {
		var can captaincode.CancelResponse
		json.Unmarshal(resp.Body, &can)
		fmt.Printf("task %s — %s\n", can.TaskID, can.State)
		if len(can.Cancelled) > 0 {
			fmt.Printf("cancelled %d item(s):\n", len(can.Cancelled))
			for _, label := range can.Cancelled {
				fmt.Printf("  · %s\n", label)
			}
		} else {
			fmt.Println("no in-flight work.")
		}
	})
}

func cmdTaskResume(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: captain task resume <task-id> <attempt-id>")
		os.Exit(2)
	}
	taskID := strings.TrimSpace(args[0])
	attemptID := strings.TrimSpace(args[1])
	bodyJSON, _ := json.Marshal(captaincode.ResumeRequest{AttemptID: attemptID})
	sendTaskRequest(captaincode.OpResume, taskID, bodyJSON, func(resp *captaincode.TaskResponse) {
		var res captaincode.ResumeResponse
		json.Unmarshal(resp.Body, &res)
		fmt.Printf("task %s — %s\n", res.TaskID, res.State)
		fmt.Printf("attempt: %s\n", res.AttemptID)
		if res.ParentAttempt != "" {
			fmt.Printf("resumed from: %s\n", res.ParentAttempt)
		}
		if res.Budget != nil {
			fmt.Printf("budget: %d/%d attempts\n", res.Budget.SettledAttempts, res.Budget.MaxAttempts)
		}
	})
}

func sendTaskRequest(op captaincode.Operation, taskID string, bodyJSON []byte, onOK func(*captaincode.TaskResponse)) {
	req := captaincode.TaskRequest{
		Version:   captaincode.TaskAPIVersion,
		Op:        op,
		RequestID: captaincode.NewRequestID(),
		TaskID:    taskID,
		Body:      bodyJSON,
	}
	reqJSON, _ := json.Marshal(req)
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Post("http://127.0.0.1:14097/v1/task", "application/json", bytes.NewReader(reqJSON))
	if err != nil {
		fatal(fmt.Errorf("brain not reachable: %w", err))
	}
	defer resp.Body.Close()
	var tresp captaincode.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&tresp); err != nil {
		fatal(fmt.Errorf("bad response: %w", err))
	}
	if tresp.Status == captaincode.ResponseError && tresp.Error != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %s\n", tresp.Error.Code, tresp.Error.Message)
		os.Exit(1)
	}
	onOK(&tresp)
}
