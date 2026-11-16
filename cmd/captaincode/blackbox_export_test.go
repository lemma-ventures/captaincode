package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type blackboxExportCommandFixture struct {
	dir    string
	cache  string
	key    string
	store  string
	output string
	pins   captaincode.BlackboxTrustPins
}

func newBlackboxExportCommandFixture(t testing.TB) blackboxExportCommandFixture {
	t.Helper()
	dir := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "authority.key")
	if err := os.WriteFile(key, privateKey, 0o600); err != nil {
		t.Fatal(err)
	}
	return blackboxExportCommandFixture{
		dir:    dir,
		cache:  filepath.Join("..", "..", "pkg", "captaincode", "testdata", "blackbox", "upstream_ruleset.json"),
		key:    key,
		store:  filepath.Join(dir, "store"),
		output: filepath.Join(dir, "envelope.json"),
		pins: captaincode.BlackboxTrustPins{
			KeyID:     "fixture-key",
			PublicKey: publicKey,
			TrustRoot: "fixture-root",
			Graph:     "urn:graph:demo",
			Network:   "fixture-network",
		},
	}
}

func (f blackboxExportCommandFixture) args() []string {
	return []string{
		"--cache", f.cache,
		"--id", "fixture-export",
		"--revision", "7d697683e0d56608cfe3fee374d6a6866702058a",
		"--key-id", f.pins.KeyID,
		"--signing-key", f.key,
		"--trust-root", f.pins.TrustRoot,
		"--network", f.pins.Network,
		"--store", f.store,
		"--output", f.output,
	}
}

func TestBlackboxExportCommandSignsAndAccepts(t *testing.T) {
	fixture := newBlackboxExportCommandFixture(t)
	var output bytes.Buffer
	if err := runBlackboxExport(fixture.args(), &output); err != nil {
		t.Fatal(err)
	}
	envelopeFile, err := os.Open(fixture.output)
	if err != nil {
		t.Fatal(err)
	}
	envelope, decodeErr := captaincode.DecodeBlackboxEnvelope(envelopeFile)
	closeErr := envelopeFile.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if envelope.Authority.Generation != 1 || envelope.Manifest.Sequence != 1 {
		t.Fatalf("generation %d sequence %d", envelope.Authority.Generation, envelope.Manifest.Sequence)
	}
	store, err := captaincode.OpenBlackboxStore(fixture.store, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	if !store.HasCurrent() || store.Generation() != 1 || store.Snapshot().ID != "fixture-export" {
		t.Fatalf("store current %t generation %d snapshot %q", store.HasCurrent(), store.Generation(), store.Snapshot().ID)
	}
	var result blackboxExportCommandResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Generation != 1 || result.Digest != store.Digest() || result.Output != fixture.output {
		t.Fatalf("result = %+v", result)
	}
	info, err := os.Stat(fixture.output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode %v", info.Mode().Perm())
	}
}

func TestBlackboxExportCommandAdvancesDefaults(t *testing.T) {
	fixture := newBlackboxExportCommandFixture(t)
	if err := runBlackboxExport(fixture.args(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	second := fixture
	second.output = filepath.Join(fixture.dir, "second.json")
	if err := runBlackboxExport(second.args(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	store, err := captaincode.OpenBlackboxStore(fixture.store, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	if store.Generation() != 2 {
		t.Fatalf("generation %d", store.Generation())
	}
	if err := store.Rollback(); err != nil {
		t.Fatal(err)
	}
	if store.Generation() != 1 {
		t.Fatalf("rollback generation %d", store.Generation())
	}
}

func TestBlackboxExportCommandRefusesReplayWithoutReplacingOutput(t *testing.T) {
	fixture := newBlackboxExportCommandFixture(t)
	args := append(fixture.args(), "--generation", "4", "--sequence", "9")
	if err := runBlackboxExport(args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fixture.output)
	if err != nil {
		t.Fatal(err)
	}
	if err := runBlackboxExport(args, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted a replay")
	}
	after, err := os.ReadFile(fixture.output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a replay replaced the accepted export")
	}
	store, err := captaincode.OpenBlackboxStore(fixture.store, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	if store.Generation() != 4 || !store.VerifiedAt().After(time.Time{}) {
		t.Fatalf("store generation %d verified at %s", store.Generation(), store.VerifiedAt())
	}
}

func TestBlackboxExportCommandProtectsTheSigningKey(t *testing.T) {
	for _, test := range []struct {
		name string
		mode os.FileMode
		link bool
	}{
		{name: "group readable", mode: 0o640},
		{name: "world readable", mode: 0o604},
		{name: "symlink", mode: 0o600, link: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBlackboxExportCommandFixture(t)
			key := fixture.key
			if test.link {
				key = filepath.Join(fixture.dir, "linked.key")
				if err := os.Symlink(fixture.key, key); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(key, test.mode); err != nil {
				t.Fatal(err)
			}
			args := fixture.args()
			for index := range args {
				if args[index] == "--signing-key" {
					args[index+1] = key
				}
			}
			if err := runBlackboxExport(args, &bytes.Buffer{}); err == nil {
				t.Fatal("accepted an unsafe signing key")
			}
			if _, err := os.Stat(fixture.output); !os.IsNotExist(err) {
				t.Fatal("an unsafe key produced an export")
			}
		})
	}
}

func BenchmarkBlackboxExportCommand(b *testing.B) {
	fixture := newBlackboxExportCommandFixture(b)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		args := fixture.args()
		for position := range args {
			if args[position] == "--output" {
				args[position+1] = filepath.Join(fixture.dir, fmt.Sprintf("export-%d.json", index))
			}
		}
		if err := runBlackboxExport(args, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

func TestBlackboxExportCommandRefusesBadArguments(t *testing.T) {
	fixture := newBlackboxExportCommandFixture(t)
	for _, args := range [][]string{
		nil,
		{"--cache", fixture.cache},
		append(fixture.args(), "extra"),
		append(fixture.args(), "--ttl", "0s"),
		append(fixture.args(), "--sequence", "-1"),
	} {
		if err := runBlackboxExport(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
}
