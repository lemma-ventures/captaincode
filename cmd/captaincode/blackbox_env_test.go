package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// main() dispatches `captain blackbox` before it loads the captain files, so a
// terminal that did not export them starts with no CAPTAIN_BLACKBOX_* name at
// all. These tests write the operator's pins into the captain env file and
// check that the command reads them from there: otherwise status reports a
// store that is not configured and the pilot review screens the legacy file
// snapshot instead of the signed rules.

var blackboxPinNames = []string{
	"CAPTAIN_BLACKBOX_STORE", "CAPTAIN_BLACKBOX_KEY_ID", "CAPTAIN_BLACKBOX_PUBLIC_KEY",
	"CAPTAIN_BLACKBOX_TRUST_ROOT", "CAPTAIN_BLACKBOX_GRAPH", "CAPTAIN_BLACKBOX_NETWORK",
}

// bareBlackboxEnvironment removes every name the operator's own shell or an
// earlier test could have left behind, and restores it afterwards.
func bareBlackboxEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range append(blackboxPinNames, "CAPTAIN_BLACKBOX_SNAPSHOT") {
		key := key
		previous, present := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if present {
				os.Setenv(key, previous)
			} else {
				os.Unsetenv(key)
			}
		})
	}
}

// writeCaptainPinsFile stores the six trust pins where LoadBlackboxConfig
// reads them, the way the operator leaves them after a store export.
func writeCaptainPinsFile(t *testing.T, configHome string, fixture blackboxExportCommandFixture) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	lines := []string{
		"CAPTAIN_BLACKBOX_STORE=" + fixture.store,
		"CAPTAIN_BLACKBOX_KEY_ID=" + fixture.pins.KeyID,
		"CAPTAIN_BLACKBOX_PUBLIC_KEY=" + base64.StdEncoding.EncodeToString(fixture.pins.PublicKey),
		"CAPTAIN_BLACKBOX_TRUST_ROOT=" + fixture.pins.TrustRoot,
		"CAPTAIN_BLACKBOX_GRAPH=" + fixture.pins.Graph,
		"CAPTAIN_BLACKBOX_NETWORK=" + fixture.pins.Network,
	}
	dir := filepath.Join(configHome, "captain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBlackboxStatusReadsTheOperatorStoreFromTheEnvFile(t *testing.T) {
	fixture := newBlackboxExportCommandFixture(t)
	if err := runBlackboxExport(fixture.args(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	bareBlackboxEnvironment(t)
	writeCaptainPinsFile(t, filepath.Join(fixture.dir, "config"), fixture)

	var out bytes.Buffer
	if err := runBlackbox([]string{"status"}, bytes.NewReader(nil), &out); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "authenticated") {
		t.Fatalf("report = %s - the pins must come from the captain env file", out.String())
	}
	if strings.Contains(out.String(), "not configured") {
		t.Fatalf("report = %s", out.String())
	}
}

func TestBlackboxPilotScreensAgainstTheEnvFileStore(t *testing.T) {
	fixture := newBlackboxExportCommandFixture(t)
	if err := runBlackboxExport(fixture.args(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	bareBlackboxEnvironment(t)
	writeCaptainPinsFile(t, filepath.Join(fixture.dir, "config"), fixture)
	state := filepath.Join(fixture.dir, "pilot-state")

	var out bytes.Buffer
	if err := runBlackbox([]string{"pilot", "--count", "8", "--workspaces", "2", "--state", state}, bytes.NewReader(nil), &out); err != nil {
		t.Fatalf("pilot: %v\n%s", err, out.String())
	}
	// A store the command never reads leaves every screening degraded - the
	// legacy file snapshot does not exist under this isolated config home -
	// and the gate below counts degraded screenings as failures.
	if !strings.Contains(out.String(), `"verdict":"pass"`) || !strings.Contains(out.String(), `"refusals":0`) {
		t.Fatalf("summary = %s - the configured store must drive every screening", out.String())
	}
}
