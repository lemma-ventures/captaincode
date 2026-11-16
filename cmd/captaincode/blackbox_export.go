package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type blackboxExportCommandResult struct {
	ID         string    `json:"id"`
	Digest     string    `json:"digest"`
	Generation uint64    `json:"generation"`
	Sequence   int       `json:"sequence"`
	PathRules  int       `json:"path_rules"`
	ExpiresAt  time.Time `json:"expires_at"`
	Output     string    `json:"output"`
}

func runBlackboxExport(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("blackbox export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	cachePath := flags.String("cache", "", "upstream ruleset cache")
	envelopeID := flags.String("id", "", "snapshot and envelope id")
	revision := flags.String("revision", "", "upstream revision")
	keyID := flags.String("key-id", "", "pinned authority key id")
	keyPath := flags.String("signing-key", "", "raw ed25519 private key file")
	trustRoot := flags.String("trust-root", "", "pinned trust root")
	network := flags.String("network", "", "pinned network")
	storePath := flags.String("store", "", "accepted snapshot store directory")
	outputPath := flags.String("output", "", "new signed envelope file")
	upstreamVersion := flags.String("upstream-version", "", "upstream version metadata")
	generation := flags.Uint64("generation", 0, "authority generation (default store generation plus one)")
	sequence := flags.Int("sequence", 0, "manifest sequence (default generation)")
	ttl := flags.Duration("ttl", 24*time.Hour, "snapshot lifetime")
	policyPath := flags.String("paths", "", "operator protected_path policy file (json array)")
	ecosystems := flags.String("ecosystems", "", "comma-separated package ecosystems")
	maxRules := flags.Int("max-rules", 0, "maximum imported rules")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("usage: captain blackbox export --cache <ruleset.json> --id <id> --revision <rev> --key-id <id> --signing-key <file> --trust-root <root> --network <name> --store <dir> --output <new-file> [--generation n --sequence n --ttl 24h --ecosystems npm,pypi --max-rules n --paths <policy.json>]")
	}
	for name, value := range map[string]string{
		"cache": *cachePath, "id": *envelopeID, "revision": *revision,
		"key-id": *keyID, "signing-key": *keyPath, "trust-root": *trustRoot,
		"network": *network, "store": *storePath, "output": *outputPath,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("blackbox export requires --%s", name)
		}
	}
	if *ttl <= 0 {
		return fmt.Errorf("blackbox export lifetime must be positive")
	}
	if _, err := os.Lstat(*outputPath); err == nil {
		return fmt.Errorf("blackbox export output already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect blackbox export output: %w", err)
	}
	privateKeyBytes, err := readBlackboxExportFile(*keyPath, ed25519.PrivateKeySize, true)
	if err != nil {
		return fmt.Errorf("read signing key: %w", err)
	}
	if len(privateKeyBytes) != ed25519.PrivateKeySize {
		return fmt.Errorf("private key must contain exactly %d bytes", ed25519.PrivateKeySize)
	}
	privateKey := ed25519.PrivateKey(privateKeyBytes)
	var localPaths []captaincode.BlackboxPathRule
	if *policyPath != "" {
		policy, readErr := readBlackboxExportFile(*policyPath, captaincode.BlackboxPathPolicyMaxBytes, true)
		if readErr != nil {
			return fmt.Errorf("read path policy: %w", readErr)
		}
		localPaths, err = captaincode.DecodeBlackboxPathPolicy(bytes.NewReader(policy))
		if err != nil {
			return fmt.Errorf("path policy: %w", err)
		}
	}
	cache, err := readBlackboxExportFile(*cachePath, captaincode.BlackboxUpstreamMaxCacheBytes, false)
	if err != nil {
		return fmt.Errorf("read upstream cache: %w", err)
	}
	var selected []string
	if *ecosystems != "" {
		selected = strings.Split(*ecosystems, ",")
	}
	imported, err := captaincode.ImportBlackboxUpstreamCache(bytes.NewReader(cache), captaincode.BlackboxImportOptions{
		SnapshotID: *envelopeID,
		Revision:   *revision,
		Ecosystems: selected,
		MaxRules:   *maxRules,
		LocalPaths: localPaths,
	})
	if err != nil {
		return err
	}
	pins := captaincode.BlackboxTrustPins{
		KeyID:     *keyID,
		PublicKey: privateKey.Public().(ed25519.PublicKey),
		TrustRoot: *trustRoot,
		Graph:     imported.Graph,
		Network:   *network,
	}
	store, err := captaincode.OpenBlackboxStore(*storePath, pins)
	if err != nil {
		return err
	}
	if *generation == 0 {
		*generation = store.Generation() + 1
	}
	if *generation == 0 {
		return fmt.Errorf("blackbox export generation must increase from zero")
	}
	if *sequence == 0 {
		*sequence = int(*generation)
	}
	if *sequence <= store.Sequence() {
		return fmt.Errorf("blackbox export sequence %d does not exceed last accepted %d", *sequence, store.Sequence())
	}
	now := time.Now().UTC()
	envelope, err := captaincode.ExportBlackboxEnvelope(captaincode.BlackboxExportOptions{
		Snapshot:        imported.Snapshot,
		Complete:        imported.CompleteCategories,
		Omitted:         imported.Omitted,
		Revocations:     imported.Revocations,
		EnvelopeID:      *envelopeID,
		UpstreamVersion: *upstreamVersion,
		Now:             now,
		TTL:             *ttl,
		Sequence:        *sequence,
		Generation:      *generation,
		Pins:            pins,
		PrivateKey:      privateKey,
	})
	if err != nil {
		return err
	}
	if err := store.Accept(envelope, now); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("encode accepted export: %w", err)
	}
	if err := writeBlackboxExportOutput(*outputPath, append(encoded, '\n')); err != nil {
		return fmt.Errorf("store accepted generation %d but output failed: %w", *generation, err)
	}
	result := blackboxExportCommandResult{
		ID: envelope.ID, Digest: store.Digest(), Generation: *generation,
		Sequence: *sequence, PathRules: len(envelope.Rules.Paths),
		ExpiresAt: envelope.ExpiresAt, Output: *outputPath,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("store and output accepted generation %d but result output failed: %w", *generation, err)
	}
	return nil
}

// readBlackboxExportFile reads one protected local file without following a
// symlink. `protected` files are one regular file with mode 0600 and a single
// hard link: the signing key and the operator path policy both change what the
// exporter will vouch for, so neither may be swapped under it. The upstream
// cache is untrusted input that travels through the signature, so it carries
// no mode requirement.
func readBlackboxExportFile(path string, maximum int, protected bool) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if protected && (info.Mode().Perm() != 0o600 || info.Sys().(*syscall.Stat_t).Nlink != 1) {
		return nil, fmt.Errorf("file must be one regular file with mode 0600")
	}
	if info.Size() < 0 || info.Size() > int64(maximum) {
		return nil, fmt.Errorf("file size exceeds %d bytes", maximum)
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maximum {
		return nil, fmt.Errorf("file size exceeds %d bytes", maximum)
	}
	return body, nil
}

func writeBlackboxExportOutput(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	name := file.Name()
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
