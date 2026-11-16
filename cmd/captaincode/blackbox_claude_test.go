package main

// BB4: the Claude composition. What has to be true here is that the input the
// screening sees is the input Claude Code executes, that a refusal files
// nothing, that the audit never refuses, and that no action text reaches the
// private security stream.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// claudeFixture writes a synthetic snapshot whose pattern rule matches only
// the restored form of the command (the "marker" exists after the stand-in is
// put back), so a would_block proves the screening ran after restoration.
func claudeFixture(t *testing.T, dir string) string {
	t.Helper()
	snapshot := captaincode.BlackboxSnapshot{
		Schema: 1, ID: "claude-fixture", Source: "synthetic", Revision: "rev-claude",
		Categories: map[string]bool{"dependency": true, "protected_path": true, "secret_exposure": true, "pattern": true},
		Packages: []captaincode.BlackboxPackageRule{
			{ID: "pkg-exact", Ecosystem: "npm", Name: "example-package", Version: "1.2.3", Kind: "known_malware", Severity: "critical"},
		},
		Paths: []captaincode.BlackboxPathRule{{ID: "path-exact", Path: "config/private/**", Kind: "protected_path", Severity: "critical"}},
		Patterns: []captaincode.BlackboxPatternRule{
			{ID: "pattern-restored", Category: "pattern", Pattern: `/home/jdoe/#[a-z0-9-]+`, Kind: "heuristic", Severity: "critical"},
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

func claudeEnv(t *testing.T, audit bool) (home, state string) {
	t.Helper()
	home = t.TempDir()
	state = filepath.Join(home, "security.jsonl")
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", state)
	t.Setenv("CAPTAIN_BLACKBOX_SNAPSHOT", claudeFixture(t, home))
	if audit {
		t.Setenv("CAPTAIN_BLACKBOX", "audit")
	} else {
		t.Setenv("CAPTAIN_BLACKBOX", "off")
	}
	captaincode.SetIdentityRulesForTest(map[string]string{"/home/jdoe": "/home/captain"})
	return home, state
}

func claudeRecords(t *testing.T, state string) []captaincode.BlackboxSecurityRecord {
	t.Helper()
	records, partial, err := captaincode.ReadBlackboxSecurityRecords(state)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read records: %v", err)
	}
	if partial != 0 {
		t.Fatalf("partial records: %d", partial)
	}
	return records
}

// The core proof: the command the hook screens is the command the hook hands
// back for execution, stand-ins and all - and the record never carries it.
func TestClaudeHookScreensTheExecutedRestore(t *testing.T) {
	_, state := claudeEnv(t, true)
	in := claudeHookInput{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "echo /home/captain/#stand-in-marker"},
		SessionID: "ses-claude-1",
		Cwd:       "/w/project",
	}
	var out bytes.Buffer
	action, screened := redactHookRun(in, &out)
	if !screened {
		t.Fatal("audit mode must screen the input")
	}

	// The executed input: what the hook hands back to Claude Code.
	var decoded struct {
		Hook struct {
			UpdatedInput map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("hook output: %v (%s)", err, out.String())
	}
	executed, _ := decoded.Hook.UpdatedInput["command"].(string)
	if executed != "echo /home/jdoe/#stand-in-marker" {
		t.Fatalf("restored command = %q", executed)
	}
	if action.Command != executed {
		t.Fatalf("screened %q but Claude will execute %q", action.Command, executed)
	}

	records := claudeRecords(t, state)
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	record := records[0]
	if record.Disposition != "would_block" {
		t.Fatalf("disposition = %q reasons = %v status = %q - the rule matches only the restored form", record.Disposition, record.Reasons, record.Status)
	}
	if record.Host != "claude" || record.Tool != "bash" || record.Phase != "pre" {
		t.Fatalf("host/tool/phase = %q/%q/%q", record.Host, record.Tool, record.Phase)
	}
	if record.SessionID != "ses-claude-1" || record.WorkspaceID != captaincode.BlackboxWorkspaceID("/w/project") {
		t.Fatalf("session = %q workspace = %q", record.SessionID, record.WorkspaceID)
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"/home/jdoe", "stand-in-marker", "echo"} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("record leaks %q: %s", leak, body)
		}
	}
}

// A path where nothing executes files nothing: the redaction refusal runs
// before any restoration, and the audit records only actions that will run.
func TestClaudeRefusalAndAuditOffFileNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		audit bool
	}{
		{"audit off", false},
		{"refused read", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, state := claudeEnv(t, tc.audit)
			in := claudeHookInput{ToolName: "Read", ToolInput: map[string]any{"file_path": filepath.Join(t.TempDir(), "id_rsa")}}
			var out bytes.Buffer
			_, screened := redactHookRun(in, &out)
			if tc.audit {
				if screened {
					t.Fatal("a refused action never executes and is never screened")
				}
				if !strings.Contains(out.String(), "permissionDecision") || !strings.Contains(out.String(), "deny") {
					t.Fatalf("expected the redaction refusal, got %s", out.String())
				}
			}
			if records := claudeRecords(t, state); len(records) != 0 {
				t.Fatalf("records = %d, want 0", len(records))
			}
		})
	}
}

// WebFetch rewrites nothing: Claude runs the input as it stands, so that is
// what the screening sees.
func TestClaudeWebFetchScreensUntouchedInput(t *testing.T) {
	_, state := claudeEnv(t, true)
	in := claudeHookInput{
		ToolName:  "WebFetch",
		ToolInput: map[string]any{"url": "https://example.invalid/?q=marker"},
		SessionID: "ses-claude-2",
	}
	var out bytes.Buffer
	action, screened := redactHookRun(in, &out)
	want, _ := in.ToolInput["url"].(string)
	if !screened || action.Kind != "fetch" {
		t.Fatalf("screened = %v action = %+v", screened, action)
	}
	if len(action.URLs) != 1 || action.URLs[0] != want {
		t.Fatalf("screened %q, Claude runs %q", action.URLs, want)
	}
	if out.Len() != 0 {
		t.Fatalf("nothing is rewritten, so no hook output is due: %s", out.String())
	}
	records := claudeRecords(t, state)
	if len(records) != 1 || records[0].Tool != "webfetch" || records[0].SessionID != "ses-claude-2" {
		t.Fatalf("records = %+v", records)
	}
}

// The composed path reads the same switch as the plugin, and its own failure
// can never fail the tool call: a state file it cannot write still returns
// the screened action and says so on stderr.
func TestClaudeAuditDegradesInsteadOfRefusing(t *testing.T) {
	home, _ := claudeEnv(t, true)
	// A directory where the record file belongs: the append must fail.
	blocker := filepath.Join(home, "blocked")
	if err := os.Mkdir(blocker, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(blocker, "security.jsonl"))
	in := claudeHookInput{ToolName: "Bash", ToolInput: map[string]any{"command": "echo 42"}}
	var out bytes.Buffer
	action, screened := redactHookRun(in, &out)
	if !screened || action.Command != "echo 42" {
		t.Fatalf("screened = %v action = %+v", screened, action)
	}
	if out.Len() != 0 {
		t.Fatalf("an audit failure must not change what Claude runs: %s", out.String())
	}
}

// Operator-facing coverage: the composed hook is installed as one entry and
// the settings file keeps its own hooks, with no second Captain entry.
func TestClaudeHookInstalledWithoutASecondEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo user-hook"}]}]}}`
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captaincode.EnsureClaudeRedactHook(true); err != nil {
		t.Fatal(err)
	}
	if !captaincode.ClaudeHookInstalled("captain redact --hook") {
		t.Fatal("the composed hook must be reported installed")
	}
	if captaincode.ClaudeHookInstalled("captain blackbox hook") {
		t.Fatal("BB4 composes into the redact hook: no second Captain entry")
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "echo user-hook") {
		t.Fatalf("the user's own hook must survive: %s", raw)
	}
}

func TestClaudeHookTwoWorkspaceIsolation(t *testing.T) {
	_, state := claudeEnv(t, true)
	inputs := []claudeHookInput{
		{ToolName: "Bash", ToolInput: map[string]any{"command": "echo workspace-a"}, SessionID: "ses-workspace-a", Cwd: "/w/project-a"},
		{ToolName: "Bash", ToolInput: map[string]any{"command": "echo workspace-b"}, SessionID: "ses-workspace-b", Cwd: "/w/project-b"},
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, input := range inputs {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var out bytes.Buffer
			if _, screened := redactHookRun(input, &out); !screened {
				t.Errorf("%s was not screened", input.SessionID)
			}
		}()
	}
	close(start)
	wg.Wait()
	records := claudeRecords(t, state)
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	seen := map[string]captaincode.BlackboxSecurityRecord{}
	for _, record := range records {
		seen[record.SessionID] = record
	}
	for _, input := range inputs {
		record, ok := seen[input.SessionID]
		if !ok {
			t.Fatalf("no record for %s", input.SessionID)
		}
		if record.WorkspaceID != captaincode.BlackboxWorkspaceID(input.Cwd) {
			t.Fatalf("%s joined workspace %q", input.SessionID, record.WorkspaceID)
		}
		if record.Host != "claude" || record.TaskID != "" {
			t.Fatalf("unexpected identity join: %+v", record)
		}
	}
}

func BenchmarkClaudeScreenHook(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("HOME", dir)
	b.Setenv("CAPTAIN_BLACKBOX_STATE", filepath.Join(dir, "security.jsonl"))
	b.Setenv("CAPTAIN_BLACKBOX_SNAPSHOT", filepath.Join(dir, "snapshot.json"))
	snapshot := captaincode.BlackboxSnapshot{
		Schema: 1, ID: "bench", Source: "synthetic", Revision: "rev-bench",
		Categories: map[string]bool{"dependency": true, "protected_path": true, "secret_exposure": true, "pattern": true},
		Packages: []captaincode.BlackboxPackageRule{
			{ID: "pkg-bench", Ecosystem: "npm", Name: "bench-package", Version: "1.0.0", Kind: "known_malware", Severity: "critical"},
		},
		Paths: []captaincode.BlackboxPathRule{{ID: "path-bench", Path: "config/private/**", Kind: "protected_path", Severity: "critical"}},
		Patterns: []captaincode.BlackboxPatternRule{
			{ID: "p1", Category: "pattern", Pattern: `dangerous-marker`, Kind: "heuristic", Severity: "warning"},
		},
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), body, 0o600); err != nil {
		b.Fatal(err)
	}
	input := map[string]any{
		"command":   "npm run build-index --marker dangerous-marker --out ./dist/index.js",
		"file_path": "/home/user/project/src/index.ts",
		"content":   "export const index = 1\n",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := screenClaudeToolInput("bash", "ses-bench", "/home/user/project", input); err != nil {
			b.Fatal(err)
		}
	}
}
