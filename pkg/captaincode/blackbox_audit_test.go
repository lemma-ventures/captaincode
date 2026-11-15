package captaincode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func blackboxAuditRecord(t *testing.T) BlackboxSecurityRecord {
	t.Helper()
	result := BlackboxResult{
		Schema: 1, SnapshotID: "fixture", SnapshotDigest: "abc123", Status: "complete",
		Findings: []BlackboxFinding{},
		Coverage: []BlackboxCoverage{{Category: "dependency", Status: "covered"}},
	}
	action := BlackboxAction{Schema: 1, Host: "opencode", Phase: "pre", Tool: "bash", Kind: "shell", ParseStatus: "parsed", WorkspaceID: "ws_1", SessionID: "ses_1"}
	record := NewBlackboxSecurityRecord(action, result, "audit", 1500*time.Microsecond)
	FillBlackboxIDs(&record, "ses_1", "task_1", "run_1", "call_1")
	return record
}

func TestBlackboxSecurityRecordCarriesNoActionContent(t *testing.T) {
	record := blackboxAuditRecord(t)
	record.Findings = append(record.Findings, BlackboxSecurityFinding{Category: "protected_path", RuleID: "path-exact", Kind: "known_malware", Severity: "critical", Reason: "protected_path_match"})
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, forbidden := range []string{"command", "paths", "urls", "packages", "\"text\"", "output", "/Users/", "secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("record leaks %q: %s", forbidden, text)
		}
	}
}

func TestBlackboxDispositionByStatusAndPhase(t *testing.T) {
	tests := []struct {
		name, phase, status string
		critical            bool
		want                string
	}{
		{"clean pre", "pre", "complete", false, "evaluated"},
		{"critical pre", "pre", "complete", true, "would_block"},
		{"error pre", "pre", "error", false, "degraded"},
		{"incomplete pre", "pre", "incomplete", false, "degraded"},
		{"critical post stays observation", "post", "complete", true, "observed_after_execution"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action := BlackboxAction{Schema: 1, Host: "opencode", Phase: test.phase, Tool: "bash", Kind: "shell", ParseStatus: "parsed", WorkspaceID: "ws_1"}
			result := BlackboxResult{Schema: 1, SnapshotID: "fixture", Status: test.status, Findings: []BlackboxFinding{}, Coverage: []BlackboxCoverage{}}
			if test.critical {
				result.Findings = append(result.Findings, BlackboxFinding{Category: "protected_path", RuleID: "path-exact", Kind: "known_malware", Severity: "critical", Reason: "protected_path_match"})
			}
			if test.status == "error" {
				result.Errors = []string{"snapshot_unavailable"}
			}
			record := NewBlackboxSecurityRecord(action, result, "audit", time.Millisecond)
			if record.Disposition != test.want {
				t.Fatalf("disposition = %s, want %s", record.Disposition, test.want)
			}
		})
	}
}

func TestBlackboxRecordAppendRotateAndConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "security.jsonl")
	t.Setenv("CAPTAIN_BLACKBOX_STATE", path)

	// Concurrent appends with no rotation in the way: every writer's line
	// must arrive whole and unique.
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				record := blackboxAuditRecord(t)
				record.SessionID = fmt.Sprintf("ses_%d_%d", worker, i)
				if err := AppendBlackboxSecurityRecord(record); err != nil {
					t.Errorf("append: %v", err)
				}
			}
		}(worker)
	}
	wg.Wait()
	records, partial, err := ReadBlackboxSecurityRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if partial != 0 {
		t.Fatalf("%d partial lines after concurrent appends", partial)
	}
	if len(records) != 200 {
		t.Fatalf("records = %d, want 200", len(records))
	}
	seen := map[string]bool{}
	for _, record := range records {
		if seen[record.EventID] {
			t.Fatalf("duplicate event id %s", record.EventID)
		}
		seen[record.EventID] = true
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("no rotation may happen under the size threshold")
	}
}

func TestBlackboxRecordRotationBoundsTheStream(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "security.jsonl")
	t.Setenv("CAPTAIN_BLACKBOX_STATE", path)
	oldRotate := blackboxRotateBytes
	blackboxRotateBytes = 256
	defer func() { blackboxRotateBytes = oldRotate }()
	for i := 0; i < 20; i++ {
		record := blackboxAuditRecord(t)
		record.SessionID = fmt.Sprintf("ses_%d", i)
		if err := AppendBlackboxSecurityRecord(record); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Size() > blackboxRotateBytes+blackboxMaxRecordBytes {
		t.Fatalf("live stream is %v bytes, rotation did not bound it", info.Size())
	}
	for _, generation := range []string{path, path + ".1", path + ".2"} {
		_, partial, err := ReadBlackboxSecurityRecords(generation)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if partial != 0 {
			t.Fatalf("%s has %d partial lines", generation, partial)
		}
	}
}

func TestBlackboxRecordAppendFailsClosedOnUnwritableState(t *testing.T) {
	dir := t.TempDir()
	readOnly := filepath.Join(dir, "locked")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(readOnly, "security.jsonl"))
	err := AppendBlackboxSecurityRecord(blackboxAuditRecord(t))
	if err == nil {
		t.Fatal("append into an unwritable state dir must fail, not hide the loss")
	}
}

func TestBlackboxRecordSizeCapTrimsFindings(t *testing.T) {
	record := blackboxAuditRecord(t)
	for i := 0; i < blackboxMaxRecordFindings*4; i++ {
		record.Findings = append(record.Findings, BlackboxSecurityFinding{Category: "pattern", RuleID: fmt.Sprintf("pattern-rule-with-a-long-identifier-%04d", i), Kind: "heuristic", Severity: "warning", Reason: "pattern_match"})
	}
	record.FindingCount = len(record.Findings)
	line, err := blackboxRecordLine(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(line) > blackboxMaxRecordBytes {
		t.Fatalf("line is %d bytes, cap is %d", len(line), blackboxMaxRecordBytes)
	}
	var decoded BlackboxSecurityRecord
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.FindingCount != len(record.Findings) {
		t.Fatalf("finding count = %d, want the true total %d", decoded.FindingCount, len(record.Findings))
	}
	if len(decoded.Findings) > blackboxMaxRecordFindings {
		t.Fatalf("record carries %d findings, cap is %d", len(decoded.Findings), blackboxMaxRecordFindings)
	}
}

func TestBlackboxWorkspaceIDAndIDSanitization(t *testing.T) {
	if BlackboxWorkspaceID("") != "" {
		t.Fatal("an empty cwd must stay empty")
	}
	a, b := BlackboxWorkspaceID("/Users/x/one"), BlackboxWorkspaceID("/Users/x/two")
	if a == b || !strings.HasPrefix(a, "ws_") {
		t.Fatalf("workspace ids collide or are malformed: %s %s", a, b)
	}
	record := BlackboxSecurityRecord{}
	FillBlackboxIDs(&record, "ses-ok", "", "", "")
	if record.SessionID != "ses-ok" || len(record.OmittedIDs) != 0 {
		t.Fatalf("session not kept: %+v", record)
	}
	hostile := strings.Repeat("x", 200) + "\n"
	FillBlackboxIDs(&record, hostile, hostile, hostile, hostile)
	if record.SessionID != "" || record.TaskID != "" || record.RunID != "" || record.ToolCallID != "" {
		t.Fatalf("hostile ids must be dropped, not recorded: %+v", record)
	}
	if len(record.OmittedIDs) != 4 {
		t.Fatalf("omitted ids = %v", record.OmittedIDs)
	}
}

func TestGateTaskForSessionJoinsAndMisses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	now := time.Now()
	AppendGateSession(GateSession{Session: "ses-live", TaskID: "task-1", Start: now.Add(-time.Minute), End: now.Add(time.Minute)})
	if got := GateTaskForSession("ses-live", now); got != "task-1" {
		t.Fatalf("task = %q, want task-1", got)
	}
	if got := GateTaskForSession("ses-gone", now); got != "" {
		t.Fatalf("unknown session resolved to %q", got)
	}
	if got := GateTaskForSession("ses-live", now.Add(time.Hour)); got != "" {
		t.Fatalf("a time outside the run resolved to %q", got)
	}
}

func TestPrepareBlackboxHookActionPreAndPost(t *testing.T) {
	pre, _ := json.Marshal(map[string]any{"command": "npm install example-package@1.2.3"})
	action := PrepareBlackboxHookAction("opencode", "pre", "bash", pre, "", "/Users/x/repo")
	if action.Kind != "shell" || action.Command != "npm install example-package@1.2.3" {
		t.Fatalf("pre action = %+v", action)
	}
	if action.WorkspaceID == "" || strings.Contains(action.WorkspaceID, "repo") {
		t.Fatalf("workspace id must be a path-free hash: %q", action.WorkspaceID)
	}
	if len(action.Packages) != 1 || action.Packages[0].Name != "example-package" {
		t.Fatalf("packages = %+v", action.Packages)
	}
	post := PrepareBlackboxHookAction("opencode", "post", "bash", nil, "tool output text", "/Users/x/repo")
	if post.Phase != "post" || post.Text != "tool output text" || post.Kind != "shell" {
		t.Fatalf("post action = %+v", post)
	}
	long := PrepareBlackboxHookAction("opencode", "post", "bash", nil, strings.Repeat("a", blackboxMaxText+10), "")
	if !long.Truncated {
		t.Fatal("an oversized post output must be marked truncated")
	}
	unknown := PrepareBlackboxHookAction("opencode", "pre", "mcp_server_tool", pre, "", "")
	if unknown.Kind != "unknown" || len(unknown.OmittedFields) == 0 {
		t.Fatalf("unknown tool must be a coverage gap: %+v", unknown)
	}
}

func TestEvaluateBlackboxHookDegradedWithoutSnapshot(t *testing.T) {
	dir := t.TempDir()
	action := PrepareBlackboxHookAction("opencode", "pre", "bash", json.RawMessage(`{"command":"ls"}`), "", dir)
	for name, path := range map[string]string{"missing": filepath.Join(dir, "none.json"), "invalid": writeBlackboxFile(t, dir, "bad.json", "{not json")} {
		result := EvaluateBlackboxHook(action, path)
		if result.Status != "error" || len(result.Errors) != 1 {
			t.Fatalf("%s: result = %+v", name, result)
		}
	}
}

func TestFinishBlackboxHookRecordFullRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(dir, "security.jsonl"))
	now := time.Now()
	AppendGateSession(GateSession{Session: "ses-run", TaskID: "task-9", Start: now.Add(-time.Minute), End: now.Add(time.Minute)})

	snapshot := blackboxFixtureSnapshot()
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := writeBlackboxFile(t, dir, "snapshot.json", string(body))

	args, _ := json.Marshal(map[string]any{"command": "npm install example-package@1.2.3"})
	action := PrepareBlackboxHookAction("opencode", "pre", "bash", args, "", "/Users/x/repo")
	result := EvaluateBlackboxHook(action, snapshotPath)
	if result.Status != "complete" || len(result.Findings) != 1 {
		t.Fatalf("result = %+v", result)
	}
	record := FinishBlackboxHookRecord(action, result, "audit", "ses-run", now)
	if record.Disposition != "would_block" {
		t.Fatalf("disposition = %s, want would_block", record.Disposition)
	}
	if record.TaskID != "task-9" || record.SessionID != "ses-run" {
		t.Fatalf("correlation ids = %+v", record)
	}
	if record.Findings[0].RuleID != "pkg-exact" {
		t.Fatalf("finding = %+v", record.Findings[0])
	}
	if err := AppendBlackboxSecurityRecord(record); err != nil {
		t.Fatal(err)
	}
	records, partial, err := ReadBlackboxSecurityRecords(filepath.Join(dir, "security.jsonl"))
	if err != nil || partial != 0 || len(records) != 1 {
		t.Fatalf("read: %v partial=%d n=%d", err, partial, len(records))
	}
	if records[0].Disposition != "would_block" {
		t.Fatalf("stored disposition = %s", records[0].Disposition)
	}
}

func writeBlackboxFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBlackboxHookResultWithoutSnapshotFileDefault(t *testing.T) {
	action := PrepareBlackboxHookAction("opencode", "pre", "bash", json.RawMessage(`{"command":"ls"}`), "", "")
	result := EvaluateBlackboxHook(action, "")
	if result.Status != "error" || result.Errors[0] != "snapshot_unavailable" {
		t.Fatalf("result = %+v", result)
	}
	ctx := context.Background()
	if EvaluateBlackbox(ctx, action, blackboxFixtureSnapshot()).Status == "error" {
		t.Fatal("the evaluator itself must still work for a valid snapshot")
	}
}
