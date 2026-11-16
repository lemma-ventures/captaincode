package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBlackboxKeygenWritesPrivateKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signing.key")
	publicPath := filepath.Join(dir, "signing.pub")
	var stdout bytes.Buffer
	if err := runBlackboxKeygen([]string{"--output", keyPath, "--public", publicPath}, &stdout); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", info.Mode())
	}
	if info.Size() != int64(ed25519.PrivateKeySize) {
		t.Fatalf("key size %d", info.Size())
	}
	body, err := readBlackboxExportFile(keyPath, ed25519.PrivateKeySize, true)
	if err != nil {
		t.Fatalf("read key back: %v", err)
	}
	privateKey := ed25519.PrivateKey(body)
	var result blackboxKeygenCommandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	encoded, err := base64.StdEncoding.DecodeString(result.PublicKey)
	if err != nil || len(encoded) != ed25519.PublicKeySize {
		t.Fatalf("public key decode: %v %d", err, len(encoded))
	}
	if !bytes.Equal(privateKey.Public().(ed25519.PublicKey), encoded) {
		t.Fatal("reported public key does not match the private key")
	}
	if result.KeyIDHint == "" || len(result.KeyIDHint) != len("bbx-")+12 {
		t.Fatalf("key id hint %q", result.KeyIDHint)
	}
	publicBody, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatalf("read public file: %v", err)
	}
	if string(bytes.TrimSpace(publicBody)) != result.PublicKey {
		t.Fatal("public key file does not match the reported key")
	}
}

func TestBlackboxKeygenSignsAndVerifies(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signing.key")
	var stdout bytes.Buffer
	if err := runBlackboxKeygen([]string{"--output", keyPath}, &stdout); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	body, err := readBlackboxExportFile(keyPath, ed25519.PrivateKeySize, true)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	privateKey := ed25519.PrivateKey(body)
	message := []byte("blackbox keygen round trip")
	signature := ed25519.Sign(privateKey, message)
	if !ed25519.Verify(privateKey.Public().(ed25519.PublicKey), message, signature) {
		t.Fatal("signature does not verify")
	}
}

func TestBlackboxKeygenRefusesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signing.key")
	var stdout bytes.Buffer
	if err := runBlackboxKeygen([]string{"--output", keyPath}, &stdout); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if err := runBlackboxKeygen([]string{"--output", keyPath}, &stdout); err == nil {
		t.Fatal("keygen overwrote an existing key")
	}
	publicPath := filepath.Join(dir, "signing.pub")
	if err := os.WriteFile(publicPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherKey := filepath.Join(dir, "other.key")
	if err := runBlackboxKeygen([]string{"--output", otherKey, "--public", publicPath}, &stdout); err == nil {
		t.Fatal("keygen overwrote an existing public key file")
	}
	if _, err := os.Lstat(otherKey); !os.IsNotExist(err) {
		t.Fatalf("refused run left a key file behind: %v", err)
	}
}

func TestBlackboxKeygenKeysDiffer(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one.key")
	second := filepath.Join(dir, "two.key")
	var stdout bytes.Buffer
	if err := runBlackboxKeygen([]string{"--output", first}, &stdout); err != nil {
		t.Fatalf("keygen one: %v", err)
	}
	if err := runBlackboxKeygen([]string{"--output", second}, &stdout); err != nil {
		t.Fatalf("keygen two: %v", err)
	}
	one, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(one, two) {
		t.Fatal("two keygens produced the same key")
	}
}
