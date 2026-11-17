package captaincode

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubHub answers the pair and pair.status paths the way the reviewed hub
// does: it verifies the brain's signature with the key in the request, signs
// its replies in the hub_to_brain direction, and can fail a request with the
// signed link.error the protocol defines.
type stubHub struct {
	mu            sync.Mutex
	key           ed25519.PrivateKey
	approved      bool
	approveOnCall int
	errStatus     int
	pairCount     int
	statusCount   int
	lastMachine   string
	lastLabel     string
	lastPublicKey string
	brainKey      []byte
	tamper        func(LinkEnvelope) LinkEnvelope
}

func newStubHub() *stubHub {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return &stubHub{key: key, approveOnCall: 0}
}

func (h *stubHub) sign(machine, kind string, payload any) LinkEnvelope {
	e, err := LinkSign(h.key, machine, linkHubDirection, kind, payload, time.Now())
	if err != nil {
		panic(err)
	}
	if h.tamper != nil {
		e = h.tamper(e)
	}
	return e
}

func (h *stubHub) write(w http.ResponseWriter, status int, e LinkEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

func (h *stubHub) hubError(w http.ResponseWriter, machine string, status int) {
	h.write(w, status, h.sign(machine, "link.error", map[string]int{"status": status}))
}

func (h *stubHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil || len(raw) > linkMaxBody {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var e LinkEnvelope
	if err := LinkDecodeEnvelope(raw, &e); err != nil {
		http.Error(w, "bad envelope", http.StatusBadRequest)
		return
	}
	if e.Direction != linkBrainDirection {
		http.Error(w, "bad direction", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastMachine = e.MachineID
	switch r.URL.Path {
	case "/api/v1/link/pair":
		h.pairCount++
		if h.errStatus != 0 {
			h.hubError(w, e.MachineID, h.errStatus)
			return
		}
		var p struct {
			MachineID string `json:"machine_id"`
			PublicKey string `json:"public_key"`
			Label     string `json:"label"`
		}
		if err := LinkDecodePayload(e, &p); err != nil || p.MachineID != e.MachineID || p.Label == "" {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		pub, err := base64.RawURLEncoding.DecodeString(p.PublicKey)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			http.Error(w, "bad key", http.StatusBadRequest)
			return
		}
		if err := LinkVerify(ed25519.PublicKey(pub), e, linkBrainDirection, time.Now()); err != nil {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		h.lastLabel = p.Label
		h.lastPublicKey = p.PublicKey
		h.brainKey = pub
		hubKey := base64.RawURLEncoding.EncodeToString(h.key.Public().(ed25519.PublicKey))
		h.write(w, http.StatusOK, h.sign(e.MachineID, "pair.pending", map[string]any{
			"code":           "ABCD2345",
			"pair_id":        e.ID,
			"expires_at":     time.Now().Add(5 * time.Minute),
			"hub_public_key": hubKey,
		}))
	case "/api/v1/link/pair/status":
		h.statusCount++
		if h.errStatus != 0 {
			h.hubError(w, e.MachineID, h.errStatus)
			return
		}
		var p struct {
			PairID string `json:"pair_id"`
		}
		if err := LinkDecodePayload(e, &p); err != nil || p.PairID == "" {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		if err := LinkVerify(ed25519.PublicKey(h.brainKey), e, linkBrainDirection, time.Now()); err != nil {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		state := LinkPairPending
		if h.approved || (h.approveOnCall > 0 && h.statusCount >= h.approveOnCall) {
			state = LinkPairApproved
		}
		h.write(w, http.StatusOK, h.sign(e.MachineID, "pair.status", map[string]string{"status": state}))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(io.LimitReader(r.Body, linkMaxBody+1))
}

// snapshot reads the observations the fake hub recorded, under its lock.
func (h *stubHub) snapshot() (machine, label, publicKey string, pairs, statuses int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastMachine, h.lastLabel, h.lastPublicKey, h.pairCount, h.statusCount
}

func linkStub(t *testing.T, hub *stubHub) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewTLSServer(hub)
	t.Cleanup(server.Close)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	caPath := filepath.Join(t.TempDir(), "hub-ca.pem")
	if err := os.WriteFile(caPath, cert, 0o644); err != nil {
		t.Fatal(err)
	}
	return server, caPath
}

// linkPairFixture gives the tests a machine identity, an isolated link key and
// registry, and the pending state of one pairing.
func linkPairFixture(t *testing.T, hub *stubHub) (*httptest.Server, string) {
	t.Helper()
	linkConfig(t)
	linkPresence(t, "machine-pair-test")
	return linkStub(t, hub)
}

func linkHubPair(t *testing.T, url, ca string) LinkHub {
	t.Helper()
	hub, reused, err := LinkPair(context.Background(), url, ca)
	if err != nil {
		t.Fatalf("LinkPair: %v", err)
	}
	if reused {
		t.Fatal("the first pairing must be requested, not reused")
	}
	return hub
}

func TestLinkPairPinsHubAndReachesApproval(t *testing.T) {
	stub := newStubHub()
	server, ca := linkPairFixture(t, stub)

	hub := linkHubPair(t, server.URL, ca)
	if hub.Code != "ABCD2345" || hub.PairID == "" || hub.URL != server.URL {
		t.Fatalf("pairing %+v", hub)
	}
	if hub.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("window closes in the past: %d", hub.ExpiresAt)
	}
	machine, label, publicKey, _, _ := stub.snapshot()
	if machine != "machine-pair-test" || label == "" {
		t.Fatalf("the hub saw machine %q label %q", machine, label)
	}
	if publicKey == "" {
		t.Fatal("the hub saw no public key")
	}
	if !linkLabelPatternForTest.MatchString(label) {
		t.Fatalf("label %q fails the hub's label pattern", label)
	}
	info, err := os.Stat(LinkStatePath())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode %v, want 0600", info.Mode().Perm())
	}
	body, err := os.ReadFile(LinkStatePath())
	if err != nil || !json.Valid(body) {
		t.Fatalf("registry not valid JSON after pairing: %v", err)
	}
	page, err := LinkApprovalURL(hub.URL)
	if err != nil || page != server.URL+"/captains/pair/" {
		t.Fatalf("approval url %q, %v", page, err)
	}
	fingerprint, err := LinkHubFingerprint(hub.PublicKey)
	if err != nil || fingerprint == "" {
		t.Fatalf("hub fingerprint %q, %v", fingerprint, err)
	}

	if status, err := LinkPairStatus(context.Background(), ca); err != nil || status != LinkPairPending {
		t.Fatalf("status %q, %v; want pending", status, err)
	}
	if stub.statusCount != 1 {
		t.Fatalf("status calls %d, want 1", stub.statusCount)
	}

	again, reused, err := LinkPair(context.Background(), server.URL, ca)
	if err != nil || !reused {
		t.Fatalf("second pairing: reused=%v err=%v, want the live pairing kept", reused, err)
	}
	_, _, _, pairs, _ := stub.snapshot()
	if again.Code != hub.Code || pairs != 1 {
		t.Fatalf("reuse changed the pairing: %+v, pair calls %d", again, pairs)
	}

	stub.mu.Lock()
	stub.approved = true
	stub.mu.Unlock()
	if status, err := LinkPairStatus(context.Background(), ca); err != nil || status != LinkPairApproved {
		t.Fatalf("status %q, %v; want approved", status, err)
	}
	stored, has, err := LinkHubState()
	if err != nil || !has || stored.ApprovedAt == 0 {
		t.Fatalf("approval not recorded: %+v has=%v err=%v", stored, has, err)
	}
	_, _, _, _, before := stub.snapshot()
	if status, err := LinkPairStatus(context.Background(), ca); err != nil || status != LinkPairApproved {
		t.Fatalf("status %q, %v; want approved", status, err)
	}
	_, _, _, _, after := stub.snapshot()
	if after != before {
		t.Fatal("an approved pairing must answer locally, without the network")
	}
	if _, _, err := LinkPair(context.Background(), server.URL, ca); err == nil {
		t.Fatal("a second pairing on an approved machine must be refused")
	}
}

func TestLinkPairRefusesUntrustedTLS(t *testing.T) {
	stub := newStubHub()
	server, _ := linkPairFixture(t, stub)
	if _, _, err := LinkPair(context.Background(), server.URL, ""); err == nil {
		t.Fatal("pairing without the hub certificate must fail")
	}
	if _, has, err := LinkHubState(); err != nil || has {
		t.Fatalf("an unverified hub must leave no state: has=%v err=%v", has, err)
	}
	if _, _, _, pairs, _ := stub.snapshot(); pairs != 0 {
		t.Fatalf("the hub saw %d pair requests, want 0", pairs)
	}
}

func TestLinkHubURLRules(t *testing.T) {
	linkConfig(t)
	for _, bad := range []string{
		"",
		"   ",
		"http://hub.example",
		"ftp://hub.example",
		"hub.example",
		"https://user:pass@hub.example",
		"https://hub.example?next=1",
		"https://hub.example#top",
		"https:///nohost",
	} {
		if _, _, err := LinkPair(context.Background(), bad, ""); err == nil {
			t.Fatalf("hub url %q accepted", bad)
		}
	}
	page, err := LinkApprovalURL("https://hub.example")
	if err != nil || page != "https://hub.example/captains/pair/" {
		t.Fatalf("approval url %q, %v", page, err)
	}
}

func TestLinkPairRefusesChangedHubKey(t *testing.T) {
	stub := newStubHub()
	server, ca := linkPairFixture(t, stub)
	hub := linkHubPair(t, server.URL, ca)

	if _, err := linkUpdate(true, func(s *linkState) error {
		if s.Hub == nil {
			t.Fatal("pairing missing")
		}
		s.Hub.ExpiresAt = time.Now().Unix() - 10
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, other, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.key = other
	stub.mu.Unlock()

	if _, _, err := LinkPair(context.Background(), server.URL, ca); err == nil {
		t.Fatal("a hub signing with a new key must be refused")
	}
	stored, has, err := LinkHubState()
	if err != nil || !has {
		t.Fatal(err)
	}
	if stored.PublicKey != hub.PublicKey {
		t.Fatal("the pinned key changed without unpairing")
	}
}

func TestLinkPairRejectsTamperedReply(t *testing.T) {
	stub := newStubHub()
	server, ca := linkPairFixture(t, stub)
	stub.mu.Lock()
	stub.tamper = func(e LinkEnvelope) LinkEnvelope {
		body := base64.RawURLEncoding.EncodeToString([]byte(`{"code":"ABCD2345","pair_id":"AAAAAAAAAAAAAAAAAAAAAA","expires_at":"2030-01-01T00:00:00Z","hub_public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`))
		e.Payload = body
		return e
	}
	stub.mu.Unlock()

	if _, _, err := LinkPair(context.Background(), server.URL, ca); err == nil {
		t.Fatal("a reply whose payload no longer matches its signature must be refused")
	}
	if _, has, err := LinkHubState(); err != nil || has {
		t.Fatalf("a tampered reply must leave no state: has=%v err=%v", has, err)
	}
}

func TestLinkPairHubErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "rejected the signature"},
		{403, "refused this machine"},
		{409, "already holds a live pairing"},
		{503, "cannot sign"},
	} {
		stub := newStubHub()
		stub.errStatus = tc.status
		server, ca := linkPairFixture(t, stub)
		_, _, err := LinkPair(context.Background(), server.URL, ca)
		if err == nil || !containsFold(err.Error(), tc.want) {
			t.Fatalf("status %d gave %v, want a message containing %q", tc.status, err, tc.want)
		}
		if _, has, err := LinkHubState(); err != nil || has {
			t.Fatalf("a hub error must leave no state: has=%v err=%v", has, err)
		}
	}
}

func TestLinkPairRequiresMachineIdentity(t *testing.T) {
	linkConfig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "captain"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LinkPair(context.Background(), "https://hub.example", ""); err == nil || !containsFold(err.Error(), "machine identity") {
		t.Fatalf("pairing without a machine identity gave %v", err)
	}
}

func TestLinkPairRefusesAnotherHub(t *testing.T) {
	stub := newStubHub()
	server, ca := linkPairFixture(t, stub)
	linkHubPair(t, server.URL, ca)
	_, _, err := LinkPair(context.Background(), "https://other-hub.example", ca)
	if err == nil || !containsFold(err.Error(), "unpair") {
		t.Fatalf("pairing with a second hub must ask for unpair, got %v", err)
	}
}

func TestLinkPairStatusErrors(t *testing.T) {
	stub := newStubHub()
	server, ca := linkPairFixture(t, stub)
	linkHubPair(t, server.URL, ca)

	stub.mu.Lock()
	stub.errStatus = 403
	stub.mu.Unlock()
	if _, err := LinkPairStatus(context.Background(), ca); err == nil || !containsFold(err.Error(), "refused this machine") {
		t.Fatalf("status error %v", err)
	}

	if _, err := LinkPairStatus(context.Background(), ""); err == nil {
		t.Fatal("reading the status without the hub certificate must fail")
	}

	if _, err := linkUpdate(true, func(s *linkState) error {
		if s.Hub == nil {
			t.Fatal("pairing missing")
		}
		s.Hub.ExpiresAt = time.Now().Unix() - 10
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, calls := stub.snapshot()
	if status, err := LinkPairStatus(context.Background(), ca); err != nil || status != LinkPairExpired {
		t.Fatalf("status %q, %v; want expired", status, err)
	}
	_, _, _, _, after := stub.snapshot()
	if after != calls {
		t.Fatal("an expired window must answer locally, without the network")
	}
}

func TestLinkPairNoHubIsRefused(t *testing.T) {
	linkConfig(t)
	if _, err := LinkPairStatus(context.Background(), ""); err == nil {
		t.Fatal("status without a pairing must be refused")
	}
}

func TestLinkUnpairKeepsFolders(t *testing.T) {
	stub := newStubHub()
	server, ca := linkPairFixture(t, stub)
	folder := t.TempDir()
	if _, err := LinkAdd(folder, LinkModeActive); err != nil {
		t.Fatal(err)
	}
	linkHubPair(t, server.URL, ca)

	cleared, err := LinkUnpair()
	if err != nil || !cleared {
		t.Fatalf("unpair: cleared=%v err=%v", cleared, err)
	}
	if _, has, err := LinkHubState(); err != nil || has {
		t.Fatalf("hub still stored: has=%v err=%v", has, err)
	}
	folders, err := LinkFolders()
	if err != nil || len(folders) != 1 {
		t.Fatalf("folders after unpair: %+v, %v", folders, err)
	}
	if cleared, err := LinkUnpair(); err != nil || cleared {
		t.Fatalf("a second unpair: cleared=%v err=%v", cleared, err)
	}
}

func TestLinkPairWaitUntilApproval(t *testing.T) {
	stub := newStubHub()
	stub.approveOnCall = 2
	server, ca := linkPairFixture(t, stub)
	linkHubPair(t, server.URL, ca)

	var seen []string
	status, err := LinkPairWait(context.Background(), ca, 3*time.Second, func(s string) { seen = append(seen, s) })
	if err != nil || status != LinkPairApproved {
		t.Fatalf("wait gave status %q, %v; want approved", status, err)
	}
	if len(seen) < 2 || seen[0] != LinkPairPending {
		t.Fatalf("observed states %v, want a pending state first", seen)
	}
}

func TestLinkPairWaitStopsAtDeadline(t *testing.T) {
	stub := newStubHub()
	server, ca := linkPairFixture(t, stub)
	linkHubPair(t, server.URL, ca)

	status, err := LinkPairWait(context.Background(), ca, 50*time.Millisecond, nil)
	if err != nil || status != LinkPairPending {
		t.Fatalf("wait gave status %q, %v; want pending", status, err)
	}
	status, after := func() (string, bool) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return "", stub.approved
	}()
	_ = status
	if after {
		t.Fatal("stub should not approve")
	}
}

func TestLinkPairWaitStopsWithContext(t *testing.T) {
	stub := newStubHub()
	server, ca := linkPairFixture(t, stub)
	linkHubPair(t, server.URL, ca)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	status, err := LinkPairWait(ctx, ca, 5*time.Second, nil)
	if status != LinkPairPending {
		t.Fatalf("status %q, want pending while the caller stops", status)
	}
	if err == nil {
		t.Fatal("a cancelled wait must report the cancellation")
	}
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

var linkLabelPatternForTest = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,79}$`)

// BenchmarkLinkPairStatusApprovedLocal is the cost of the answer a paired
// brain gives without the network: read the guarded registry, find the
// approval, return.
func BenchmarkLinkPairStatusApprovedLocal(b *testing.B) {
	restore := setupBenchHub(b)
	defer restore()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		status, err := LinkPairStatus(context.Background(), "")
		if err != nil || status != LinkPairApproved {
			b.Fatalf("status %q, %v", status, err)
		}
	}
}

// BenchmarkLinkHubURLParse measures the hub validation the pair command runs
// before any request leaves the machine.
func BenchmarkLinkHubURLParse(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := linkHubBase("https://hub.example.com"); err != nil {
			b.Fatal(err)
		}
	}
}

func setupBenchHub(b *testing.B) func() {
	b.Helper()
	home := b.TempDir()
	xdg := b.TempDir()
	oldHome, hadHome := os.LookupEnv("HOME")
	oldXDG, hadXDG := os.LookupEnv("XDG_CONFIG_HOME")
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", xdg)
	conf := filepath.Join(home, ".config", "captain")
	if err := os.MkdirAll(conf, 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "presence.json"), []byte(`{"v":1,"machine":"machine-bench","window_seconds":900}`), 0o600); err != nil {
		b.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatal(err)
	}
	pub := base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	hub := LinkHub{
		URL:         "https://hub.example.com",
		PublicKey:   pub,
		PairID:      "AAAAAAAAAAAAAAAAAAAAAA",
		Code:        "ABCD2345",
		RequestedAt: time.Now().Unix(),
		ExpiresAt:   time.Now().Add(5 * time.Minute).Unix(),
		ApprovedAt:  time.Now().Unix(),
	}
	if _, err := linkUpdate(true, func(s *linkState) error {
		stored := hub
		s.Hub = &stored
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	return func() {
		if hadHome {
			os.Setenv("HOME", oldHome)
		} else {
			os.Unsetenv("HOME")
		}
		if hadXDG {
			os.Setenv("XDG_CONFIG_HOME", oldXDG)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	}
}
