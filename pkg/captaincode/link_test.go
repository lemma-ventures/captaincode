package captaincode

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// canonicalTemp resolves the paths a macOS temp directory really lives under:
// /var is a symlink to /private/var, and LinkCanonicalRoot resolves symlinks.
func canonicalTemp(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// linkPresence writes the brain's presence store with one machine identity
// under a temporary home, the way a configured brain reports it.
func linkPresence(t *testing.T, machine string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	conf := filepath.Join(dir, ".config", "captain")
	if err := os.MkdirAll(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"v":1,"machine":"` + machine + `","window_seconds":900}`
	if err := os.WriteFile(filepath.Join(conf, "presence.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func linkConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestLinkKeyLifecycle(t *testing.T) {
	linkConfig(t)
	t.Setenv("HOME", t.TempDir())
	if _, err := LoadLinkKey(); !os.IsNotExist(err) {
		t.Fatalf("fresh machine: LoadLinkKey err = %v, want not-exist", err)
	}
	pub, err := EnsureLinkKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key length %d", len(pub))
	}
	info, err := os.Stat(LinkKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %#o, want 0600", info.Mode().Perm())
	}
	again, err := EnsureLinkKey()
	if err != nil {
		t.Fatal(err)
	}
	if !again.Equal(pub) {
		t.Fatal("EnsureLinkKey minted a second key")
	}
	if _, err := LinkMachineID(); err == nil {
		t.Fatal("a machine without presence reports a link identity")
	}
	linkPresence(t, "machine-link-test")
	id, err := LinkMachineID()
	if err != nil {
		t.Fatal(err)
	}
	if !linkIdentifier.MatchString(id) || id != "machine-link-test" {
		t.Fatalf("machine id %q fails the wire identifiers", id)
	}
	if LinkFingerprint(pub) != LinkFingerprint(again) || LinkFingerprint(pub)[:7] != "sha256:" {
		t.Fatalf("fingerprint %q", LinkFingerprint(pub))
	}
}

func TestLinkKeyRefusesUnsafeFiles(t *testing.T) {
	linkConfig(t)
	if _, err := EnsureLinkKey(); err != nil {
		t.Fatal(err)
	}
	path := LinkKeyPath()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLinkKey(); err == nil {
		t.Fatal("a group-readable key loaded")
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(path+".copy", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".copy", path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLinkKey(); err == nil {
		t.Fatal("a symlinked key loaded")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, ed25519.PrivateKeySize-1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLinkKey(); err == nil {
		t.Fatal("a truncated key loaded")
	}
}

func TestLinkFolderOptIn(t *testing.T) {
	linkConfig(t)
	folder := t.TempDir()
	other := t.TempDir()

	added, err := LinkAdd(folder, LinkModeActive)
	if err != nil {
		t.Fatal(err)
	}
	if want := canonicalTemp(t, folder); added.Root != want {
		t.Fatalf("stored root %q, want %q", added.Root, want)
	}
	if mode, ok := LinkMode(folder); !ok || mode != LinkModeActive {
		t.Fatalf("LinkMode = %q, %v; want active, true", mode, ok)
	}
	if _, ok := LinkMode(other); ok {
		t.Fatal("an unlisted folder reports linked")
	}

	if _, err := LinkAdd(folder, LinkModeQueue); err != nil {
		t.Fatal(err)
	}
	folders, err := LinkFolders()
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Mode != LinkModeQueue {
		t.Fatalf("re-adding a folder must set its mode, got %+v", folders)
	}
	if mode, _ := LinkMode(folder); mode != LinkModeQueue {
		t.Fatalf("mode = %q, want queue", mode)
	}

	if _, err := LinkAdd(other, "everything"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	if _, err := LinkAdd(filepath.Join(folder, "missing"), LinkModeActive); err == nil {
		t.Fatal("a missing folder was linked")
	}
	if _, err := LinkAdd(filepath.Join(folder, "a.txt"), LinkModeActive); err == nil {
		t.Fatal("a non-directory was linked")
	}
	if _, err := LinkAdd("", LinkModeActive); err == nil {
		t.Fatal("an empty folder was linked")
	}

	info, err := os.Stat(LinkStatePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode %#o, want 0600", info.Mode().Perm())
	}

	stopped, err := LinkOff(folder)
	if err != nil {
		t.Fatal(err)
	}
	if stopped != 1 {
		t.Fatalf("stopped %d, want 1", stopped)
	}
	if _, ok := LinkMode(folder); ok {
		t.Fatal("the folder still links after off")
	}
	stopped, err = LinkOff("")
	if err != nil || stopped != 0 {
		t.Fatalf("off on an empty registry: %d, %v", stopped, err)
	}
}

func TestLinkProjectMapping(t *testing.T) {
	linkConfig(t)
	folder := t.TempDir()
	if _, err := LinkAdd(folder, LinkModeActive); err != nil {
		t.Fatal(err)
	}
	mapped, err := LinkMap(folder, "owner-project")
	if err != nil {
		t.Fatal(err)
	}
	if mapped.ProjectID != "owner-project" {
		t.Fatalf("mapped folder: %+v", mapped)
	}
	mapped, err = LinkAdd(folder, LinkModeQueue)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.ProjectID != "owner-project" || mapped.Mode != LinkModeQueue {
		t.Fatalf("re-adding changed the mapping: %+v", mapped)
	}
	for _, id := range []string{"", "bad/id", "../project", "has space", strings.Repeat("x", 129)} {
		if _, err := LinkMap(folder, id); err == nil {
			t.Fatalf("project id %q was accepted", id)
		}
	}
	if _, err := LinkMap(t.TempDir(), "owner-project"); err == nil {
		t.Fatal("an unlinked folder accepted a project id")
	}
	cleared, err := LinkUnmap(folder)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ProjectID != "" {
		t.Fatalf("cleared folder: %+v", cleared)
	}
}

func TestLinkOffClosesEveryFolder(t *testing.T) {
	linkConfig(t)
	for _, dir := range []string{t.TempDir(), t.TempDir(), t.TempDir()} {
		if _, err := LinkAdd(dir, LinkModeActive); err != nil {
			t.Fatal(err)
		}
	}
	stopped, err := LinkOff("")
	if err != nil {
		t.Fatal(err)
	}
	if stopped != 3 {
		t.Fatalf("stopped %d, want 3", stopped)
	}
	folders, err := LinkFolders()
	if err != nil || len(folders) != 0 {
		t.Fatalf("folders after off: %+v, %v", folders, err)
	}
}

func TestLinkCanonicalRootResolvesSymlinks(t *testing.T) {
	linkConfig(t)
	real := t.TempDir()
	if err := os.Mkdir(filepath.Join(real, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Join(real, "repo"), link); err != nil {
		t.Fatal(err)
	}
	root, err := LinkCanonicalRoot(link)
	if err != nil {
		t.Fatal(err)
	}
	want := canonicalTemp(t, filepath.Join(real, "repo"))
	if root != want {
		t.Fatalf("canonical root %q, want %q", root, want)
	}
	if _, err := LinkCanonicalRoot(filepath.Join(real, "repo", "nope")); err == nil {
		t.Fatal("a missing folder resolved")
	}
}

func TestLinkRegistryRefusesUnsafeState(t *testing.T) {
	linkConfig(t)
	if _, err := LinkAdd(t.TempDir(), LinkModeActive); err != nil {
		t.Fatal(err)
	}
	path := LinkStatePath()

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkFolders(); err == nil {
		t.Fatal("a group-readable registry loaded")
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"folders":[],"extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkFolders(); err == nil {
		t.Fatal("a registry with an unknown field loaded")
	}

	if err := os.WriteFile(path, []byte(`{"version":9,"folders":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkFolders(); err == nil {
		t.Fatal("an unknown version loaded")
	}

	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkFolders(); err == nil {
		t.Fatal("a corrupt registry loaded")
	}
}

func linkTestKey(t testing.TB, fill byte) ed25519.PrivateKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i) + fill
	}
	return ed25519.NewKeyFromSeed(seed)
}

const testMachineID = "machine-one"

func TestLinkEnvelopeRoundTrip(t *testing.T) {
	key := linkTestKey(t, 1)
	pub := key.Public().(ed25519.PublicKey)
	machine := testMachineID
	now := time.Now()

	for _, direction := range []string{"brain_to_hub", "hub_to_brain"} {
		e, err := LinkSign(key, machine, direction, "presence", map[string]string{"state": "brain only"}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := LinkVerify(pub, e, direction, now); err != nil {
			t.Fatalf("%s: %v", direction, err)
		}
		var payload map[string]string
		if err := LinkDecodePayload(e, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["state"] != "brain only" {
			t.Fatalf("payload %+v", payload)
		}
	}
}

func TestLinkEnvelopeRefusals(t *testing.T) {
	key := linkTestKey(t, 1)
	base := key.Public().(ed25519.PublicKey)
	now := time.Now()
	valid, err := LinkSign(key, testMachineID, "brain_to_hub", "presence", map[string]int{"n": 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	other := linkTestKey(t, 2)
	otherPub := other.Public().(ed25519.PublicKey)

	cases := []struct {
		name string
		key  ed25519.PublicKey
		env  func(LinkEnvelope) LinkEnvelope
		want time.Time
	}{
		{"wrong direction verified", base, func(e LinkEnvelope) LinkEnvelope {
			e.Direction = "hub_to_brain"
			e.Signature = valid.Signature
			return e
		}, now},
		{"wrong key", otherPub, func(e LinkEnvelope) LinkEnvelope { return e }, now},
		{"tampered payload", base, func(e LinkEnvelope) LinkEnvelope {
			e.Payload = base64.RawURLEncoding.EncodeToString([]byte(`{"n":2}`))
			return e
		}, now},
		{"tampered type", base, func(e LinkEnvelope) LinkEnvelope { e.Type = "presence2"; return e }, now},
		{"unknown schema", base, func(e LinkEnvelope) LinkEnvelope { e.V = 2; return e }, now},
		{"expired", base, func(e LinkEnvelope) LinkEnvelope { return e }, now.Add(time.Minute)},
		{"clock ahead", base, func(e LinkEnvelope) LinkEnvelope { return e }, now.Add(-time.Hour)},
		{"empty signature", base, func(e LinkEnvelope) LinkEnvelope { e.Signature = ""; return e }, now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env(valid)
			if err := LinkVerify(tc.key, env, "brain_to_hub", tc.want); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestLinkEnvelopeRejectsBoundedBodies(t *testing.T) {
	key := linkTestKey(t, 1)
	machine := testMachineID
	if _, err := LinkSign(key, machine, "brain_to_hub", "presence", strings.Repeat("x", linkMaxBody), time.Now()); err == nil {
		t.Fatal("an oversized payload signed")
	}
	e, err := LinkSign(key, machine, "brain_to_hub", "turn.summary", map[string]string{"done": "ok"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Payload = strings.Repeat("A", linkMaxBody+1)
	if err := LinkVerify(key.Public().(ed25519.PublicKey), e, "brain_to_hub", time.Now()); err == nil {
		t.Fatal("an oversized body verified")
	}
}

func TestLinkDecodePayloadIsStrict(t *testing.T) {
	key := linkTestKey(t, 1)
	e, err := LinkSign(key, testMachineID, "brain_to_hub", "presence", map[string]int{"n": 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var known struct {
		N int `json:"n"`
	}
	if err := LinkDecodePayload(e, &known); err != nil || known.N != 1 {
		t.Fatalf("decode = %v, %+v", err, known)
	}
	e.Payload = base64.RawURLEncoding.EncodeToString([]byte(`{"n":1,"sneaky":true}`))
	var strict struct {
		N int `json:"n"`
	}
	if err := LinkDecodePayload(e, &strict); err == nil {
		t.Fatal("an unknown field decoded")
	}
	e.Payload = base64.RawURLEncoding.EncodeToString([]byte(`{"n":1} {"n":2}`))
	if err := LinkDecodePayload(e, &strict); err == nil {
		t.Fatal("trailing JSON decoded")
	}
}

// TestLinkWireCompatibilityVector pins the eight-line frame both sides sign.
// The hub signed this envelope with the seed below; the client must reproduce
// the signature bytes and accept the envelope as the hub sent it. Test material
// only: a throwaway key.
func TestLinkWireCompatibilityVector(t *testing.T) {
	if linkProtocolDomain != "captain.link.v1" {
		t.Fatalf("signing domain %q", linkProtocolDomain)
	}
	seed, err := base64.RawURLEncoding.DecodeString("AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA")
	if err != nil {
		t.Fatal(err)
	}
	if len(seed) != ed25519.SeedSize {
		t.Fatalf("seed length %d", len(seed))
	}
	key := ed25519.NewKeyFromSeed(seed)
	pub := key.Public().(ed25519.PublicKey)
	now := time.Unix(1800000000, 0)

	mine, err := linkSign(key, "machine-one", "brain_to_hub", "link.open", struct{}{}, now, "AAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	const wantSignature = "jbbmENIJKFvbUX6KMtfkdrcO55234STXIQoY96fI75Fd3XlVHOzdKaIQxmXDi2sfifey3gmACr_eHJ1YA-O3CQ"
	if mine.Signature != wantSignature {
		t.Fatalf("signature %s\nwant %s", mine.Signature, wantSignature)
	}

	var fromHub LinkEnvelope
	raw := `{"v":1,"direction":"brain_to_hub","machine_id":"machine-one","id":"AAAAAAAAAAAAAAAAAAAAAA","type":"link.open","issued_at":1800000000,"expires_at":1800000030,"payload":"e30","signature":"` + wantSignature + `"}`
	if err := json.Unmarshal([]byte(raw), &fromHub); err != nil {
		t.Fatal(err)
	}
	if err := LinkVerify(pub, fromHub, "brain_to_hub", now); err != nil {
		t.Fatalf("hub envelope rejected: %v", err)
	}
	if base64.RawURLEncoding.EncodeToString(pub) != "ebVWLo_mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ" {
		t.Fatalf("public key %s", base64.RawURLEncoding.EncodeToString(pub))
	}
}

func TestLinkDecodeEnvelopeIsStrict(t *testing.T) {
	key := linkTestKey(t, 1)
	e, err := LinkSign(key, testMachineID, "brain_to_hub", "link.open", struct{}{}, time.Unix(1800000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got LinkEnvelope
	if err := LinkDecodeEnvelope(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := LinkVerify(key.Public().(ed25519.PublicKey), got, "brain_to_hub", time.Unix(1800000000, 0)); err != nil {
		t.Fatal(err)
	}
	unknown := append([]byte(nil), raw[:len(raw)-1]...)
	unknown = append(unknown, []byte(`,"extra":1}`)...)
	var sneaky LinkEnvelope
	if err := LinkDecodeEnvelope(unknown, &sneaky); err == nil {
		t.Fatal("an unknown envelope field decoded")
	}
	trailing := append(append([]byte(nil), raw[:len(raw)-1]...), '{', '}')
	if err := LinkDecodeEnvelope(trailing, &sneaky); err == nil {
		t.Fatal("trailing JSON decoded")
	}
	var partial LinkEnvelope
	if err := LinkDecodeEnvelope([]byte(`{"v":1,"direction":"brain_to_hub"}`), &partial); err != nil {
		t.Fatalf("a well-formed but incomplete envelope: %v", err)
	}
	if err := LinkVerify(key.Public().(ed25519.PublicKey), partial, "brain_to_hub", time.Unix(1800000000, 0)); err == nil {
		t.Fatal("an incomplete envelope verified")
	}
	if err := LinkDecodeEnvelope(make([]byte, linkMaxBody+1), &sneaky); err == nil {
		t.Fatal("an oversized envelope decoded")
	}
}

func TestLinkStateFormat(t *testing.T) {
	linkConfig(t)
	folder := t.TempDir()
	if _, err := LinkAdd(folder, LinkModeActive); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(LinkStatePath())
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != linkStateVersion {
		t.Fatalf("version %d", state.Version)
	}
}

func BenchmarkLinkMode(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("XDG_CONFIG_HOME", dir)
	folder := b.TempDir()
	if _, err := LinkAdd(folder, LinkModeActive); err != nil {
		b.Fatal(err)
	}
	if _, ok := LinkMode(folder); !ok {
		b.Fatal("folder not linked")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := LinkMode(folder); !ok {
			b.Fatal("lookup failed")
		}
	}
}

func BenchmarkLinkMap(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	folder := b.TempDir()
	if _, err := LinkAdd(folder, LinkModeActive); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LinkMap(folder, "owner-project"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLinkSign(b *testing.B) {
	key := linkTestKey(b, 1)
	machine := testMachineID
	now := time.Now()
	payload := map[string]string{"state": "brain only", "folder": "repo", "version": "0.0.0"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LinkSign(key, machine, "brain_to_hub", "presence", payload, now); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLinkVerify(b *testing.B) {
	key := linkTestKey(b, 1)
	pub := key.Public().(ed25519.PublicKey)
	machine := testMachineID
	now := time.Now()
	e, err := LinkSign(key, machine, "brain_to_hub", "presence", map[string]string{"state": "brain only"}, now)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := LinkVerify(pub, e, "brain_to_hub", now); err != nil {
			b.Fatal(err)
		}
	}
}
