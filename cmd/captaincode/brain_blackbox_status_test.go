package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func TestBlackboxWorkspaceStatusScopesRecords(t *testing.T) {
	dir := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX", "audit")
	t.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(dir, "security.jsonl"))
	t.Setenv("CAPTAIN_BLACKBOX_SNAPSHOT", writeStatusSnapshot(t, dir))
	appendStatusRecord(t, captaincode.BlackboxSecurityRecord{
		EventID: "event-a", Phase: "pre", Tool: "bash", Mode: "audit", Host: "claude",
		Disposition: "would_block", Status: "complete", WorkspaceID: captaincode.BlackboxWorkspaceID(workspaceA), SessionID: "session-a", TaskID: "task-a",
	})
	appendStatusRecord(t, captaincode.BlackboxSecurityRecord{
		EventID: "event-b", Phase: "post", Tool: "write", Mode: "audit", Host: "opencode",
		Disposition: "observed_after_execution", Status: "complete", WorkspaceID: captaincode.BlackboxWorkspaceID(workspaceB), SessionID: "session-b", TaskID: "task-b",
	})

	for _, tc := range []struct {
		workspace   string
		disposition string
		host        string
		included    string
		excluded    string
	}{
		{workspaceA, "would_block", "claude", "task-a", "task-b"},
		{workspaceB, "observed_after_execution", "opencode", "task-b", "task-a"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/blackbox/status?cwd="+url.QueryEscape(tc.workspace), nil)
		(&brain{}).blackboxStatusHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Workspace    string                               `json:"workspace"`
			Total        int                                  `json:"total"`
			Dispositions map[string]int                       `json:"dispositions"`
			Hosts        map[string]int                       `json:"hosts"`
			Recent       []captaincode.BlackboxSecurityRecord `json:"recent"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Workspace != captaincode.BlackboxWorkspaceID(tc.workspace) || body.Total != 1 {
			t.Fatalf("workspace status = %+v", body)
		}
		if body.Dispositions[tc.disposition] != 1 || body.Hosts[tc.host] != 1 || len(body.Recent) != 1 || body.Recent[0].TaskID != tc.included {
			t.Fatalf("scoped status = %+v", body)
		}
		if strings.Contains(rec.Body.String(), tc.excluded) || strings.Contains(rec.Body.String(), tc.workspace) {
			t.Fatalf("response crosses a workspace or exposes a path: %s", rec.Body.String())
		}
	}
}

func TestBlackboxWorkspaceStatusRequiresScopeAndGET(t *testing.T) {
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/blackbox/status", nil),
		httptest.NewRequest(http.MethodGet, "/v1/blackbox/status?cwd=relative", nil),
		httptest.NewRequest(http.MethodPost, "/v1/blackbox/status?cwd="+url.QueryEscape(t.TempDir()), nil),
	} {
		rec := httptest.NewRecorder()
		(&brain{}).blackboxStatusHTTP(rec, req)
		if rec.Code != http.StatusBadRequest && req.Method == http.MethodGet {
			t.Fatalf("GET status = %d, want 400", rec.Code)
		}
		if rec.Code != http.StatusMethodNotAllowed && req.Method == http.MethodPost {
			t.Fatalf("POST status = %d, want 405", rec.Code)
		}
	}
}
