package captaincode

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type blackboxEnvelopeFixture struct {
	envelope   BlackboxEnvelope
	pins       BlackboxTrustPins
	options    BlackboxVerifyOptions
	privateKey ed25519.PrivateKey
}

func blackboxFixtureRules() BlackboxSnapshot {
	return BlackboxSnapshot{
		Schema:   1,
		ID:       "snap-1",
		Source:   "synthetic",
		Revision: "rev-1",
		Categories: map[string]bool{
			"dependency":      true,
			"protected_path":  true,
			"secret_exposure": true,
			"pattern":         true,
		},
		Packages: []BlackboxPackageRule{{ID: "pkg-1", Ecosystem: "npm", Name: "example-package", Version: "1.2.3", Kind: "known_malware", Severity: "critical"}},
		Paths:    []BlackboxPathRule{{ID: "path-1", Path: "config/private/**", Kind: "protected_path", Severity: "critical"}},
		Patterns: []BlackboxPatternRule{{ID: "pattern-1", Category: "pattern", Pattern: "dangerous-example", Kind: "heuristic", Severity: "warning"}},
	}
}

func newBlackboxEnvelopeFixture(t *testing.T) blackboxEnvelopeFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	envelope := BlackboxEnvelope{
		Schema:      BlackboxEnvelopeSchema,
		ID:          "env-1",
		Source:      BlackboxExportSource,
		Revision:    "7d697683e0d56608cfe3fee374d6a6866702058a",
		GeneratedAt: now.Add(-time.Hour),
		ExpiresAt:   now.Add(24 * time.Hour),
		Engine:      BlackboxEngine{EvaluatorMin: 1, UpstreamVersion: "0.1.0"},
		Authority: BlackboxAuthority{
			Kind:       BlackboxAuthorityEd25519,
			KeyID:      "key-1",
			PublicKey:  base64.StdEncoding.EncodeToString(publicKey),
			Generation: 4,
			TrustRoot:  "root-1",
			Graph:      "graph-1",
			Network:    "network-1",
		},
		Manifest: BlackboxManifest{
			Sequence:           2,
			RuleCounts:         map[string]int{"dependency": 1, "protected_path": 1, "pattern": 1},
			CompleteCategories: []string{"dependency", "protected_path", "secret_exposure", "pattern"},
		},
		Revocations: []string{"pkg-old"},
		Rules:       blackboxFixtureRules(),
	}
	pins := BlackboxTrustPins{KeyID: "key-1", PublicKey: publicKey, TrustRoot: "root-1", Graph: "graph-1", Network: "network-1"}
	fixture := blackboxEnvelopeFixture{envelope: envelope, pins: pins, options: BlackboxVerifyOptions{Pins: pins, Now: now, LastGeneration: 3}}
	fixture.sign(t, privateKey)
	return fixture
}

func (f *blackboxEnvelopeFixture) sign(t *testing.T, privateKey ed25519.PrivateKey) {
	t.Helper()
	f.envelope.Authority.Signature = ""
	payload, err := json.Marshal(f.envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	f.envelope.Authority.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	f.privateKey = privateKey
}

func TestBlackboxEnvelopeAcceptsAndCarriesDigest(t *testing.T) {
	fixture := newBlackboxEnvelopeFixture(t)
	snapshot, err := VerifyBlackboxEnvelope(fixture.envelope, fixture.options)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if snapshot.Digest == "" {
		t.Fatal("accepted snapshot carries no digest")
	}
	if snapshot.ID != fixture.envelope.Rules.ID || len(snapshot.Packages) != 1 {
		t.Fatalf("unexpected snapshot payload: %+v", snapshot)
	}
}

func TestBlackboxEnvelopeRoundTripDecode(t *testing.T) {
	fixture := newBlackboxEnvelopeFixture(t)
	encoded, err := json.Marshal(fixture.envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := DecodeBlackboxEnvelope(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := VerifyBlackboxEnvelope(decoded, fixture.options); err != nil {
		t.Fatalf("verify decoded: %v", err)
	}
}

func TestBlackboxEnvelopeRejectsUnknownFieldAndTrailingData(t *testing.T) {
	fixture := newBlackboxEnvelopeFixture(t)
	encoded, err := json.Marshal(fixture.envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := DecodeBlackboxEnvelope(strings.NewReader(`{"schema":2,"extra":1}`)); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if _, err := DecodeBlackboxEnvelope(io.MultiReader(bytes.NewReader(encoded), strings.NewReader("{}"))); err == nil {
		t.Fatal("trailing data was accepted")
	}
}

func TestBlackboxEnvelopeRejections(t *testing.T) {
	fixture := newBlackboxEnvelopeFixture(t)
	cases := []struct {
		name string
		mut  func(*BlackboxEnvelope)
	}{
		{"wrong schema", func(e *BlackboxEnvelope) { e.Schema = 3 }},
		{"wrong source", func(e *BlackboxEnvelope) { e.Source = "public" }},
		{"empty id", func(e *BlackboxEnvelope) { e.ID = "" }},
		{"empty revision", func(e *BlackboxEnvelope) { e.Revision = " " }},
		{"future generation time", func(e *BlackboxEnvelope) { e.GeneratedAt = fixture.options.Now.Add(time.Hour) }},
		{"expired", func(e *BlackboxEnvelope) { e.ExpiresAt = fixture.options.Now.Add(-time.Minute) }},
		{"unsupported engine", func(e *BlackboxEnvelope) { e.Engine.EvaluatorMin = 2 }},
		{"wrong authority kind", func(e *BlackboxEnvelope) { e.Authority.Kind = "hmac" }},
		{"wrong key id", func(e *BlackboxEnvelope) { e.Authority.KeyID = "key-2" }},
		{"wrong trust root", func(e *BlackboxEnvelope) { e.Authority.TrustRoot = "root-2" }},
		{"wrong graph", func(e *BlackboxEnvelope) { e.Authority.Graph = "graph-2" }},
		{"wrong network", func(e *BlackboxEnvelope) { e.Authority.Network = "network-2" }},
		{"replayed generation", func(e *BlackboxEnvelope) { e.Authority.Generation = 3 }},
		{"non positive sequence", func(e *BlackboxEnvelope) { e.Manifest.Sequence = 0 }},
		{"missing category count", func(e *BlackboxEnvelope) { delete(e.Manifest.RuleCounts, "pattern") }},
		{"wrong category count", func(e *BlackboxEnvelope) { e.Manifest.RuleCounts["dependency"] = 5 }},
		{"unknown category count", func(e *BlackboxEnvelope) { e.Manifest.RuleCounts["injection"] = 1 }},
		{"complete but disabled", func(e *BlackboxEnvelope) {
			e.Manifest.CompleteCategories = append(e.Manifest.CompleteCategories, "extra")
		}},
		{"complete but empty", func(e *BlackboxEnvelope) { e.Rules.Paths = nil }},
		{"revoked rule present", func(e *BlackboxEnvelope) { e.Revocations = []string{"pkg-1"} }},
		{"empty revocation id", func(e *BlackboxEnvelope) { e.Revocations = []string{" "} }},
		{"duplicate revocation", func(e *BlackboxEnvelope) { e.Revocations = []string{"pkg-old", "pkg-old"} }},
		{"tampered rules", func(e *BlackboxEnvelope) {
			e.Rules.Packages = append(e.Rules.Packages, BlackboxPackageRule{ID: "pkg-2", Ecosystem: "npm", Name: "other", Version: "*", Kind: "known_malware", Severity: "critical"})
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			envelope := fixture.envelope
			testCase.mut(&envelope)
			if _, err := VerifyBlackboxEnvelope(envelope, fixture.options); err == nil {
				t.Fatal("envelope was accepted")
			}
		})
	}
}

func TestBlackboxEnvelopeRejectsForgedSignatureAndKey(t *testing.T) {
	fixture := newBlackboxEnvelopeFixture(t)
	forged := fixture.envelope
	forged.Authority.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if _, err := VerifyBlackboxEnvelope(forged, fixture.options); err == nil {
		t.Fatal("zero signature was accepted")
	}
	otherPublic, otherPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signed := fixture.envelope
	signed.Authority.Signature = ""
	payload, _ := json.Marshal(signed)
	signed.Authority.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(otherPrivate, payload))
	signed.Authority.PublicKey = base64.StdEncoding.EncodeToString(otherPublic)
	if _, err := VerifyBlackboxEnvelope(signed, fixture.options); err == nil {
		t.Fatal("a signature from an unpinned key was accepted")
	}
}

func TestBlackboxEnvelopeReplayAcrossVerifier(t *testing.T) {
	fixture := newBlackboxEnvelopeFixture(t)
	if _, err := VerifyBlackboxEnvelope(fixture.envelope, fixture.options); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	replayed := fixture.options
	replayed.LastGeneration = fixture.envelope.Authority.Generation
	if _, err := VerifyBlackboxEnvelope(fixture.envelope, replayed); err == nil {
		t.Fatal("replayed generation was accepted")
	}
}

func BenchmarkBlackboxEnvelopeVerify(b *testing.B) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("generate key: %v", err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	envelope := BlackboxEnvelope{
		Schema: BlackboxEnvelopeSchema, ID: "bench", Source: BlackboxExportSource, Revision: "rev",
		GeneratedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
		Engine:    BlackboxEngine{EvaluatorMin: 1},
		Authority: BlackboxAuthority{Kind: BlackboxAuthorityEd25519, KeyID: "k", PublicKey: base64.StdEncoding.EncodeToString(publicKey), Generation: 9, TrustRoot: "r", Graph: "g", Network: "n"},
		Manifest:  BlackboxManifest{Sequence: 1, RuleCounts: map[string]int{"dependency": 512, "protected_path": 256, "pattern": 256}, CompleteCategories: []string{"dependency"}},
		Rules:     BlackboxSnapshot{Schema: 1, ID: "s", Source: "synthetic", Revision: "rev", Categories: map[string]bool{"dependency": true}},
	}
	for i := 0; i < 512; i++ {
		envelope.Rules.Packages = append(envelope.Rules.Packages, BlackboxPackageRule{ID: fmt.Sprintf("p%d", i), Ecosystem: "npm", Name: "x", Version: "*", Kind: "known_malware", Severity: "critical"})
	}
	for i := 0; i < 256; i++ {
		envelope.Rules.Paths = append(envelope.Rules.Paths, BlackboxPathRule{ID: fmt.Sprintf("t%d", i), Path: "a/**", Kind: "protected_path", Severity: "critical"})
		envelope.Rules.Patterns = append(envelope.Rules.Patterns, BlackboxPatternRule{ID: fmt.Sprintf("r%d", i), Category: "pattern", Pattern: "x", Kind: "heuristic", Severity: "warning"})
	}
	envelope.Authority.Signature = ""
	payload, _ := json.Marshal(envelope)
	envelope.Authority.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	options := BlackboxVerifyOptions{Pins: BlackboxTrustPins{KeyID: "k", PublicKey: publicKey, TrustRoot: "r", Graph: "g", Network: "n"}, Now: now, LastGeneration: 8}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := VerifyBlackboxEnvelope(envelope, options); err != nil {
			b.Fatalf("verify: %v", err)
		}
	}
}
