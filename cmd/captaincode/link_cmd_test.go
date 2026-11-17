package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func linkCapture(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	callErr := fn()
	os.Stdout = saved
	w.Close()
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out), callErr
}

func TestRunLinkUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"frobnicate"}, {"add"}, {"status", "extra"}} {
		if err := runLink(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("runLink(%v) err = %v, want a usage error", args, err)
		}
	}
}

func TestRunLinkAddAndStatus(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "captain"), 0o700); err != nil {
		t.Fatal(err)
	}
	presence := filepath.Join(home, ".config", "captain", "presence.json")
	if err := os.WriteFile(presence, []byte(`{"v":1,"machine":"machine-cli-test","window_seconds":900}`), 0o600); err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()

	out, err := linkCapture(t, func() error { return runLink([]string{"add", folder}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "linked ") || !strings.Contains(out, "active") {
		t.Fatalf("add printed %q", out)
	}

	if _, err := captaincode.EnsureLinkKey(); err != nil {
		t.Fatal(err)
	}
	out, err = linkCapture(t, func() error { return runLink([]string{"status", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Key       string `json:"key"`
		MachineID string `json:"machine_id"`
		Linked    int    `json:"linked"`
		Folders   []struct {
			Label string `json:"label"`
			Mode  string `json:"mode"`
			Root  string `json:"root"`
		} `json:"folders"`
	}
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("status is not JSON: %v\n%s", err, out)
	}
	if record.Linked != 1 || record.Key == "" {
		t.Fatalf("status %+v", record)
	}
	if record.MachineID != "machine-cli-test" {
		t.Fatalf("machine id %q, want the presence identity", record.MachineID)
	}
	if record.Folders[0].Root != filepath.Clean(record.Folders[0].Root) || record.Folders[0].Mode != "active" {
		t.Fatalf("folder record %+v", record.Folders[0])
	}

	if _, err := linkCapture(t, func() error { return runLink([]string{"status"}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := linkCapture(t, func() error { return runLink([]string{"off", folder}) }); err != nil {
		t.Fatal(err)
	}
	out, err = linkCapture(t, func() error { return runLink([]string{"off"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing was linked") {
		t.Fatalf("off printed %q", out)
	}
}

func TestRunLinkQueueMode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	folder := t.TempDir()
	if _, err := linkCapture(t, func() error {
		return runLink([]string{"add", folder, "--queue"})
	}); err != nil {
		t.Fatal(err)
	}
	folders, err := captaincode.LinkFolders()
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Mode != captaincode.LinkModeQueue {
		t.Fatalf("folders %+v", folders)
	}
}

func TestRunLinkStatusWithoutKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	out, err := linkCapture(t, func() error { return runLink([]string{"status"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not created") || !strings.Contains(out, "linked folders: 0") {
		t.Fatalf("empty status printed %q", out)
	}
}

func TestRunLinkRefusesWorkers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(captaincode.GuardEnv, "1")
	for _, args := range [][]string{{"add", t.TempDir()}, {"off"}} {
		err := runLink(args)
		if err == nil || !strings.Contains(err.Error(), "terminal") {
			t.Fatalf("runLink(%v) err = %v, want a terminal refusal", args, err)
		}
	}
	folders, err := captaincode.LinkFolders()
	if err != nil || len(folders) != 0 {
		t.Fatalf("a worker changed the registry: %+v, %v", folders, err)
	}
}
