package captaincode

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const blackboxEvaluatorSchema = 1
const (
	BlackboxEnvelopeSchema   = 2
	BlackboxExportSource     = "agent-blackbox-export"
	BlackboxAuthorityEd25519 = "ed25519"
	BlackboxMaxEnvelopeBytes = 8 << 20
)

type BlackboxEngine struct {
	EvaluatorMin    int    `json:"evaluator_min"`
	UpstreamVersion string `json:"upstream_version"`
}

type BlackboxAuthority struct {
	Kind       string `json:"kind"`
	KeyID      string `json:"key_id"`
	PublicKey  string `json:"public_key"`
	Signature  string `json:"signature"`
	Generation uint64 `json:"generation"`
	TrustRoot  string `json:"trust_root"`
	Graph      string `json:"graph"`
	Network    string `json:"network"`
}

type BlackboxManifest struct {
	Sequence           int            `json:"sequence"`
	RuleCounts         map[string]int `json:"rule_counts"`
	CompleteCategories []string       `json:"complete_categories"`
	Omitted            []string       `json:"omitted"`
}

type BlackboxEnvelope struct {
	Schema      int               `json:"schema"`
	ID          string            `json:"id"`
	Source      string            `json:"source"`
	Revision    string            `json:"revision"`
	GeneratedAt time.Time         `json:"generated_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Engine      BlackboxEngine    `json:"engine"`
	Authority   BlackboxAuthority `json:"authority"`
	Manifest    BlackboxManifest  `json:"manifest"`
	Revocations []string          `json:"revocations"`
	Rules       BlackboxSnapshot  `json:"rules"`
}

type BlackboxTrustPins struct {
	KeyID     string
	PublicKey ed25519.PublicKey
	TrustRoot string
	Graph     string
	Network   string
}

type BlackboxVerifyOptions struct {
	Pins           BlackboxTrustPins
	Now            time.Time
	LastGeneration uint64
}

func DecodeBlackboxEnvelope(reader io.Reader) (BlackboxEnvelope, error) {
	var envelope BlackboxEnvelope
	decoder := json.NewDecoder(io.LimitReader(reader, BlackboxMaxEnvelopeBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return BlackboxEnvelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	if decoder.More() {
		return BlackboxEnvelope{}, fmt.Errorf("decode envelope: trailing data")
	}
	return envelope, nil
}

func canonicalBlackboxEnvelope(envelope BlackboxEnvelope) ([]byte, error) {
	original := envelope.Authority.Signature
	envelope.Authority.Signature = ""
	encoded, err := json.Marshal(envelope)
	envelope.Authority.Signature = original
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func VerifyBlackboxEnvelope(envelope BlackboxEnvelope, options BlackboxVerifyOptions) (BlackboxSnapshot, error) {
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	if envelope.GeneratedAt.After(now.Add(5 * time.Minute)) {
		return BlackboxSnapshot{}, fmt.Errorf("envelope generated_at %s is in the future", envelope.GeneratedAt.Format(time.RFC3339))
	}
	if !envelope.ExpiresAt.After(now) {
		return BlackboxSnapshot{}, fmt.Errorf("envelope expired at %s", envelope.ExpiresAt.Format(time.RFC3339))
	}
	if envelope.Authority.Generation <= options.LastGeneration {
		return BlackboxSnapshot{}, fmt.Errorf("envelope generation %d does not exceed last accepted %d", envelope.Authority.Generation, options.LastGeneration)
	}
	return verifyBlackboxEnvelopeStructure(envelope, options)
}

func verifyBlackboxEnvelopeStructure(envelope BlackboxEnvelope, options BlackboxVerifyOptions) (BlackboxSnapshot, error) {
	if envelope.Schema != BlackboxEnvelopeSchema {
		return BlackboxSnapshot{}, fmt.Errorf("envelope schema %d is not %d", envelope.Schema, BlackboxEnvelopeSchema)
	}
	if envelope.Source != BlackboxExportSource {
		return BlackboxSnapshot{}, fmt.Errorf("envelope source %q is not %q", envelope.Source, BlackboxExportSource)
	}
	if strings.TrimSpace(envelope.ID) == "" || strings.TrimSpace(envelope.Revision) == "" {
		return BlackboxSnapshot{}, fmt.Errorf("envelope id and revision are required")
	}
	if envelope.Engine.EvaluatorMin > blackboxEvaluatorSchema {
		return BlackboxSnapshot{}, fmt.Errorf("envelope requires evaluator schema %d above supported %d", envelope.Engine.EvaluatorMin, blackboxEvaluatorSchema)
	}
	if err := verifyBlackboxAuthority(envelope, options); err != nil {
		return BlackboxSnapshot{}, err
	}
	if err := verifyBlackboxManifest(envelope); err != nil {
		return BlackboxSnapshot{}, err
	}
	if err := verifyBlackboxRules(&envelope); err != nil {
		return BlackboxSnapshot{}, err
	}
	snapshot := envelope.Rules
	snapshot.Digest = blackboxEnvelopeDigest(envelope)
	return snapshot, nil
}

func verifyBlackboxRules(envelope *BlackboxEnvelope) error {
	if err := validateBlackboxSnapshot(&envelope.Rules, BlackboxExportSource, "synthetic"); err != nil {
		return fmt.Errorf("envelope rules are invalid: %w", err)
	}
	return nil
}

func verifyBlackboxAuthority(envelope BlackboxEnvelope, options BlackboxVerifyOptions) error {
	authority := envelope.Authority
	if authority.Kind != BlackboxAuthorityEd25519 {
		return fmt.Errorf("authority kind %q is not %q", authority.Kind, BlackboxAuthorityEd25519)
	}
	if authority.KeyID != options.Pins.KeyID {
		return fmt.Errorf("authority key id %q does not match pinned %q", authority.KeyID, options.Pins.KeyID)
	}
	if authority.TrustRoot != options.Pins.TrustRoot {
		return fmt.Errorf("authority trust root %q does not match pinned %q", authority.TrustRoot, options.Pins.TrustRoot)
	}
	if authority.Graph != options.Pins.Graph {
		return fmt.Errorf("authority graph %q does not match pinned %q", authority.Graph, options.Pins.Graph)
	}
	if authority.Network != options.Pins.Network {
		return fmt.Errorf("authority network %q does not match pinned %q", authority.Network, options.Pins.Network)
	}
	publicKey, err := base64.StdEncoding.DecodeString(authority.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("authority public key is not %d base64 bytes", ed25519.PublicKeySize)
	}
	if !bytes.Equal(publicKey, options.Pins.PublicKey) {
		return fmt.Errorf("authority public key does not match the pinned key")
	}
	signature, err := base64.StdEncoding.DecodeString(authority.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("authority signature is not %d base64 bytes", ed25519.SignatureSize)
	}
	payload, err := canonicalBlackboxEnvelope(envelope)
	if err != nil {
		return fmt.Errorf("canonicalize envelope: %w", err)
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return fmt.Errorf("authority signature does not verify")
	}
	return nil
}

func verifyBlackboxManifest(envelope BlackboxEnvelope) error {
	manifest := envelope.Manifest
	if manifest.Sequence <= 0 {
		return fmt.Errorf("manifest sequence %d is not positive", manifest.Sequence)
	}
	rules := envelope.Rules
	counts := map[string]int{
		"dependency":     len(rules.Packages),
		"protected_path": len(rules.Paths),
		"pattern":        len(rules.Patterns),
	}
	for category, expected := range counts {
		if actual, ok := manifest.RuleCounts[category]; !ok {
			return fmt.Errorf("manifest is missing the rule count for %s", category)
		} else if actual != expected {
			return fmt.Errorf("manifest counts %d %s rules but the export carries %d", actual, category, expected)
		}
	}
	for category := range manifest.RuleCounts {
		if _, known := counts[category]; !known {
			return fmt.Errorf("manifest counts unknown category %q", category)
		}
	}
	complete := map[string]bool{}
	for _, category := range manifest.CompleteCategories {
		if complete[category] {
			return fmt.Errorf("manifest marks %s complete twice", category)
		}
		complete[category] = true
	}
	seenOmitted := map[string]bool{}
	for _, entry := range manifest.Omitted {
		if strings.TrimSpace(entry) == "" {
			return fmt.Errorf("manifest omits an empty record")
		}
		if seenOmitted[entry] {
			return fmt.Errorf("manifest omits %q twice", entry)
		}
		if complete[blackboxOmissionCategory(entry)] {
			return fmt.Errorf("manifest omits %q and marks its category complete", entry)
		}
		seenOmitted[entry] = true
	}
	for category := range complete {
		if enabled, ok := rules.Categories[category]; !ok || !enabled {
			return fmt.Errorf("manifest marks %s complete but the rules do not enable it", category)
		}
		if category != "secret_exposure" && counts[category] == 0 {
			return fmt.Errorf("manifest marks %s complete but the export carries no rules for it", category)
		}
	}
	revoked := make(map[string]bool, len(envelope.Revocations))
	for _, id := range envelope.Revocations {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("revocations contain an empty rule id")
		}
		if revoked[id] {
			return fmt.Errorf("revocations contain %q twice", id)
		}
		revoked[id] = true
	}
	for _, rule := range rules.Packages {
		if revoked[rule.ID] {
			return fmt.Errorf("revoked rule %q is still present in the export", rule.ID)
		}
	}
	for _, rule := range rules.Paths {
		if revoked[rule.ID] {
			return fmt.Errorf("revoked rule %q is still present in the export", rule.ID)
		}
	}
	for _, rule := range rules.Patterns {
		if revoked[rule.ID] {
			return fmt.Errorf("revoked rule %q is still present in the export", rule.ID)
		}
	}
	return nil
}

func blackboxOmissionCategory(entry string) string {
	if colon := strings.IndexByte(entry, ':'); colon >= 0 {
		return entry[:colon]
	}
	return entry
}

func blackboxEnvelopeDigest(envelope BlackboxEnvelope) string {
	payload, err := canonicalBlackboxEnvelope(envelope)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
