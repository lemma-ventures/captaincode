package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// cmdHubStub is the hub half the pairing command talks to: it verifies the
// brain's signature, answers with a live code, and approves on a schedule so
// the command can wait.
type cmdHubStub struct {
	mu            sync.Mutex
	key           ed25519.PrivateKey
	brainKey      []byte
	pairCount     int
	statusCount   int
	approveOnCall int
	errStatus     int
}

func newCmdHubStub() *cmdHubStub {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return &cmdHubStub{key: key}
}

func (h *cmdHubStub) sign(machine, kind string, payload any) captaincode.LinkEnvelope {
	e, err := captaincode.LinkSign(h.key, machine, "hub_to_brain", kind, payload, time.Now())
	if err != nil {
		panic(err)
	}
	return e
}

func (h *cmdHubStub) write(w http.ResponseWriter, status int, e captaincode.LinkEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

func (h *cmdHubStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 65*1024))
	if err != nil || r.URL.Path == "" {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var e captaincode.LinkEnvelope
	if err := captaincode.LinkDecodeEnvelope(raw, &e); err != nil {
		http.Error(w, "bad envelope", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch r.URL.Path {
	case "/api/v1/link/pair":
		h.pairCount++
		if h.errStatus != 0 {
			h.write(w, h.errStatus, h.sign(e.MachineID, "link.error", map[string]int{"status": h.errStatus}))
			return
		}
		var p struct {
			MachineID string `json:"machine_id"`
			PublicKey string `json:"public_key"`
			Label     string `json:"label"`
		}
		if err := captaincode.LinkDecodePayload(e, &p); err != nil || p.Label == "" {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		pub, err := base64.RawURLEncoding.DecodeString(p.PublicKey)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			http.Error(w, "bad key", http.StatusBadRequest)
			return
		}
		if err := captaincode.LinkVerify(ed25519.PublicKey(pub), e, "brain_to_hub", time.Now()); err != nil {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		h.brainKey = pub
		h.write(w, http.StatusOK, h.sign(e.MachineID, "pair.pending", map[string]any{
			"code":           "ABCD2345",
			"pair_id":        e.ID,
			"expires_at":     time.Now().Add(5 * time.Minute),
			"hub_public_key": base64.RawURLEncoding.EncodeToString(h.key.Public().(ed25519.PublicKey)),
		}))
	case "/api/v1/link/pair/status":
		h.statusCount++
		if h.errStatus != 0 {
			h.write(w, h.errStatus, h.sign(e.MachineID, "link.error", map[string]int{"status": h.errStatus}))
			return
		}
		if err := captaincode.LinkVerify(ed25519.PublicKey(h.brainKey), e, "brain_to_hub", time.Now()); err != nil {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		state := "pending"
		if h.approveOnCall > 0 && h.statusCount >= h.approveOnCall {
			state = "approved"
		}
		h.write(w, http.StatusOK, h.sign(e.MachineID, "pair.status", map[string]string{"status": state}))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// linkMachineHome isolates the key, registry and machine identity.
func linkMachineHome(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	conf := filepath.Join(home, ".config", "captain")
	if err := os.MkdirAll(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "presence.json"), []byte(`{"v":1,"machine":"machine-cmd-pair","window_seconds":900}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func linkHubStub(t *testing.T, stub *cmdHubStub) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewTLSServer(stub)
	t.Cleanup(server.Close)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	path := filepath.Join(t.TempDir(), "hub-ca.pem")
	if err := os.WriteFile(path, cert, 0o644); err != nil {
		t.Fatal(err)
	}
	return server, path
}

func TestRunLinkPairUsage(t *testing.T) {
	for _, args := range [][]string{
		{"pair"},
		{"pair", "https://hub.example", "--frobnicate"},
		{"pair", "https://hub.example", "--wait", "soon"},
		{"pair", "https://hub.example", "https://other.example"},
		{"unpair", "extra"},
	} {
		err := runLink(args)
		if err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("runLink(%v) err = %v, want a usage error", args, err)
		}
	}
}

func TestRunLinkPairRefusesWorkers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(captaincode.GuardEnv, "1")
	for _, args := range [][]string{{"pair", "https://hub.example"}, {"unpair"}} {
		if err := runLink(args); err == nil || !strings.Contains(err.Error(), "terminal") {
			t.Fatalf("runLink(%v) err = %v, want a terminal refusal", args, err)
		}
	}
	if _, has, err := captaincode.LinkHubState(); err != nil || has {
		t.Fatalf("a worker changed the hub pin: has=%v err=%v", has, err)
	}
}

func TestRunLinkPairApprovesWhileWaiting(t *testing.T) {
	linkMachineHome(t)
	stub := newCmdHubStub()
	stub.approveOnCall = 1
	server, ca := linkHubStub(t, stub)

	out, err := linkCapture(t, func() error {
		return runLink([]string{"pair", server.URL, "--ca", ca, "--wait", "10s"})
	})
	if err != nil {
		t.Fatalf("pair: %v\n%s", err, out)
	}
	for _, want := range []string{"pairing code: ABCD2345", "approve it at: " + server.URL + "/captains/pair/", "waiting for approval", "paired"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pair output does not contain %q:\n%s", want, out)
		}
	}

	out, err = linkCapture(t, func() error { return runLink([]string{"status", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Hub struct {
			URL      string `json:"url"`
			Key      string `json:"key"`
			State    string `json:"state"`
			Code     string `json:"code"`
			Approved string `json:"approved_at"`
		} `json:"hub"`
	}
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("status is not JSON: %v\n%s", err, out)
	}
	if record.Hub.URL != server.URL || record.Hub.State != captaincode.LinkPairApproved || record.Hub.Approved == "" {
		t.Fatalf("hub record %+v", record.Hub)
	}
	if record.Hub.Code != "" || !strings.HasPrefix(record.Hub.Key, "sha256:") {
		t.Fatalf("approved hub must carry no code and a fingerprint: %+v", record.Hub)
	}

	if _, err = linkCapture(t, func() error { return runLink([]string{"unpair"}) }); err != nil {
		t.Fatal(err)
	}
	if _, has, err := captaincode.LinkHubState(); err != nil || has {
		t.Fatalf("unpair left a hub: has=%v err=%v", has, err)
	}
	out, err = linkCapture(t, func() error { return runLink([]string{"unpair"}) })
	if err != nil || !strings.Contains(out, "no hub is paired") {
		t.Fatalf("second unpair: %q, %v", out, err)
	}
}

func TestRunLinkPairStaysPendingWithoutWaiting(t *testing.T) {
	linkMachineHome(t)
	stub := newCmdHubStub()
	server, ca := linkHubStub(t, stub)

	out, err := linkCapture(t, func() error {
		return runLink([]string{"pair", server.URL, "--ca", ca, "--wait", "0"})
	})
	if err != nil {
		t.Fatalf("pair: %v\n%s", err, out)
	}
	if !strings.Contains(out, "still pending") || !strings.Contains(out, "pairing code: ABCD2345") {
		t.Fatalf("pending output:\n%s", out)
	}

	out, err = linkCapture(t, func() error { return runLink([]string{"status"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hub: " + server.URL + " (pending)", "pairing code: ABCD2345", "linked folders: 0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status does not contain %q:\n%s", want, out)
		}
	}
}

func TestRunLinkPairRefusesUntrustedHub(t *testing.T) {
	linkMachineHome(t)
	stub := newCmdHubStub()
	server, _ := linkHubStub(t, stub)
	out, err := linkCapture(t, func() error {
		return runLink([]string{"pair", server.URL, "--wait", "0"})
	})
	if err == nil {
		t.Fatalf("pairing without the hub certificate must fail: %q", out)
	}
	if _, has, err := captaincode.LinkHubState(); err != nil || has {
		t.Fatalf("an unverified hub pinned itself: has=%v err=%v", has, err)
	}
}

func TestRunLinkPairHubErrorMessage(t *testing.T) {
	linkMachineHome(t)
	stub := newCmdHubStub()
	stub.errStatus = 403
	server, ca := linkHubStub(t, stub)
	if _, err := linkCapture(t, func() error {
		return runLink([]string{"pair", server.URL, "--ca", ca, "--wait", "0"})
	}); err == nil || !strings.Contains(err.Error(), "refused this machine") {
		t.Fatalf("pair error %v", err)
	}
}
