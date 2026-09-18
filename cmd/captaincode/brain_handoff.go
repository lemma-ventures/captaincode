package main

// Handoff HTTP handler (ROADMAP M3.5).
//
// GET /v1/handoff?task=<id>  — return the stored brief for one task
// GET /v1/handoff             — return a list of all stored briefs
// POST /v1/handoff?task=<id>  — rebuild and store a brief from current ledger state

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func (b *brain) handoffHTTP(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	switch r.Method {
	case http.MethodGet:
		b.handoffGetHTTP(w, r, taskID)
	case http.MethodPost:
		b.handoffBuildHTTP(w, r, taskID)
	default:
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
	}
}

func (b *brain) handoffGetHTTP(w http.ResponseWriter, _ *http.Request, taskID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if taskID != "" {
		brief := b.ledger.HandoffFor(taskID)
		if brief == nil {
			writeJSON(w, 404, map[string]any{"error": "no handoff for task"})
			return
		}
		writeJSON(w, 200, brief)
		return
	}
	writeJSON(w, 200, map[string]any{
		"briefs": b.ledger.Handoffs,
		"count":  len(b.ledger.Handoffs),
	})
}

func (b *brain) handoffBuildHTTP(w http.ResponseWriter, _ *http.Request, taskID string) {
	if taskID == "" {
		writeJSON(w, 400, map[string]any{"error": "missing task parameter"})
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	integration := b.integrationFor(taskID)
	requirements := b.requirementsFor(taskID)
	brief := captaincode.BuildHandoffBrief(b.ledger, taskID, requirements, integration)
	b.ledger.RecordHandoff(brief)
	if err := b.ledger.Save(); err != nil {
		writeJSON(w, 500, map[string]any{"error": "save: " + err.Error()})
		return
	}
	writeJSON(w, 200, brief)
}

// integrationFor returns the last stored integration candidate for a task,
// or nil. Guarded by imu.
func (b *brain) integrationFor(taskID string) *captaincode.IntegrationCandidate {
	b.imu.Lock()
	defer b.imu.Unlock()
	ic := b.lastIntegrations[taskID]
	return &ic
}

// requirementsFor returns the original task prompt for a task from the ledger's
// events. The first event for a task carries the task text.
func (b *brain) requirementsFor(taskID string) string {
	for _, ev := range b.ledger.Events {
		if ev.TaskID == taskID && ev.Task != "" {
			return ev.Task
		}
	}
	return ""
}

var _ = json.Marshal
