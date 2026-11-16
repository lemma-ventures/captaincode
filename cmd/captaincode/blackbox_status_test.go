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

func writeStatusSnapshot(t *testing.T, dir string) string {
	t.Helper()
	snapshot := captaincode.BlackboxSnapshot{
		Schema: 1, ID: "claude-fixture", Source: "synthetic", Revision: "rev-claude",
		Categories: map[string]bool{"dependency": true, "protected_path": true, "secret_exposure": true, "pattern": true},
		Packages: []captaincode.BlackboxPackageRule{
			{ID: "pkg-exact", Ecosystem: "npm", Name: "example-package", Version: "1.2.3", Kind: "known_malware", Severity: "critical"},
		},
		Paths: []captaincode.BlackboxPathRule{{ID: "path-exact", Path: "config/private/**", Kind: "protected_path", Severity: "critical"}},
		Patterns: []captaincode.BlackboxPatternRule{
			{ID: "pattern-one", Category: "pattern", Pattern: `dangerous-example`, Kind: "heuristic", Severity: "warning"},
		},
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "snapshot.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendStatusRecord(t testing.TB, record captaincode.BlackboxSecurityRecord) {
	t.Helper()
	record.Schema = 1
	record.Time = time.Now().UTC().Format(time.RFC3339Nano)
	if err := captaincode.AppendBlackboxSecurityRecord(record); err != nil {
		t.Fatal(err)
	}
}

// The read half of BB4: hosts, snapshot age, the five dispositions and the
// transports that stay uncovered - counts and path-free ids only.
func TestBlackboxStatusReportsCoverageAndDispositions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX", "audit")
	t.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(dir, "security.jsonl"))
	t.Setenv("CAPTAIN_BLACKBOX_SNAPSHOT", writeStatusSnapshot(t, dir))
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captaincode.EnsureClaudeRedactHook(true); err != nil {
		t.Fatal(err)
	}
	appendStatusRecord(t, captaincode.BlackboxSecurityRecord{
		EventID: "ev1", Phase: "pre", Tool: "bash", Mode: "audit", Host: "claude",
		Disposition: "would_block", Status: "complete", WorkspaceID: "ws_aaaaaaaa", SessionID: "ses-1",
	})
	appendStatusRecord(t, captaincode.BlackboxSecurityRecord{
		EventID: "ev2", Phase: "post", Tool: "write", Mode: "audit", Host: "opencode",
		Disposition: "observed_after_execution", Status: "complete", WorkspaceID: "ws_bbbbbbbb", SessionID: "ses-2",
		Coverage: []captaincode.BlackboxCoverage{{Category: "dependency", Status: "not_covered", Reason: "category_not_in_snapshot"}},
	})

	var out bytes.Buffer
	if err := runBlackboxStatus(nil, &out); err != nil {
		t.Fatal(err)
	}
	report := out.String()
	for _, want := range []string{
		"blackbox   mode audit",
		"claude     covered - the audit composes into `captain redact --hook`",
		"snapshot   claude-fixture synthetic, age ",
		"records    2",
		"would_block 1",
		"observed_after_execution 1",
		"evaluated 0",
		"degraded 0",
		"not_covered 1",
		"hosts      claude 1 · opencode 1",
		"workspaces ws_aaaaaaaa 1 · ws_bbbbbbbb 1",
		"uncovered  codex-cli, grok-cli, cursor-agent",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in report:\n%s", want, report)
		}
	}
	for _, leak := range []string{"dangerous-example", "example-package", "config/private"} {
		if strings.Contains(report, leak) {
			t.Errorf("rule text reaches the report: %q", leak)
		}
	}
}

// An operator who set no switch or a wrong one must be told, and an empty
// host must still show every disposition state and the uncovered transports.
func TestBlackboxStatusEmptyAndUnknownMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(dir, "security.jsonl"))
	t.Setenv("CAPTAIN_BLACKBOX_SNAPSHOT", filepath.Join(dir, "missing.json"))
	t.Setenv("CAPTAIN_BLACKBOX", "bogus")

	var out bytes.Buffer
	if err := runBlackboxStatus(nil, &out); err != nil {
		t.Fatal(err)
	}
	report := out.String()
	for _, want := range []string{
		"mode bogus - unknown",
		"snapshot   unreadable",
		"records    0",
		"would_block 0 · observed_after_execution 0 · evaluated 0 · degraded 0 · not_covered 0",
		"uncovered  codex-cli, grok-cli, cursor-agent",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in report:\n%s", want, report)
		}
	}

	if err := runBlackboxStatus([]string{"extra"}, &out); err == nil {
		t.Fatal("positional arguments must be refused")
	}
}

// Redaction off means the composed hook never installs: the report must say
// that, not "run captain init", which would turn redaction back on.
func TestBlackboxStatusClaudeUncoveredWithRedactionOff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX", "audit")
	t.Setenv("CAPTAIN_REDACT", "off")
	t.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(dir, "security.jsonl"))
	t.Setenv("CAPTAIN_BLACKBOX_SNAPSHOT", filepath.Join(dir, "snapshot.json"))

	var out bytes.Buffer
	if err := runBlackboxStatus(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "claude     not covered - CAPTAIN_REDACT=off keeps the composed hook uninstalled") {
		t.Fatalf("report:\n%s", out.String())
	}
}

func TestBlackboxStatusBounders(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{90 * time.Second, "1 m"},
		{90 * time.Minute, "1 h"},
		{49 * time.Hour, "2 d"},
		{time.Millisecond, "<1 m"},
	} {
		if got := ageText(tc.age); got != tc.want {
			t.Errorf("ageText(%s) = %q, want %q", tc.age, got, tc.want)
		}
	}
	if got := safeField("id\x00\x1b[31m\nid"); got != "id[31mid" {
		t.Errorf("safeField = %q", got)
	}
	if got := safeField("\x00"); got != "-" {
		t.Errorf("safeField of control-only input = %q", got)
	}
}

func benchmarkBlackboxStatus(b *testing.B) string {
	dir := b.TempDir()
	b.Setenv("HOME", dir)
	b.Setenv("CAPTAIN_BLACKBOX", "audit")
	b.Setenv("CAPTAIN_BLACKBOX_SNAPSHOT", filepath.Join(dir, "snapshot.json"))
	b.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(dir, "security.jsonl"))
	snapshot := captaincode.BlackboxSnapshot{
		Schema: 1, ID: "bench", Source: "synthetic", Revision: "rev-bench",
		Categories: map[string]bool{"dependency": true, "protected_path": true, "secret_exposure": true, "pattern": true},
		Packages: []captaincode.BlackboxPackageRule{
			{ID: "pkg-bench", Ecosystem: "npm", Name: "bench-package", Version: "1.0.0", Kind: "known_malware", Severity: "critical"},
		},
		Paths:    []captaincode.BlackboxPathRule{{ID: "path-bench", Path: "config/private/**", Kind: "protected_path", Severity: "critical"}},
		Patterns: []captaincode.BlackboxPatternRule{{ID: "pattern-bench", Category: "pattern", Pattern: "dangerous-marker", Kind: "heuristic", Severity: "warning"}},
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), body, 0o600); err != nil {
		b.Fatal(err)
	}
	workspace := "/workspace/bench"
	for i := 0; i < 200; i++ {
		appendStatusRecord(b, captaincode.BlackboxSecurityRecord{
			EventID: "ev-bench", Phase: "pre", Tool: "bash", Mode: "audit", Host: "claude",
			Disposition: "evaluated", Status: "complete", WorkspaceID: captaincode.BlackboxWorkspaceID(workspace),
		})
	}
	return workspace
}

func BenchmarkBlackboxStatusCollect(b *testing.B) {
	benchmarkBlackboxStatus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		collectBlackboxStatus()
	}
}

func BenchmarkBlackboxWorkspaceStatusCollect(b *testing.B) {
	workspace := benchmarkBlackboxStatus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		collectBlackboxWorkspaceStatus(workspace)
	}
}
