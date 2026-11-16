package captaincode

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

type blackboxExportFixture struct {
	options  BlackboxExportOptions
	pins     BlackboxTrustPins
	now      time.Time
	private  ed25519.PrivateKey
	imported BlackboxUpstreamImport
}

func newBlackboxExportFixture(t testing.TB) blackboxExportFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	imported := upstreamImportFixture(t)
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
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
		EnvelopeID:      "export-1",
		UpstreamVersion: "0.0.0",
		Now:             now,
		TTL:             24 * time.Hour,
		Sequence:        3,
		Generation:      5,
		Pins:            pins,
		PrivateKey:      privateKey,
	}
	return blackboxExportFixture{options: options, pins: pins, now: now, private: privateKey, imported: imported}
}

func (f *blackboxExportFixture) export(t testing.TB) BlackboxEnvelope {
	t.Helper()
	envelope, err := ExportBlackboxEnvelope(f.options)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return envelope
}

func TestExportBlackboxEnvelopeRoundTrip(t *testing.T) {
	fixture := newBlackboxExportFixture(t)
	envelope := fixture.export(t)
	if envelope.Revision != fixture.imported.Snapshot.Revision {
		t.Fatalf("envelope revision %q", envelope.Revision)
	}
	if envelope.Authority.Graph != fixture.imported.Graph {
		t.Fatalf("envelope graph %q", envelope.Authority.Graph)
	}
	if envelope.Manifest.RuleCounts["dependency"] != 2 ||
		envelope.Manifest.RuleCounts["protected_path"] != 0 ||
		envelope.Manifest.RuleCounts["pattern"] != 1 {
		t.Fatalf("manifest counts are %v", envelope.Manifest.RuleCounts)
	}
	options := BlackboxVerifyOptions{Pins: fixture.pins, Now: fixture.now, LastGeneration: 4}
	snapshot, err := VerifyBlackboxEnvelope(envelope, options)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if snapshot.Digest == "" || snapshot.ID != fixture.imported.Snapshot.ID {
		t.Fatalf("accepted snapshot carries id %q digest %q", snapshot.ID, snapshot.Digest)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := DecodeBlackboxEnvelope(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	reverified, err := VerifyBlackboxEnvelope(decoded, options)
	if err != nil {
		t.Fatalf("verify decoded: %v", err)
	}
	if reverified.Digest != snapshot.Digest {
		t.Fatalf("digest moved from %s to %s", snapshot.Digest, reverified.Digest)
	}
	again, err := ExportBlackboxEnvelope(fixture.options)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if blackboxEnvelopeDigest(again) != blackboxEnvelopeDigest(envelope) {
		t.Fatal("the same input exported two different envelope digests")
	}
}

func TestExportBlackboxEnvelopeCarriesTimesAndAuthority(t *testing.T) {
	fixture := newBlackboxExportFixture(t)
	envelope := fixture.export(t)
	if !envelope.GeneratedAt.Equal(fixture.now) {
		t.Fatalf("generated_at is %s", envelope.GeneratedAt.UTC().Format(time.RFC3339))
	}
	if !envelope.ExpiresAt.Equal(fixture.now.Add(24 * time.Hour)) {
		t.Fatalf("expires_at is %s", envelope.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if envelope.Source != BlackboxExportSource || envelope.Authority.Kind != BlackboxAuthorityEd25519 {
		t.Fatalf("source %q kind %q", envelope.Source, envelope.Authority.Kind)
	}
	if envelope.Authority.KeyID != fixture.pins.KeyID || envelope.Engine.EvaluatorMin > blackboxEvaluatorSchema {
		t.Fatalf("key id %q evaluator min %d", envelope.Authority.KeyID, envelope.Engine.EvaluatorMin)
	}
}

func TestExportBlackboxEnvelopeRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*BlackboxExportOptions)
	}{
		{"no generation", func(o *BlackboxExportOptions) { o.Generation = 0 }},
		{"no sequence", func(o *BlackboxExportOptions) { o.Sequence = 0 }},
		{"no ttl", func(o *BlackboxExportOptions) { o.TTL = 0 }},
		{"no envelope id", func(o *BlackboxExportOptions) { o.EnvelopeID = "" }},
		{"no revision", func(o *BlackboxExportOptions) { o.Snapshot.Revision = "" }},
		{"unknown source", func(o *BlackboxExportOptions) { o.Snapshot.Source = "unverified" }},
		{"empty key", func(o *BlackboxExportOptions) { o.PrivateKey = nil }},
		{"revoked rule carried", func(o *BlackboxExportOptions) {
			o.Revocations = []string{o.Snapshot.Packages[0].ID}
		}},
		{"duplicate revocation", func(o *BlackboxExportOptions) { o.Revocations = []string{"x", "x"} }},
		{"empty revocation", func(o *BlackboxExportOptions) { o.Revocations = []string{" "} }},
		{"empty omission", func(o *BlackboxExportOptions) { o.Omitted = []string{" "} }},
		{"duplicate omission", func(o *BlackboxExportOptions) { o.Omitted = []string{"ioc", "ioc"} }},
		{"omitted and complete", func(o *BlackboxExportOptions) {
			o.Complete = append(append([]string{}, o.Complete...), "pattern")
			o.Omitted = append(append([]string{}, o.Omitted...), "pattern")
		}},
		{"complete without rules", func(o *BlackboxExportOptions) {
			o.Snapshot.Packages = nil
			o.Omitted = nil
			o.Complete = []string{"secret_exposure", "dependency"}
		}},
		{"undeclared category", func(o *BlackboxExportOptions) {
			delete(o.Snapshot.Categories, "protected_path")
		}},
		{"rule count above the limit", func(o *BlackboxExportOptions) {
			packages := append([]BlackboxPackageRule{}, o.Snapshot.Packages...)
			for len(packages) <= blackboxMaxRules {
				packages = append(packages, BlackboxPackageRule{
					ID: fmt.Sprintf("bulk-%d", len(packages)), Ecosystem: "npm", Name: "x", Version: "*", Kind: "malware", Severity: "high",
				})
			}
			o.Snapshot.Packages = packages
			o.Complete = []string{"dependency", "pattern", "secret_exposure"}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newBlackboxExportFixture(t)
			testCase.mutate(&fixture.options)
			if _, err := ExportBlackboxEnvelope(fixture.options); err == nil {
				t.Fatal("export was accepted")
			}
		})
	}
}

func TestExportBlackboxEnvelopeRefusesForeignKeyAndPins(t *testing.T) {
	fixture := newBlackboxExportFixture(t)
	otherPublic, otherPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	fixture.options.PrivateKey = otherPrivate
	if _, err := ExportBlackboxEnvelope(fixture.options); err == nil {
		t.Fatal("a signing key outside the pinned authority was accepted")
	}
	fixture.options.PrivateKey = fixture.private
	foreign := fixture.pins
	foreign.PublicKey = otherPublic
	fixture.options.Pins = foreign
	if _, err := ExportBlackboxEnvelope(fixture.options); err == nil {
		t.Fatal("export against a foreign pin set was accepted")
	}
}

func TestExportBlackboxEnvelopeExportVerifiesWhatItEmits(t *testing.T) {
	fixture := newBlackboxExportFixture(t)
	envelope := fixture.export(t)
	decoded, err := DecodeBlackboxEnvelope(bytes.NewReader(mustJSON(t, envelope)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	options := BlackboxVerifyOptions{Pins: fixture.pins, Now: fixture.now, LastGeneration: envelope.Authority.Generation}
	if _, err := VerifyBlackboxEnvelope(decoded, options); err == nil {
		t.Fatal("a replayed generation was accepted by the receiver")
	}
	options.LastGeneration = envelope.Authority.Generation - 1
	if _, err := VerifyBlackboxEnvelope(decoded, options); err != nil {
		t.Fatalf("the emitted envelope fails the receiver: %v", err)
	}
	future := fixture.options
	future.Now = fixture.now
	future.TTL = time.Minute
	short, err := ExportBlackboxEnvelope(future)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := VerifyBlackboxEnvelope(short, BlackboxVerifyOptions{
		Pins: fixture.pins, Now: fixture.now.Add(2 * time.Minute), LastGeneration: short.Authority.Generation - 1,
	}); err == nil {
		t.Fatal("an expired envelope was accepted")
	}
}

func BenchmarkExportBlackboxEnvelope(b *testing.B) {
	fixture := newBlackboxExportFixture(b)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := ExportBlackboxEnvelope(fixture.options); err != nil {
			b.Fatalf("export: %v", err)
		}
	}
}
