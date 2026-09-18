package main

// `captain task fixture` (ROADMAP M4.1). A standalone contract verification
// program that a host adapter developer runs against a live brain endpoint to
// confirm the task API speaks the envelope, error taxonomy, and lifecycle
// their adapter depends on.
//
// The fixture is the executable companion to the 17 unit tests in
// taskapi_test.go and the compatibility policy in TASK_API_COMPATIBILITY.md.
// The unit tests exercise the schema; the fixture exercises the wire — a live
// brain at CAPTAIN_BRAIN_URL (default http://127.0.0.1:14097) with
// CAPTAIN_TASK_TOKEN if set.
//
//	captain task fixture              # run all checks, exit 0 on pass, 1 on fail
//	captain task fixture --json       # machine-readable JSON output
//	captain task fixture --prompt X   # override the test prompt

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type fixtureStep struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass, fail, skip
	Detail string `json:"detail"`
}

type fixtureReport struct {
	BrainURL string        `json:"brain_url"`
	Steps    []fixtureStep `json:"steps"`
	Passed   int           `json:"passed"`
	Failed   int           `json:"failed"`
	Skipped  int           `json:"skipped"`
}

func cmdTaskFixture(args []string) {
	fs := flag.NewFlagSet("task fixture", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
	prompt := fs.String("prompt", "Add a doc comment to the main function", "test prompt for plan/submit")
	_ = fs.Parse(args)

	client := captaincode.DefaultTaskClient()
	report := runFixtureAgainstClient(client.BaseURL, client.Token, *prompt)
	report.finish(*jsonOut)
}

// runFixtureAgainstClient is the testable core of `captain task fixture`: it
// runs the full contract verification step sequence against a brain URL and
// returns the report without exiting. The command wraps it with flag parsing
// and output; the tests wrap it with a httptest server.
func runFixtureAgainstClient(brainURL, token, prompt string) fixtureReport {
	client := captaincode.NewTaskClient(brainURL, token)
	r := fixtureReport{BrainURL: brainURL}

	r.run("version_compatibility", func() (string, error) {
		if e := captaincode.CheckCompatibility(captaincode.TaskAPIVersion); e != nil {
			return "", fmt.Errorf("current version rejected: %s", e.Code)
		}
		if e := captaincode.CheckCompatibility(captaincode.TaskAPIVersion + 1); e == nil {
			return "", fmt.Errorf("future version should be rejected")
		}
		return fmt.Sprintf("v%d (min %d)", captaincode.TaskAPIVersion, captaincode.TaskAPIMinVersion), nil
	})

	var planResp *captaincode.PlanResponse
	r.run("plan", func() (string, error) {
		p, e := client.Plan(captaincode.PlanRequest{
			Prompt: prompt,
			Intent: captaincode.TaskIntent{Prefer: "quality"},
		})
		if e != nil {
			return "", fmt.Errorf("%s: %s", e.Code, e.Message)
		}
		planResp = p
		return fmt.Sprintf("leg=%s class=%s", p.Leg, p.Class), nil
	})

	var taskID string
	r.run("submit", func() (string, error) {
		s, e := client.Submit(captaincode.SubmitRequest{Prompt: prompt})
		if e != nil {
			return "", fmt.Errorf("%s: %s", e.Code, e.Message)
		}
		if s.TaskID == "" {
			return "", fmt.Errorf("empty task_id in submit response")
		}
		taskID = s.TaskID
		return fmt.Sprintf("task=%s state=%s", s.TaskID, s.State), nil
	})

	if taskID == "" {
		skipMsg := "skipped: no task_id from submit"
		r.run("inspect", func() (string, error) { return "", fmt.Errorf("%s", skipMsg) })
		r.run("events", func() (string, error) { return "", fmt.Errorf("%s", skipMsg) })
		r.run("artifacts", func() (string, error) { return "", fmt.Errorf("%s", skipMsg) })
		r.run("cancel", func() (string, error) { return "", fmt.Errorf("%s", skipMsg) })
		r.run("error_not_found", func() (string, error) { return "", fmt.Errorf("%s", skipMsg) })
		return r
	}

	r.run("inspect", func() (string, error) {
		insp, e := client.Inspect(taskID, captaincode.InspectRequest{
			IncludeCharges:  true,
			IncludeBudget:   true,
			IncludeDecision: true,
			IncludeHandoff:  true,
		})
		if e != nil {
			return "", fmt.Errorf("%s: %s", e.Code, e.Message)
		}
		if insp.Task == nil {
			return "", fmt.Errorf("nil task in inspect response")
		}
		return fmt.Sprintf("state=%s", insp.Task.State), nil
	})

	r.run("events", func() (string, error) {
		evs, e := client.Events(taskID, "", 50)
		if e != nil {
			return "", fmt.Errorf("%s: %s", e.Code, e.Message)
		}
		return fmt.Sprintf("%d events, has_more=%v", len(evs.Events), evs.HasMore), nil
	})

	r.run("artifacts", func() (string, error) {
		arts, e := client.Artifacts(taskID, false)
		if e != nil {
			return "", fmt.Errorf("%s: %s", e.Code, e.Message)
		}
		if arts.Integration == nil {
			return "no integration candidate (text-only task)", nil
		}
		return fmt.Sprintf("status=%s, %d manifests", arts.Integration.Status, len(arts.Manifests)), nil
	})

	r.run("cancel", func() (string, error) {
		can, e := client.Cancel(taskID, "fixture verification")
		if e != nil {
			if e.Code == captaincode.ErrAlreadyTerminal {
				return "already terminal", nil
			}
			return "", fmt.Errorf("%s: %s", e.Code, e.Message)
		}
		if !captaincode.IsTerminal(can.State) {
			state := can.State
			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				insp, ie := client.Inspect(taskID, captaincode.InspectRequest{})
				if ie != nil {
					time.Sleep(2 * time.Second)
					continue
				}
				if insp.Task != nil && captaincode.IsTerminal(insp.Task.State) {
					state = insp.Task.State
					break
				}
				time.Sleep(2 * time.Second)
			}
			if !captaincode.IsTerminal(state) {
				return "", fmt.Errorf("task did not reach terminal (state=%s)", state)
			}
			return fmt.Sprintf("state=%s (polled)", state), nil
		}
		return fmt.Sprintf("state=%s, cancelled=%d", can.State, len(can.Cancelled)), nil
	})

	r.run("error_not_found", func() (string, error) {
		_, e := client.Inspect("nonexistent-task-fixture-"+captaincode.NewRequestID()[:8], captaincode.InspectRequest{})
		if e == nil {
			return "", fmt.Errorf("expected error for non-existent task")
		}
		if e.Code != captaincode.ErrNotFound {
			return "", fmt.Errorf("expected not_found, got %s", e.Code)
		}
		return fmt.Sprintf("code=%s", e.Code), nil
	})

	if planResp != nil {
		r.run("idempotent_submit", func() (string, error) {
			s, e := client.Submit(captaincode.SubmitRequest{Prompt: prompt})
			if e != nil {
				return "", fmt.Errorf("%s: %s", e.Code, e.Message)
			}
			if s.TaskID == "" {
				return "", fmt.Errorf("empty task_id in duplicate submit response")
			}
			return fmt.Sprintf("task=%s (new task, idempotency per request_id)", s.TaskID), nil
		})
	}

	return r
}

func (r *fixtureReport) run(name string, fn func() (string, error)) {
	detail, err := fn()
	step := fixtureStep{Name: name}
	if err != nil {
		step.Status = "fail"
		step.Detail = err.Error()
		r.Failed++
	} else {
		step.Status = "pass"
		step.Detail = detail
		r.Passed++
	}
	r.Steps = append(r.Steps, step)
}

func (r *fixtureReport) finish(jsonOut bool) {
	if jsonOut {
		r.Skipped = countByStatus(r.Steps, "skip")
		out, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(out))
	} else {
		fmt.Printf("fixture against %s\n", r.BrainURL)
		fmt.Println(strings.Repeat("─", 60))
		for _, s := range r.Steps {
			mark := "✓"
			if s.Status == "fail" {
				mark = "✗"
			}
			fmt.Printf("  %s %-22s %s\n", mark, s.Name, s.Detail)
		}
		fmt.Println(strings.Repeat("─", 60))
		fmt.Printf("  %d passed, %d failed\n", r.Passed, r.Failed)
	}
	if r.Failed > 0 {
		os.Exit(1)
	}
}

func countByStatus(steps []fixtureStep, status string) int {
	n := 0
	for _, s := range steps {
		if s.Status == status {
			n++
		}
	}
	return n
}
