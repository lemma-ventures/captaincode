package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// TestTaskFixtureHTTP runs the contract fixture against a test server that
// speaks the task API envelope, verifying every step passes against a
// compliant brain.
func TestTaskFixtureHTTP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/task", func(w http.ResponseWriter, r *http.Request) {
		var req captaincode.TaskRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := captaincode.TaskResponse{
			Version: captaincode.TaskAPIVersion,
			Status:  captaincode.ResponseOK,
			Op:      req.Op,
		}

		switch req.Op {
		case captaincode.OpPlan:
			body, _ := json.Marshal(captaincode.PlanResponse{
				Leg:   "claude",
				Class: "frontier",
			})
			resp.Body = body
		case captaincode.OpSubmit:
			body, _ := json.Marshal(captaincode.SubmitResponse{
				TaskID: "task-fixture-001",
				State:  captaincode.StateAdmitted,
			})
			resp.Body = body
			resp.TaskID = "task-fixture-001"
		case captaincode.OpInspect:
			if strings.HasPrefix(req.TaskID, "task-fixture-") {
				body, _ := json.Marshal(captaincode.InspectResponse{
					Task: &captaincode.TaskState{
						TaskID: req.TaskID,
						State:  captaincode.StateRunning,
					},
				})
				resp.Body = body
			} else {
				resp.Status = captaincode.ResponseError
				resp.Error = captaincode.NewTaskError(captaincode.ErrNotFound, "task not found")
			}
		case captaincode.OpEvents:
			body, _ := json.Marshal(captaincode.EventsResponse{
				Events: []captaincode.TaskEvent{
					{Kind: "dispatch", Leg: "claude"},
				},
			})
			resp.Body = body
		case captaincode.OpArtifacts:
			body, _ := json.Marshal(captaincode.ArtifactsResponse{})
			resp.Body = body
		case captaincode.OpCancel:
			body, _ := json.Marshal(captaincode.CancelResponse{
				TaskID:    req.TaskID,
				State:     captaincode.StateCancelled,
				Cancelled: []string{"worker:claude"},
			})
			resp.Body = body
		default:
			resp.Status = captaincode.ResponseError
			resp.Error = captaincode.NewTaskError(captaincode.ErrBadRequest, "unknown op")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	report := runFixtureAgainstClient(srv.URL, "", "Add a doc comment to the main function")

	if report.Failed != 0 {
		t.Errorf("expected 0 failures, got %d", report.Failed)
		for _, s := range report.Steps {
			if s.Status == "fail" {
				t.Logf("  FAIL: %s — %s", s.Name, s.Detail)
			}
		}
	}
	if report.Passed < 7 {
		t.Errorf("expected at least 7 passed steps, got %d", report.Passed)
	}
}

// TestTaskFixtureNotFound verifies the error_not_found step passes when the
// server returns not_found for an unknown task.
func TestTaskFixtureNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/task", func(w http.ResponseWriter, r *http.Request) {
		var req captaincode.TaskRequest
		json.NewDecoder(r.Body).Decode(&req)
		resp := captaincode.TaskResponse{
			Version: captaincode.TaskAPIVersion,
			Op:      req.Op,
		}
		switch req.Op {
		case captaincode.OpPlan:
			body, _ := json.Marshal(captaincode.PlanResponse{Leg: "claude", Class: "frontier"})
			resp.Body = body
			resp.Status = captaincode.ResponseOK
		case captaincode.OpSubmit:
			body, _ := json.Marshal(captaincode.SubmitResponse{
				TaskID: "task-fixture-002",
				State:  captaincode.StateAdmitted,
			})
			resp.Body = body
			resp.Status = captaincode.ResponseOK
			resp.TaskID = "task-fixture-002"
		case captaincode.OpInspect:
			resp.Status = captaincode.ResponseError
			resp.Error = captaincode.NewTaskError(captaincode.ErrNotFound, "task not found")
		case captaincode.OpEvents:
			body, _ := json.Marshal(captaincode.EventsResponse{})
			resp.Body = body
			resp.Status = captaincode.ResponseOK
		case captaincode.OpArtifacts:
			body, _ := json.Marshal(captaincode.ArtifactsResponse{})
			resp.Body = body
			resp.Status = captaincode.ResponseOK
		case captaincode.OpCancel:
			body, _ := json.Marshal(captaincode.CancelResponse{
				TaskID: req.TaskID,
				State:  captaincode.StateCancelled,
			})
			resp.Body = body
			resp.Status = captaincode.ResponseOK
		default:
			resp.Status = captaincode.ResponseError
			resp.Error = captaincode.NewTaskError(captaincode.ErrBadRequest, "unknown")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	report := runFixtureAgainstClient(srv.URL, "", "test prompt")

	notFoundStep := findStep(report.Steps, "error_not_found")
	if notFoundStep == nil {
		t.Fatal("error_not_found step missing")
	}
	if notFoundStep.Status != "pass" {
		t.Errorf("error_not_found should pass, got %s: %s", notFoundStep.Status, notFoundStep.Detail)
	}
}

// TestTaskFixtureJSON verifies the report is valid JSON.
func TestTaskFixtureJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/task", func(w http.ResponseWriter, r *http.Request) {
		resp := captaincode.TaskResponse{
			Version: captaincode.TaskAPIVersion,
			Status:  captaincode.ResponseError,
			Op:      captaincode.OpPlan,
			Error:   captaincode.NewTaskError(captaincode.ErrUnavailable, "test"),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	report := runFixtureAgainstClient(srv.URL, "", "test")

	out, _ := json.Marshal(report)
	if !json.Valid(out) {
		t.Error("report should be valid JSON")
	}
	if !strings.Contains(string(out), "brain_url") {
		t.Error("JSON should contain brain_url")
	}
}

func findStep(steps []fixtureStep, name string) *fixtureStep {
	for i := range steps {
		if steps[i].Name == name {
			return &steps[i]
		}
	}
	return nil
}
