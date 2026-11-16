package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// blackboxPilotFixture gives the pilot a real exported store with every trust
// pin configured, the same way the hook store tests do, plus a disposable
// HOME so no real override file can suppress a refusal.
func blackboxPilotFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_OVERRIDES", filepath.Join(dir, "overrides.json"))
	fixture := newBlackboxExportCommandFixture(t)
	fixture.store = filepath.Join(dir, "store")
	fixture.output = filepath.Join(dir, "envelope.json")
	if err := runBlackboxExport(fixture.args(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTAIN_BLACKBOX_STORE", fixture.store)
	t.Setenv("CAPTAIN_BLACKBOX_KEY_ID", fixture.pins.KeyID)
	t.Setenv("CAPTAIN_BLACKBOX_PUBLIC_KEY", base64.StdEncoding.EncodeToString(fixture.pins.PublicKey))
	t.Setenv("CAPTAIN_BLACKBOX_TRUST_ROOT", fixture.pins.TrustRoot)
	t.Setenv("CAPTAIN_BLACKBOX_GRAPH", fixture.pins.Graph)
	t.Setenv("CAPTAIN_BLACKBOX_NETWORK", fixture.pins.Network)
	return dir
}

// blackboxPilotCriticalStore exports a store whose one dependency rule is
// critical severity, so an enforce refusal can fire. The shared fixture
// ruleset tops out at high severity, which enforce mode records only.
func blackboxPilotCriticalStore(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "pkg", "captaincode", "testdata", "blackbox", "upstream_ruleset.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cache map[string]any
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatal(err)
	}
	dependency, _ := cache["dependency"].(map[string]any)
	entry, _ := dependency["npm:evil-pkg@1.2.3"].(map[string]any)
	if entry == nil {
		t.Fatal("fixture lost its evil-pkg dependency rule")
	}
	entry["severity"] = "critical"
	encoded, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(dir, "critical-cache.json")
	if err := os.WriteFile(cachePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := newBlackboxExportCommandFixture(t)
	fixture.cache = cachePath
	fixture.store = filepath.Join(dir, "critical-store")
	fixture.output = filepath.Join(dir, "critical-envelope.json")
	if err := runBlackboxExport(fixture.args(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTAIN_BLACKBOX_STORE", fixture.store)
	t.Setenv("CAPTAIN_BLACKBOX_KEY_ID", fixture.pins.KeyID)
	t.Setenv("CAPTAIN_BLACKBOX_PUBLIC_KEY", base64.StdEncoding.EncodeToString(fixture.pins.PublicKey))
	t.Setenv("CAPTAIN_BLACKBOX_TRUST_ROOT", fixture.pins.TrustRoot)
	t.Setenv("CAPTAIN_BLACKBOX_GRAPH", fixture.pins.Graph)
	t.Setenv("CAPTAIN_BLACKBOX_NETWORK", fixture.pins.Network)
}

func TestBlackboxPilotPassesOneThousandBenignActions(t *testing.T) {
	dir := blackboxPilotFixture(t)
	state := filepath.Join(dir, "pilot-state")
	var stdout bytes.Buffer
	if err := runBlackboxPilot([]string{"--count", "1000", "--state", state}, &stdout); err != nil {
		t.Fatalf("pilot: %v\n%s", err, stdout.String())
	}
	var summary blackboxPilotSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Verdict != "pass" || summary.Refusals != 0 {
		t.Fatalf("verdict %s refusals %d failures %+v", summary.Verdict, summary.Refusals, summary.Failures)
	}
	if summary.Count != 1000 || summary.PerHost["opencode"] != 500 || summary.PerHost["claude"] != 500 {
		t.Fatalf("count %d hosts %+v - both hosts must carry half the corpus", summary.Count, summary.PerHost)
	}
	if summary.Workspaces != 2 {
		t.Fatalf("workspaces %d", summary.Workspaces)
	}
	for disposition := range summary.Dispositions {
		if disposition == "degraded" || disposition == "error" || disposition == "" {
			t.Fatalf("disposition %q appeared - the gate must explain every screening", disposition)
		}
	}
	records, err := blackboxPilotRecordCount(state)
	if err != nil {
		t.Fatal(err)
	}
	// The claude path screens only tools it composes; corpus tools all
	// screen, so the stream must hold exactly one record per action.
	if records != 1000 {
		t.Fatalf("records %d - one record per screened action is required", records)
	}
}

func TestBlackboxPilotFailsOnAMatchingAction(t *testing.T) {
	dir := blackboxPilotFixture(t)
	blackboxPilotCriticalStore(t, dir)
	// The critical store refuses evil-pkg: a matching install must be
	// refused, and the pilot must report it as a failure rather than pass.
	original := blackboxPilotCommands[0]
	blackboxPilotCommands[0] = "npm install evil-pkg@1.2.3"
	defer func() { blackboxPilotCommands[12] = original }()
	var stdout bytes.Buffer
	err := runBlackboxPilot([]string{"--count", "24", "--state", filepath.Join(dir, "p")}, &stdout)
	if err == nil {
		t.Fatalf("pilot passed a corpus that contains a matching action\n%s", stdout.String())
	}
	var summary blackboxPilotSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Verdict != "fail" || summary.Refusals == 0 {
		t.Fatalf("verdict %s refusals %d", summary.Verdict, summary.Refusals)
	}
	refused := false
	for _, failure := range summary.Failures {
		if failure.Host == "opencode" && failure.Reason != "" {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("failures %+v - the refusal must name its reason", summary.Failures)
	}
}

func TestBlackboxPilotHalfConfiguredStoreFailsClosed(t *testing.T) {
	dir := blackboxPilotFixture(t)
	t.Setenv("CAPTAIN_BLACKBOX_NETWORK", "") // half-configured: degrade, never fall back
	var stdout bytes.Buffer
	err := runBlackboxPilot([]string{"--count", "4", "--state", filepath.Join(dir, "p")}, &stdout)
	if err == nil {
		t.Fatalf("pilot passed with a half-configured store\n%s", stdout.String())
	}
	var summary blackboxPilotSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Verdict != "fail" || summary.Refusals == 0 {
		t.Fatalf("verdict %s refusals %d - a half-configured store must fail the gate", summary.Verdict, summary.Refusals)
	}
	for _, failure := range summary.Failures {
		if !strings.Contains(failure.Reason, "unavailable") {
			t.Fatalf("failure %+v - the reason must name the unavailable check", failure)
		}
	}
}

func TestBlackboxPilotRequiresState(t *testing.T) {
	var stdout bytes.Buffer
	if err := runBlackboxPilot([]string{}, &stdout); err == nil || !strings.Contains(err.Error(), "--state") {
		t.Fatalf("err = %v - the pilot must require an explicit disposable state", err)
	}
}

func BenchmarkBlackboxPilotOneThousand(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("HOME", dir)
	b.Setenv("CAPTAIN_BLACKBOX_OVERRIDES", filepath.Join(dir, "overrides.json"))
	fixture := newBlackboxExportCommandFixture(b)
	fixture.store = filepath.Join(dir, "store")
	fixture.output = filepath.Join(dir, "envelope.json")
	if err := runBlackboxExport(fixture.args(), &bytes.Buffer{}); err != nil {
		b.Fatal(err)
	}
	b.Setenv("CAPTAIN_BLACKBOX_STORE", fixture.store)
	b.Setenv("CAPTAIN_BLACKBOX_KEY_ID", fixture.pins.KeyID)
	b.Setenv("CAPTAIN_BLACKBOX_PUBLIC_KEY", base64.StdEncoding.EncodeToString(fixture.pins.PublicKey))
	b.Setenv("CAPTAIN_BLACKBOX_TRUST_ROOT", fixture.pins.TrustRoot)
	b.Setenv("CAPTAIN_BLACKBOX_GRAPH", fixture.pins.Graph)
	b.Setenv("CAPTAIN_BLACKBOX_NETWORK", fixture.pins.Network)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var stdout bytes.Buffer
		if err := runBlackboxPilot([]string{"--count", "1000", "--state", filepath.Join(dir, fmt.Sprintf("bench-%d", i))}, &stdout); err != nil {
			b.Fatal(err)
		}
	}
}

func TestBlackboxPilotClaudePathUsesTheAuthenticatedStore(t *testing.T) {
	dir := blackboxPilotFixture(t)
	blackboxPilotCriticalStore(t, dir)
	t.Setenv("CAPTAIN_BLACKBOX", "enforce")
	t.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(dir, "security.jsonl"))
	// The Claude composition must refuse the store's critical package rule
	// when it resolves rules from the configured store.
	var out bytes.Buffer
	redactHookRun(claudeHookInput{
		ToolName: "Bash", SessionID: "ses-store", Cwd: dir,
		ToolInput: map[string]any{"command": "npm install evil-pkg@1.2.3"},
	}, &out)
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output %q: %v - the Claude path must answer the denial", out.String(), err)
	}
	hookSpecific, _ := decoded["hookSpecificOutput"].(map[string]any)
	decision, _ := hookSpecific["permissionDecision"].(string)
	if decision != "deny" {
		t.Fatalf("decision %q output %s - the Claude path must use the authenticated store", decision, out.String())
	}
	var _ = captaincode.BlackboxAction{}
}
