package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

type blackboxKeygenCommandResult struct {
	Output    string `json:"output"`
	PublicKey string `json:"public_key"`
	KeyIDHint string `json:"key_id_hint"`
}

// runBlackboxKeygen writes one new raw Ed25519 signing key for the blackbox
// snapshot exporter. The private key file is exactly the 64 bytes
// `captain blackbox export --signing-key` reads: one new regular file, mode
// 0600, never an existing path. It prints the base64 public key and a key id
// hint derived from it; the operator still chooses the pinned key id, trust
// root, graph and network strings at export time.
func runBlackboxKeygen(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("blackbox keygen", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outputPath := flags.String("output", "", "new raw ed25519 private key file")
	publicPath := flags.String("public", "", "optional new base64 public key file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *outputPath == "" {
		return fmt.Errorf("usage: captain blackbox keygen --output <new-key-file> [--public <new-public-file>]")
	}
	if _, err := os.Lstat(*outputPath); err == nil {
		return fmt.Errorf("blackbox keygen output already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect blackbox keygen output: %w", err)
	}
	if *publicPath != "" {
		if _, err := os.Lstat(*publicPath); err == nil {
			return fmt.Errorf("blackbox keygen public output already exists")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect blackbox keygen public output: %w", err)
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ed25519 key: %w", err)
	}
	if err := writeBlackboxExportOutput(*outputPath, privateKey); err != nil {
		return fmt.Errorf("write signing key: %w", err)
	}
	if *publicPath != "" {
		if err := writeBlackboxExportOutput(*publicPath, []byte(base64.StdEncoding.EncodeToString(publicKey)+"\n")); err != nil {
			return fmt.Errorf("write public key: %w", err)
		}
	}
	digest := sha256.Sum256(publicKey)
	result := blackboxKeygenCommandResult{
		Output:    *outputPath,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
		KeyIDHint: "bbx-" + hex.EncodeToString(digest[:6]),
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("write blackbox keygen result: %w", err)
	}
	return nil
}
