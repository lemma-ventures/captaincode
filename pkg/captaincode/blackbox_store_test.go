package captaincode

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type blackboxStoreFixture struct {
	dir   string
	pins  BlackboxTrustPins
	base  time.Time
	valid BlackboxExportOptions
}

func newBlackboxStoreFixture(t testing.TB) blackboxStoreFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	imported := upstreamImportFixture(t)
	base := time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)
	pins := BlackboxTrustPins{
		KeyID:     "key-1",
		PublicKey: publicKey,
		TrustRoot: "root-1",
		Graph:     imported.Graph,
		Network:   "network-1",
	}
	options := BlackboxExportOptions{
		Snapshot:        imported.Snapshot,
		Complete:        imported.CompleteCategories,
		Omitted:         imported.Omitted,
		Revocations:     imported.Revocations,
		UpstreamVersion: "0.0.0",
		TTL:             24 * time.Hour,
		Pins:            pins,
		PrivateKey:      privateKey,
	}
	return blackboxStoreFixture{dir: t.TempDir(), pins: pins, base: base, valid: options}
}

func (f *blackboxStoreFixture) envelope(t testing.TB, generation uint64, now time.Time) BlackboxEnvelope {
	t.Helper()
	options := f.valid
	options.Generation = generation
	options.Sequence = int(generation)
	options.EnvelopeID = fmt.Sprintf("export-%d", generation)
	options.Now = now
	envelope, err := ExportBlackboxEnvelope(options)
	if err != nil {
		t.Fatalf("export generation %d: %v", generation, err)
	}
	return envelope
}

func blackboxStorePath(fixture blackboxStoreFixture) string {
	return filepath.Join(fixture.dir, blackboxStoreFileName)
}

func TestBlackboxStoreRoundTrip(t *testing.T) {
	fixture := newBlackboxStoreFixture(t)
	store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("open empty store: %v", err)
	}
	if store.HasCurrent() || store.Generation() != 0 {
		t.Fatalf("fresh store has current %t generation %d", store.HasCurrent(), store.Generation())
	}
	envelope := fixture.envelope(t, 5, fixture.base)
	if err := store.Accept(envelope, fixture.base); err != nil {
		t.Fatalf("accept: %v", err)
	}
	reopened, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Generation() != 5 || !reopened.HasCurrent() {
		t.Fatalf("reopened generation %d current %t", reopened.Generation(), reopened.HasCurrent())
	}
	if reopened.Snapshot().ID != envelope.Rules.ID {
		t.Fatalf("reopened snapshot %q", reopened.Snapshot().ID)
	}
	if reopened.Digest() != blackboxEnvelopeDigest(envelope) {
		t.Fatalf("reopened digest %q", reopened.Digest())
	}
	if fmt.Sprint(reopened.Revocations()) != fmt.Sprint(envelope.Revocations) {
		t.Fatalf("reopened revocations %v", reopened.Revocations())
	}
	if !reopened.VerifiedAt().Equal(fixture.base) {
		t.Fatalf("verified at %s", reopened.VerifiedAt().UTC().Format(time.RFC3339))
	}
	if reopened.Expired(fixture.base.Add(time.Hour)) {
		t.Fatal("an envelope valid for 24 hours expired early")
	}
	info, err := os.Stat(blackboxStorePath(fixture))
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("store mode is %v, want 0600", info.Mode().Perm())
	}
}

func TestBlackboxStoreRejectsReplayAndOlderGeneration(t *testing.T) {
	fixture := newBlackboxStoreFixture(t)
	store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Accept(fixture.envelope(t, 5, fixture.base), fixture.base); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := store.Accept(fixture.envelope(t, 5, fixture.base), fixture.base); err == nil {
		t.Fatal("the same generation was accepted twice")
	}
	if err := store.Accept(fixture.envelope(t, 4, fixture.base), fixture.base); err == nil {
		t.Fatal("an older generation was accepted")
	}
	reopened, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Generation() != 5 || !reopened.VerifiedAt().Equal(fixture.base) {
		t.Fatalf("generation %d verified at %s after a refused refresh", reopened.Generation(), reopened.VerifiedAt())
	}
}

func TestBlackboxStoreKeepsStateWhenTheWriteFails(t *testing.T) {
	fixture := newBlackboxStoreFixture(t)
	store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Accept(fixture.envelope(t, 5, fixture.base), fixture.base); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := os.Chmod(fixture.dir, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	blocked := fixture.envelope(t, 6, fixture.base.Add(time.Minute))
	err = store.Accept(blocked, fixture.base.Add(time.Minute))
	if chmodErr := os.Chmod(fixture.dir, 0700); chmodErr != nil {
		t.Fatalf("chmod restore: %v", chmodErr)
	}
	if err == nil {
		t.Fatal("an accept wrote through a read-only state directory")
	}
	reopened, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Generation() != 5 {
		t.Fatalf("generation is %d after a failed accept", reopened.Generation())
	}
	if reopened.Digest() != blackboxEnvelopeDigest(fixture.envelope(t, 5, fixture.base)) {
		t.Fatal("the accepted envelope changed after a failed accept")
	}
}

func TestBlackboxStoreFailsClosedOnBrokenState(t *testing.T) {
	fixture := newBlackboxStoreFixture(t)
	store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Accept(fixture.envelope(t, 5, fixture.base), fixture.base); err != nil {
		t.Fatalf("accept: %v", err)
	}
	path := blackboxStorePath(fixture)
	valid := readBlackboxStoreFile(t, path)

	t.Run("truncated file", func(t *testing.T) {
		if err := os.WriteFile(path, valid[:len(valid)/2], 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := OpenBlackboxStore(fixture.dir, fixture.pins); err == nil {
			t.Fatal("a truncated store file was accepted")
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		decoded := map[string]any{}
		if err := json.Unmarshal(valid, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		decoded["surprising_key"] = 1
		encoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := OpenBlackboxStore(fixture.dir, fixture.pins); err == nil {
			t.Fatal("an unknown store field was accepted")
		}
	})
	t.Run("tampered envelope", func(t *testing.T) {
		file, err := decodeBlackboxStoreFile(valid)
		if err != nil {
			t.Fatalf("decode store: %v", err)
		}
		file.Envelope.Rules.Packages[0].Name = "renamed-after-signing"
		encoded, err := json.Marshal(file)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := OpenBlackboxStore(fixture.dir, fixture.pins); err == nil {
			t.Fatal("a tampered envelope was accepted")
		}
	})
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := OpenBlackboxStore(fixture.dir, fixture.pins); err != nil {
		t.Fatalf("the untouched store no longer opens: %v", err)
	}
}

func TestBlackboxStoreRejectsSignedMetadataMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*blackboxStoreFile)
	}{
		{name: "expiry", mutate: func(file *blackboxStoreFile) { file.ExpiresAt = file.ExpiresAt.Add(time.Hour) }},
		{name: "revocations", mutate: func(file *blackboxStoreFile) { file.Revocations = append(file.Revocations, "unsigned-revocation") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBlackboxStoreFixture(t)
			store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if err := store.Accept(fixture.envelope(t, 5, fixture.base), fixture.base); err != nil {
				t.Fatalf("accept: %v", err)
			}
			path := blackboxStorePath(fixture)
			file, err := decodeBlackboxStoreFile(readBlackboxStoreFile(t, path))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			test.mutate(&file)
			encoded, err := json.Marshal(file)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := OpenBlackboxStore(fixture.dir, fixture.pins); err == nil {
				t.Fatal("metadata outside the signed envelope was accepted")
			}
		})
	}
}

func TestBlackboxStoreRollbackRejectsSignedMetadataMismatch(t *testing.T) {
	fixture := newBlackboxStoreFixture(t)
	store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Accept(fixture.envelope(t, 5, fixture.base), fixture.base); err != nil {
		t.Fatalf("accept first: %v", err)
	}
	if err := store.Accept(fixture.envelope(t, 6, fixture.base.Add(time.Minute)), fixture.base.Add(time.Minute)); err != nil {
		t.Fatalf("accept second: %v", err)
	}
	path := filepath.Join(fixture.dir, blackboxStorePreviousName)
	file, err := decodeBlackboxStoreFile(readBlackboxStoreFile(t, path))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	file.ExpiresAt = file.ExpiresAt.Add(time.Hour)
	encoded, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := store.Rollback(); err == nil {
		t.Fatal("rollback accepted metadata outside the signed envelope")
	}
	if store.Generation() != 6 {
		t.Fatalf("failed rollback changed generation to %d", store.Generation())
	}
}

func TestBlackboxStoreIgnoresLeftoverTempFiles(t *testing.T) {
	fixture := newBlackboxStoreFixture(t)
	store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Accept(fixture.envelope(t, 5, fixture.base), fixture.base); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.dir, "store.json.tmp-crashed"), []byte(`{"schema":1`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	reopened, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Generation() != 5 {
		t.Fatalf("generation is %d", reopened.Generation())
	}
}

func TestBlackboxStoreRetainsExpiredSnapshot(t *testing.T) {
	fixture := newBlackboxStoreFixture(t)
	short := fixture.valid
	short.TTL = time.Hour
	short.Generation = 5
	short.Sequence = 5
	short.EnvelopeID = "export-short"
	short.Now = fixture.base
	envelope, err := ExportBlackboxEnvelope(short)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Accept(envelope, fixture.base); err != nil {
		t.Fatalf("accept: %v", err)
	}
	later := fixture.base.Add(3 * time.Hour)
	reopened, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("reopen after expiry: %v", err)
	}
	if !reopened.Expired(later) {
		t.Fatal("the expired snapshot reports fresh")
	}
	if !reopened.VerifiedAt().Equal(fixture.base) {
		t.Fatalf("verified at %s, want the original verification time", reopened.VerifiedAt().UTC().Format(time.RFC3339))
	}
	if !reopened.HasCurrent() || reopened.Generation() != 5 {
		t.Fatal("the retained snapshot was dropped")
	}
	if err := reopened.Accept(fixture.envelope(t, 6, later), later); err != nil {
		t.Fatalf("a newer generation should refresh an expired store: %v", err)
	}
}

func TestBlackboxStoreRollbackRestoresThePreviousEnvelope(t *testing.T) {
	fixture := newBlackboxStoreFixture(t)
	store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	first := fixture.envelope(t, 5, fixture.base)
	second := fixture.envelope(t, 6, fixture.base.Add(time.Minute))
	if err := store.Accept(first, fixture.base); err != nil {
		t.Fatalf("accept first: %v", err)
	}
	if err := store.Accept(second, fixture.base.Add(time.Minute)); err != nil {
		t.Fatalf("accept second: %v", err)
	}
	if err := store.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if store.Generation() != 5 || store.Digest() != blackboxEnvelopeDigest(first) {
		t.Fatalf("after rollback generation is %d digest %q", store.Generation(), store.Digest())
	}
	if err := store.Accept(second, fixture.base.Add(3*time.Minute)); err != nil {
		t.Fatalf("re-accept after rollback: %v", err)
	}
	if store.Generation() != 6 || store.Digest() != blackboxEnvelopeDigest(second) {
		t.Fatalf("re-accepted generation %d digest %q", store.Generation(), store.Digest())
	}
	if err := store.Rollback(); err != nil {
		t.Fatalf("second rollback: %v", err)
	}
	reopened, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Generation() != 5 || reopened.Digest() != blackboxEnvelopeDigest(first) {
		t.Fatalf("reopened generation %d digest %q", reopened.Generation(), reopened.Digest())
	}
}

func readBlackboxStoreFile(t testing.TB, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	return body
}

func BenchmarkBlackboxStoreAcceptAndReopen(b *testing.B) {
	fixture := newBlackboxStoreFixture(b)
	store, err := OpenBlackboxStore(fixture.dir, fixture.pins)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	generation := uint64(5)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		envelope := fixture.envelope(b, generation, fixture.base)
		if err := store.Accept(envelope, fixture.base); err != nil {
			b.Fatalf("accept: %v", err)
		}
		generation++
	}
	b.StopTimer()
	if store.Generation() != generation-1 {
		b.Fatalf("generation %d", store.Generation())
	}
}
