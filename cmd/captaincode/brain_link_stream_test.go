package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// The brain's side of the outbound stream (roadmap row F6, step 3), tested
// against local state only: these tests never reach a hub. They pin the four
// things the stream trusts from disk - the pairing in link.json, the machine
// identity the presence endpoint already reports, the labels that may travel,
// and the TUI signal per folder - plus the words it prints when nothing is
// reportable.

// linkStreamFixture gives a test a machine: its own home for the presence
// store, its own config home for link.json and link.key, and a brain with no
// other wiring.
func linkStreamFixture(t *testing.T) *brain {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".config", "captain"), 0o700); err != nil {
		t.Fatal(err)
	}
	return teamBrain()
}

// linkStreamWritePresence writes the presence configuration the way the
// presence store itself writes it: one 0600 file under the home directory.
func linkStreamWritePresence(t *testing.T, machine string, folders map[string]string) {
	t.Helper()
	raw, err := json.Marshal(struct {
		Version int               `json:"v"`
		Machine string            `json:"machine"`
		Window  int64             `json:"window_seconds"`
		Folders map[string]string `json:"folders"`
	}{Version: 1, Machine: machine, Window: 900, Folders: folders})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("HOME"), ".config", "captain", "presence.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// linkStreamWriteRegistry writes link.json directly: pairing happens over
// HTTP against the real hub, and these tests are about what happens after
// it, so the registry is the thing under test.
func linkStreamWriteRegistry(t *testing.T, folders []captaincode.LinkFolder, hub *captaincode.LinkHub) {
	t.Helper()
	if folders == nil {
		folders = []captaincode.LinkFolder{}
	}
	raw, err := json.Marshal(struct {
		Version int                      `json:"version"`
		Folders []captaincode.LinkFolder `json:"folders"`
		Hub     *captaincode.LinkHub     `json:"hub,omitempty"`
	}{Version: 1, Folders: folders, Hub: hub})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "captain", "link.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// linkStreamHub is a pinned hub the way pairing leaves one.
func linkStreamHub(approvedAt int64) *captaincode.LinkHub {
	_, pub, _ := ed25519.GenerateKey(nil)
	return &captaincode.LinkHub{
		URL:         "https://hub.example.test",
		PublicKey:   base64.RawURLEncoding.EncodeToString(pub),
		PairID:      "pair-pairing-test",
		Code:        "ABCD2345",
		RequestedAt: 1,
		ExpiresAt:   9,
		ApprovedAt:  approvedAt,
	}
}

func TestLinkBrainVersionIsAHubLabel(t *testing.T) {
	stamped := captaincode.BuildRevision
	t.Cleanup(func() { captaincode.BuildRevision = stamped })

	if !captaincode.LinkPresenceLabel(linkBrainVersion()) {
		t.Fatalf("the build stamp %q is outside the hub's label profile", linkBrainVersion())
	}
	captaincode.BuildRevision = "0123456789abcdef"
	if got := linkBrainVersion(); got != "captain 0123456" {
		t.Fatalf("a stamped build names itself %q", got)
	}
	captaincode.BuildRevision = ""
	got := linkBrainVersion()
	if !strings.HasPrefix(got, "captain ") || !captaincode.LinkPresenceLabel(got) {
		t.Fatalf("an unstamped build names itself %q", got)
	}
}

func TestLinkPresenceFoldersUsesLabelsAndTUIState(t *testing.T) {
	b := linkStreamFixture(t)

	canonical := func(dir string) string {
		lead, err := captaincode.LinkCanonicalRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		return lead
	}
	linked := func(dir string) {
		if _, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive); err != nil {
			t.Fatal(err)
		}
	}
	oak, elm := t.TempDir(), t.TempDir()
	long := t.TempDir()
	orphan := t.TempDir()
	unlinked := t.TempDir()
	linked(oak)
	linked(elm)
	linked(long)
	linked(orphan)
	longLabel := strings.Repeat("x", 81)
	linkStreamWritePresence(t, "machine-stream-test", map[string]string{
		"oak":      canonical(oak),
		"elm":      canonical(elm),
		longLabel:  canonical(long),
		"unlinked": canonical(unlinked),
	})

	// The sidebar polled oak's inbox within the window; elm has no TUI up.
	b.inbox.polled = map[string]time.Time{canonical(oak): time.Now()}

	linkNotices.Lock()
	linkNotices.seen = map[string]bool{}
	linkNotices.Unlock()

	var got []captaincode.LinkPresenceFolder
	out, err := linkCapture(t, func() error {
		var e error
		got, e = b.linkPresenceFolders()
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Label != "elm" || got[1].Label != "oak" {
		t.Fatalf("snapshot: %+v", got)
	}
	if !got[1].TUIOpen || got[0].TUIOpen {
		t.Fatalf("TUI states wrong: %+v", got)
	}
	if !strings.Contains(out, "no presence label") {
		t.Fatalf("an unlabelled folder was silent: %q", out)
	}
	if !strings.Contains(out, "80 characters") {
		t.Fatalf("an oversized label was silent: %q", out)
	}
	if strings.Contains(out, "unlinked") || strings.Contains(out, oak) || strings.Contains(out, elm) {
		t.Fatalf("a path or an unlinked folder name reached the log: %q", out)
	}
	// The reason is printed once per process, not once per beat.
	out, err = linkCapture(t, func() error {
		_, e := b.linkPresenceFolders()
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "no presence label") || strings.Contains(out, "80 characters") {
		t.Fatalf("a skip reason printed twice: %q", out)
	}
}

func TestLinkPresenceFoldersCapsAtTheHubCeiling(t *testing.T) {
	b := linkStreamFixture(t)
	base := t.TempDir()
	folders := make(map[string]string, 201)
	for i := 0; i < 201; i++ {
		dir := filepath.Join(base, fmt.Sprintf("d%03d", i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		folder, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
		if err != nil {
			t.Fatal(err)
		}
		folders[fmt.Sprintf("lab%03d", i)] = folder.Root
	}
	linkStreamWritePresence(t, "machine-stream-test", folders)

	var got []captaincode.LinkPresenceFolder
	out, err := linkCapture(t, func() error {
		var e error
		got, e = b.linkPresenceFolders()
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hub takes 200 folders") {
		t.Fatalf("the ceiling was silent: %q", out)
	}
	if len(got) != 200 {
		t.Fatalf("hub ceiling ignored: %d folders", len(got))
	}
	if got[0].Label != "lab000" || got[199].Label != "lab199" {
		t.Fatalf("the hub gets a different set each beat: %q..%q", got[0].Label, got[199].Label)
	}
}

func TestLinkStreamReadyWithoutPairingIsIdle(t *testing.T) {
	b := linkStreamFixture(t)
	state, err := b.linkStreamReady()
	if err != nil {
		t.Fatal(err)
	}
	if state.Active || !strings.Contains(state.Why, "no hub") {
		t.Fatalf("an unpaired machine is active: %+v", state)
	}
}

func TestLinkStreamReadyWaitsForApproval(t *testing.T) {
	b := linkStreamFixture(t)
	linkStreamWriteRegistry(t, nil, linkStreamHub(0))
	state, err := b.linkStreamReady()
	if err != nil {
		t.Fatal(err)
	}
	if state.Active || !strings.Contains(state.Why, "approval") {
		t.Fatalf("a pending pairing is active: %+v", state)
	}
}

func TestLinkStreamReadyNeedsReportableFolder(t *testing.T) {
	b := linkStreamFixture(t)
	linkStreamWritePresence(t, "machine-stream-test", map[string]string{})
	linkStreamWriteRegistry(t, nil, linkStreamHub(time.Now().Unix()))
	if _, err := captaincode.EnsureLinkKey(); err != nil {
		t.Fatal(err)
	}
	state, err := b.linkStreamReady()
	if err != nil {
		t.Fatal(err)
	}
	if state.Active || !strings.Contains(state.Why, "no linked folder") {
		t.Fatalf("an approved machine with no folders is active: %+v", state)
	}
}

func TestLinkStreamReadyCarriesLocalState(t *testing.T) {
	b := linkStreamFixture(t)
	dir := t.TempDir()
	folder, err := captaincode.LinkAdd(dir, captaincode.LinkModeActive)
	if err != nil {
		t.Fatal(err)
	}
	linkStreamWritePresence(t, "machine-stream-test", map[string]string{"oak": folder.Root})
	linkStreamWriteRegistry(t, []captaincode.LinkFolder{folder}, linkStreamHub(time.Now().Unix()))
	if _, err := captaincode.EnsureLinkKey(); err != nil {
		t.Fatal(err)
	}

	state, err := b.linkStreamReady()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Active || state.Why != "" {
		t.Fatalf("an approved pairing with one folder is idle: %+v", state)
	}
	opts := state.Options
	if opts.MachineID != "machine-stream-test" {
		t.Fatalf("machine identity: %q", opts.MachineID)
	}
	if opts.BrainVersion != linkBrainVersion() {
		t.Fatalf("brain version: %q", opts.BrainVersion)
	}
	if opts.Hub.URL != "https://hub.example.test" || opts.Hub.ApprovedAt == 0 {
		t.Fatalf("pinned hub: %+v", opts.Hub)
	}
	if len(opts.Key) != ed25519.PrivateKeySize {
		t.Fatal("the link key did not travel with the state")
	}
	if opts.Folders == nil {
		t.Fatal("no snapshot provider")
	}
	folders, err := opts.Folders()
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Label != "oak" {
		t.Fatalf("snapshot: %+v", folders)
	}
}

func TestLinkStreamSupervisorNotesAnUnpairedMachineOnce(t *testing.T) {
	b := linkStreamFixture(t)
	done := make(chan struct{})
	out, err := linkCapture(t, func() error {
		go b.linkStreamSupervisor(done)
		// The first source read happens at once; an idle machine then
		// sleeps on the poll without dialing anywhere.
		time.Sleep(300 * time.Millisecond)
		close(done)
		time.Sleep(50 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(out, "\n"); lines != 1 || !strings.Contains(out, "no hub is paired") {
		t.Fatalf("the unpaired machine printed %d line(s): %q", lines, out)
	}
}
