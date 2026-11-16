package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if err := runBlackbox([]string{"hook", "--mode", "enforce"}, strings.NewReader(`{"tool":"bash"}`), &stdout); err != nil {
		t.Fatalf("enforce is built (BB5): %v", err)
	}
}

func TestBlackboxHookEnforceRefusesRuleMatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	state := blackboxHookState(t, dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", state)
	snapshot := blackboxHookSnapshot(t, dir)
	input := `{"tool":"bash","cwd":"/Users/x/repo","session":"ses-e1","phase":"pre","args":{"command":"npm install example-package@1.2.3"}}`
	run := func() blackboxHookOutput {
		t.Helper()
		var stdout bytes.Buffer
		if err := runBlackbox([]string{"hook", "--mode", "enforce", "--snapshot", snapshot}, strings.NewReader(input), &stdout); err != nil {
			t.Fatalf("hook: %v", err)
		}
		var out blackboxHookOutput
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := run()
	if !first.Refuse || first.Reason != "blackbox_rule_match:pkg-exact" || first.Disposition != "blocked" {
		t.Fatalf("first = %+v - an enforce match must refuse", first)
	}
	// A retry of the same action meets the same refusal: the decision is a
	// pure function of the event and the snapshot, so there is no bypass.
	second := run()
	if !second.Refuse || second.Reason != "blackbox_rule_match:pkg-exact" {
		t.Fatalf("second = %+v - a retry must meet the same refusal", second)
	}
	records, _, err := captaincode.ReadBlackboxSecurityRecords(state)
	if err != nil || len(records) != 2 {
		t.Fatalf("read: %v n=%d", err, len(records))
	}
	for _, record := range records {
		if record.Disposition != "blocked" || record.Mode != "enforce" {
			t.Fatalf("record = %+v", record)
		}
		if strings.Contains(string(marshalHookJSON(t, record)), "example-package") {
			t.Fatal("the record must not carry the screened command's content")
		}
	}
}

func TestBlackboxHookEnforceUnavailableFailsClosed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", blackboxHookState(t, dir))
	var stdout bytes.Buffer
	// A missing snapshot is a required check that cannot run: refuse as
	// unavailable, never as malicious.
	input := `{"tool":"bash","session":"ses-e2","phase":"pre","args":{"command":"ls"}}`
	if err := runBlackbox([]string{"hook", "--mode", "enforce", "--snapshot", filepath.Join(dir, "none.json")}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var out blackboxHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Refuse || out.Reason != "security_check_unavailable" {
		t.Fatalf("out = %+v - an unavailable required check must refuse", out)
	}
}

func TestBlackboxHookEnforceAdvisoryFindingsDoNotRefuse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	state := blackboxHookState(t, dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", state)
	// The pattern rule is heuristic and warning-severity: recorded, never
	// blocking, even in enforce mode.
	input := `{"tool":"bash","session":"ses-e3","phase":"pre","args":{"command":"echo dangerous-example"}}`
	var stdout bytes.Buffer
	if err := runBlackbox([]string{"hook", "--mode", "enforce", "--snapshot", blackboxHookSnapshot(t, dir)}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var out blackboxHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Refuse {
		t.Fatalf("out = %+v - an advisory finding must not refuse", out)
	}
	// Post phase never refuses either: the action already ran.
	post := `{"tool":"bash","session":"ses-e4","phase":"post","output":"npm install example-package@1.2.3 done"}`
	stdout.Reset()
	if err := runBlackbox([]string{"hook", "--mode", "enforce", "--snapshot", blackboxHookSnapshot(t, dir)}, strings.NewReader(post), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var postOut blackboxHookOutput
	json.Unmarshal(stdout.Bytes(), &postOut)
	if postOut.Refuse || postOut.Disposition != "observed_after_execution" {
		t.Fatalf("postOut = %+v", postOut)
	}
}

func TestBlackboxHookEnforceOverrideSuppressesOneRule(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	state := blackboxHookState(t, dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", state)
	workspace := captaincode.BlackboxWorkspaceID("/Users/x/repo")
	expires := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	overrides := filepath.Join(dir, "overrides.json")
	t.Setenv("CAPTAIN_BLACKBOX_OVERRIDES", overrides)
	body, err := json.Marshal([]captaincode.BlackboxOverride{
		{RuleID: "pkg-exact", WorkspaceID: workspace, Expires: expires, Reason: "operator reviewed the fixture rule"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overrides, body, 0o600); err != nil {
		t.Fatal(err)
	}
	input := `{"tool":"bash","cwd":"/Users/x/repo","session":"ses-e5","phase":"pre","args":{"command":"npm install example-package@1.2.3"}}`
	var stdout bytes.Buffer
	if err := runBlackbox([]string{"hook", "--mode", "enforce", "--snapshot", blackboxHookSnapshot(t, dir)}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var out blackboxHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Refuse || out.Disposition != "overridden" {
		t.Fatalf("out = %+v - an active override suppresses the refusal", out)
	}
	// An override scoped to another workspace grants nothing here.
	other := captaincode.BlackboxWorkspaceID("/Users/x/other")
	otherBody, err := json.Marshal([]captaincode.BlackboxOverride{{RuleID: "pkg-exact", WorkspaceID: other, Expires: expires, Reason: "other workspace"}})
	if err := os.WriteFile(overrides, otherBody, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runBlackbox([]string{"hook", "--mode", "enforce", "--snapshot", blackboxHookSnapshot(t, dir)}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var out2 blackboxHookOutput
	json.Unmarshal(stdout.Bytes(), &out2)
	if !out2.Refuse || out2.Disposition != "blocked" {
		t.Fatalf("out2 = %+v - a foreign-workspace override must not suppress", out2)
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
