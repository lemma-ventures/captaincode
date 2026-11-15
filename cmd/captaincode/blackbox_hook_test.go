package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func blackboxHookSnapshot(t *testing.T, dir string) string {
	t.Helper()
	snapshot := captaincode.BlackboxSnapshot{
		Schema: 1, ID: "fixture", Source: "synthetic", Revision: "rev-1",
		Categories: map[string]bool{"dependency": true, "protected_path": true, "secret_exposure": true, "pattern": true},
		Packages: []captaincode.BlackboxPackageRule{
			{ID: "pkg-exact", Ecosystem: "npm", Name: "example-package", Version: "1.2.3", Kind: "known_malware", Severity: "critical"},
		},
		Paths:    []captaincode.BlackboxPathRule{{ID: "path-exact", Path: "config/private/**", Kind: "protected_path", Severity: "critical"}},
		Patterns: []captaincode.BlackboxPatternRule{{ID: "pattern-one", Category: "pattern", Pattern: `dangerous-example`, Kind: "heuristic", Severity: "warning"}},
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "snapshot.json")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func blackboxHookState(t *testing.T, dir string) string {
	t.Helper()
	return filepath.Join(dir, "security.jsonl")
}

func TestBlackboxHookRecordsFindingsAndNeverBlocks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", blackboxHookState(t, dir))
	state := blackboxHookState(t, dir)
	input := `{"tool":"bash","cwd":"/Users/x/repo","session":"ses-1","phase":"pre","args":{"command":"npm install example-package@1.2.3"}}`
	var stdout bytes.Buffer
	if err := runBlackbox([]string{"hook", "--snapshot", blackboxHookSnapshot(t, dir), "--mode", "audit"}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var out blackboxHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Disposition != "would_block" || out.Findings != 1 {
		t.Fatalf("out = %+v - an audit finding is recorded, not enforced", out)
	}
	records, partial, err := captaincode.ReadBlackboxSecurityRecords(state)
	if err != nil || partial != 0 || len(records) != 1 {
		t.Fatalf("read: %v partial=%d n=%d", err, partial, len(records))
	}
	if records[0].Disposition != "would_block" || records[0].Tool != "bash" {
		t.Fatalf("record = %+v", records[0])
	}
	if strings.Contains(string(marshalHookJSON(t, records[0])), "example-package") {
		t.Fatal("the record must not carry the screened command's content")
	}
}

func TestBlackboxHookPhasesAndDegradedModes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	state := blackboxHookState(t, dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", state)
	run := func(input string, flags ...string) blackboxHookOutput {
		t.Helper()
		var stdout bytes.Buffer
		args := append([]string{"hook", "--mode", "audit"}, flags...)
		if err := runBlackbox(args, strings.NewReader(input), &stdout); err != nil {
			t.Fatalf("hook: %v", err)
		}
		var out blackboxHookOutput
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Post phase records an observation, whatever it saw.
	post := run(`{"tool":"bash","session":"ses-2","phase":"post","output":"harmless output"}`, "--snapshot", blackboxHookSnapshot(t, dir))
	if post.Disposition != "observed_after_execution" {
		t.Fatalf("post disposition = %+v", post)
	}
	// A missing snapshot degrades instead of claiming a clean screen.
	missing := run(`{"tool":"bash","session":"ses-3","phase":"pre","args":{"command":"ls"}}`, "--snapshot", filepath.Join(dir, "none.json"))
	if missing.Disposition != "degraded" || missing.Status != "error" {
		t.Fatalf("missing snapshot out = %+v", missing)
	}
	// An invalid snapshot fails closed the same way.
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{not json"), 0o600)
	invalid := run(`{"tool":"bash","session":"ses-4","phase":"pre","args":{"command":"ls"}}`, "--snapshot", bad)
	if invalid.Disposition != "degraded" {
		t.Fatalf("invalid snapshot out = %+v", invalid)
	}
	records, partial, err := captaincode.ReadBlackboxSecurityRecords(state)
	if err != nil || partial != 0 || len(records) != 3 {
		t.Fatalf("read: %v partial=%d n=%d", err, partial, len(records))
	}
	for _, record := range records {
		if record.SessionID == "" {
			t.Fatalf("record without session: %+v", record)
		}
	}
}

func TestBlackboxHookModeOffAndEnforceRefused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPTAIN_BLACKBOX_STATE", blackboxHookState(t, dir))
	var stdout bytes.Buffer
	if err := runBlackbox([]string{"hook", "--mode", "off"}, strings.NewReader(`{"tool":"bash"}`), &stdout); err != nil {
		t.Fatalf("off: %v", err)
	}
	var out blackboxHookOutput
	json.Unmarshal(stdout.Bytes(), &out)
	if out.Status != "disabled" {
		t.Fatalf("off out = %+v", out)
	}
	if _, err := os.Stat(blackboxHookState(t, dir)); !os.IsNotExist(err) {
		t.Fatal("off mode must write no record")
	}
	if err := runBlackbox([]string{"hook", "--mode", "enforce"}, strings.NewReader(`{"tool":"bash"}`), &stdout); err == nil {
		t.Fatal("enforce must be refused: it is BB5 and not built")
	}
}

func TestBlackboxHookInputValidation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPTAIN_BLACKBOX_STATE", blackboxHookState(t, dir))
	var stdout bytes.Buffer
	for name, input := range map[string]string{
		"unknown field": `{"tool":"bash","surprise":1}`,
		"trailing json": `{"tool":"bash"} {}`,
		"no tool":       `{"session":"s"}`,
		"bad phase":     `{"tool":"bash","phase":"during"}`,
	} {
		if err := runBlackbox([]string{"hook", "--mode", "audit"}, strings.NewReader(input), &stdout); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

func TestBlackboxHookWriteFailureStillExitsClean(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	locked := filepath.Join(dir, "locked")
	os.Mkdir(locked, 0o500)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(locked, "security.jsonl"))
	var stdout bytes.Buffer
	input := `{"tool":"bash","session":"ses-5","phase":"pre","args":{"command":"ls"}}`
	if err := runBlackbox([]string{"hook", "--mode", "audit", "--snapshot", blackboxHookSnapshot(t, dir)}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("a failed record write must not fail the hook: %v", err)
	}
	var out blackboxHookOutput
	json.Unmarshal(stdout.Bytes(), &out)
	if out.Status != "degraded" || out.Disposition != "degraded" {
		t.Fatalf("out = %+v", out)
	}
}

func TestBlackboxCheckUnchanged(t *testing.T) {
	dir := t.TempDir()
	snapshot := blackboxHookSnapshot(t, dir)
	var stdout bytes.Buffer
	action := `{"schema":1,"host":"opencode","phase":"pre","tool":"bash","kind":"shell","parse_status":"parsed"}`
	if err := runBlackbox([]string{"check", "--snapshot", snapshot}, strings.NewReader(action), &stdout); err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(stdout.String(), `"status"`) {
		t.Fatalf("check output = %s", stdout.String())
	}
}

func marshalHookJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
