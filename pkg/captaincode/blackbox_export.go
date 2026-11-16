package captaincode

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

type BlackboxExportOptions struct {
	Snapshot        BlackboxSnapshot
	Complete        []string
	Omitted         []string
	Revocations     []string
	EnvelopeID      string
	UpstreamVersion string
	Now             time.Time
	TTL             time.Duration
	Sequence        int
	Generation      uint64
	Pins            BlackboxTrustPins
	PrivateKey      ed25519.PrivateKey
}

func ExportBlackboxEnvelope(options BlackboxExportOptions) (BlackboxEnvelope, error) {
	if options.Generation == 0 {
		return BlackboxEnvelope{}, fmt.Errorf("export generation must increase from zero")
	}
	if options.Sequence <= 0 {
		return BlackboxEnvelope{}, fmt.Errorf("export sequence %d is not positive", options.Sequence)
	}
	if options.TTL <= 0 {
		return BlackboxEnvelope{}, fmt.Errorf("export lifetime must be positive")
	}
	if len(options.PrivateKey) != ed25519.PrivateKeySize {
		return BlackboxEnvelope{}, fmt.Errorf("export requires an ed25519 private key")
	}
	signingKey := options.PrivateKey.Public().(ed25519.PublicKey)
	if len(options.Pins.PublicKey) != ed25519.PublicKeySize || !bytes.Equal(signingKey, options.Pins.PublicKey) {
		return BlackboxEnvelope{}, fmt.Errorf("export signing key does not match the pinned authority")
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	envelope := BlackboxEnvelope{
		Schema:      BlackboxEnvelopeSchema,
		ID:          strings.TrimSpace(options.EnvelopeID),
		Source:      BlackboxExportSource,
		Revision:    options.Snapshot.Revision,
		GeneratedAt: now,
		ExpiresAt:   now.Add(options.TTL),
		Engine:      BlackboxEngine{EvaluatorMin: blackboxEvaluatorSchema, UpstreamVersion: options.UpstreamVersion},
		Authority: BlackboxAuthority{
			Kind:       BlackboxAuthorityEd25519,
			KeyID:      options.Pins.KeyID,
			PublicKey:  base64.StdEncoding.EncodeToString(options.Pins.PublicKey),
			Generation: options.Generation,
			TrustRoot:  options.Pins.TrustRoot,
			Graph:      options.Pins.Graph,
			Network:    options.Pins.Network,
		},
		Manifest: BlackboxManifest{
			Sequence:           options.Sequence,
			RuleCounts:         blackboxRuleCounts(options.Snapshot),
			CompleteCategories: append([]string{}, options.Complete...),
			Omitted:            append([]string{}, options.Omitted...),
		},
		Revocations: append([]string{}, options.Revocations...),
		Rules:       options.Snapshot,
	}
	payload, err := canonicalBlackboxEnvelope(envelope)
	if err != nil {
		return BlackboxEnvelope{}, fmt.Errorf("canonicalize export: %w", err)
	}
	envelope.Authority.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(options.PrivateKey, payload))
	if _, err := VerifyBlackboxEnvelope(envelope, BlackboxVerifyOptions{
		Pins:           options.Pins,
		Now:            now,
		LastGeneration: options.Generation - 1,
	}); err != nil {
		return BlackboxEnvelope{}, fmt.Errorf("export fails the receiver contract: %w", err)
	}
	return envelope, nil
}

func blackboxRuleCounts(snapshot BlackboxSnapshot) map[string]int {
	return map[string]int{
		"dependency":     len(snapshot.Packages),
		"protected_path": len(snapshot.Paths),
		"pattern":        len(snapshot.Patterns),
	}
}
