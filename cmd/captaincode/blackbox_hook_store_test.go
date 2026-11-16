package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blackboxHookStoreFixture exports one envelope into a real store and sets
// every trust pin, so the hook resolves rules from the authenticated store.
func blackboxHookStoreFixture(t *testing.T, dir string) {
	t.Helper()
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
}

func TestBlackboxHookScreensAgainstTheConfiguredStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", blackboxHookState(t, dir))
	blackboxHookStoreFixture(t, dir)
	input := `{"tool":"bash","cwd":"/Users/x/repo","session":"ses-1","phase":"pre","args":{"command":"npm install evil-pkg@1.2.3"}}`
	var stdout bytes.Buffer
	if err := runBlackbox([]string{"hook", "--mode", "audit"}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var out blackboxHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The exported fixture carries one high-severity package rule: the
	// screening records the finding without refusing in audit mode.
	if out.Disposition != "evaluated" || out.Findings != 1 {
		t.Fatalf("out = %+v - the store rules must drive the screening", out)
	}
}

func TestBlackboxHookHalfConfiguredStoreDegrades(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", blackboxHookState(t, dir))
	blackboxHookStoreFixture(t, dir)
	os.Unsetenv("CAPTAIN_BLACKBOX_NETWORK")
	input := `{"tool":"bash","cwd":"/Users/x/repo","session":"ses-1","phase":"pre","args":{"command":"npm install evil-pkg@1.2.3"}}`
	var stdout bytes.Buffer
	if err := runBlackbox([]string{"hook", "--mode", "audit"}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var out blackboxHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Disposition != "degraded" {
		t.Fatalf("out = %+v - a half-configured store must degrade, not fall back", out)
	}
}

func TestBlackboxHookUntrustedStorePinDegrades(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", blackboxHookState(t, dir))
	blackboxHookStoreFixture(t, dir)
	t.Setenv("CAPTAIN_BLACKBOX_TRUST_ROOT", "wrong-root")
	input := `{"tool":"bash","cwd":"/Users/x/repo","session":"ses-1","phase":"pre","args":{"command":"ls"}}`
	var stdout bytes.Buffer
	if err := runBlackbox([]string{"hook", "--mode", "audit"}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var out blackboxHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Disposition != "degraded" {
		t.Fatalf("out = %+v - an untrusted store must degrade", out)
	}
}

func TestBlackboxHookWithoutStoreKeepsTheFileSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", blackboxHookState(t, dir))
	t.Setenv("CAPTAIN_BLACKBOX_SNAPSHOT", blackboxHookSnapshot(t, dir))
	input := `{"tool":"bash","cwd":"/Users/x/repo","session":"ses-1","phase":"pre","args":{"command":"npm install example-package@1.2.3"}}`
	var stdout bytes.Buffer
	if err := runBlackbox([]string{"hook", "--mode", "audit"}, strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("hook: %v", err)
	}
	var out blackboxHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Disposition != "would_block" {
		t.Fatalf("out = %+v - the file snapshot path is unchanged", out)
	}
}
