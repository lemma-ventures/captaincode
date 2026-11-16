package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// blackboxPolicyBody is one neutral operator path policy: two critical or
// high path rules. It never names a real location.
func blackboxPolicyBody() string {
	return `[{"id":"path:secrets","path":"/srv/app/secrets/**","kind":"protected_path","severity":"critical"},
		{"id":"path:keys","path":"/srv/app/keys/*.key","kind":"protected_path","severity":"high"}]`
}

func blackboxPolicyFile(t testing.TB, dir, body string, mode os.FileMode) string {
	t.Helper()
	policy := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policy, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestBlackboxExportCommandCarriesLocalPathPolicy(t *testing.T) {
	fixture := newBlackboxExportCommandFixture(t)
	policy := blackboxPolicyFile(t, fixture.dir, blackboxPolicyBody(), 0o600)
	args := append(fixture.args(), "--paths", policy)
	var output bytes.Buffer
	if err := runBlackboxExport(args, &output); err != nil {
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
	if len(envelope.Rules.Paths) != 2 || !envelope.Rules.Categories["protected_path"] {
		t.Fatalf("rules carry %d path rules and categories %v", len(envelope.Rules.Paths), envelope.Rules.Categories)
	}
	if envelope.Manifest.RuleCounts["protected_path"] != 2 {
		t.Fatalf("rule counts are %v", envelope.Manifest.RuleCounts)
	}
	for _, entry := range envelope.Manifest.CompleteCategories {
		if entry == "protected_path" {
			t.Fatalf("complete categories claim path completeness over an omitted source: %v", envelope.Manifest.CompleteCategories)
		}
	}
	store, err := captaincode.OpenBlackboxStore(fixture.store, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	if store.Snapshot().ID != "fixture-export" || !store.Snapshot().Categories["protected_path"] || len(store.Snapshot().Paths) != 2 {
		t.Fatalf("store snapshot %q carries categories %v and %d path rules", store.Snapshot().ID, store.Snapshot().Categories, len(store.Snapshot().Paths))
	}
	var result blackboxExportCommandResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.PathRules != 2 {
		t.Fatalf("result reports %d path rules", result.PathRules)
	}
}

func TestBlackboxExportCommandRefusesUnsafePathPolicy(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		mode os.FileMode
		link bool
	}{
		{name: "group readable", body: blackboxPolicyBody(), mode: 0o644},
		{name: "symlink", body: blackboxPolicyBody(), mode: 0o600, link: true},
		{name: "empty policy", body: `[]`, mode: 0o600},
		{name: "unknown field", body: `[{"id":"path:a","path":"/srv/app/a/**","kind":"protected_path","severity":"critical","oops":1}]`, mode: 0o600},
		{name: "bad glob", body: `[{"id":"path:a","path":"/srv/app/[","kind":"protected_path","severity":"critical"}]`, mode: 0o600},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBlackboxExportCommandFixture(t)
			policy := filepath.Join(fixture.dir, "policy.json")
			if test.link {
				source := filepath.Join(fixture.dir, "policy-source.json")
				if err := os.WriteFile(source, []byte(test.body), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(source, policy); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(policy, []byte(test.body), test.mode); err != nil {
				t.Fatal(err)
			}
			if err := runBlackboxExport(append(fixture.args(), "--paths", policy), &bytes.Buffer{}); err == nil {
				t.Fatal("accepted an unsafe path policy")
			}
			if _, err := os.Stat(fixture.output); !os.IsNotExist(err) {
				t.Fatal("an unsafe policy produced an export")
			}
		})
	}
	missing := newBlackboxExportCommandFixture(t)
	if err := runBlackboxExport(append(missing.args(), "--paths", filepath.Join(missing.dir, "absent.json")), &bytes.Buffer{}); err == nil {
		t.Fatal("accepted a missing path policy")
	}
	if _, err := os.Stat(missing.output); !os.IsNotExist(err) {
		t.Fatal("a missing policy produced an export")
	}
}

func TestBlackboxHookEnforceRefusesProtectedPathFromTheStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CAPTAIN_BLACKBOX_STATE", blackboxHookState(t, dir))
	fixture := newBlackboxExportCommandFixture(t)
	fixture.store = filepath.Join(dir, "store")
	fixture.output = filepath.Join(dir, "envelope.json")
	policy := blackboxPolicyFile(t, dir, blackboxPolicyBody(), 0o600)
	if err := runBlackboxExport(append(fixture.args(), "--paths", policy), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTAIN_BLACKBOX_STORE", fixture.store)
	t.Setenv("CAPTAIN_BLACKBOX_KEY_ID", fixture.pins.KeyID)
	t.Setenv("CAPTAIN_BLACKBOX_PUBLIC_KEY", base64.StdEncoding.EncodeToString(fixture.pins.PublicKey))
	t.Setenv("CAPTAIN_BLACKBOX_TRUST_ROOT", fixture.pins.TrustRoot)
	t.Setenv("CAPTAIN_BLACKBOX_GRAPH", fixture.pins.Graph)
	t.Setenv("CAPTAIN_BLACKBOX_NETWORK", fixture.pins.Network)

	runHook := func(input string) blackboxHookOutput {
		t.Helper()
		var stdout bytes.Buffer
		if err := runBlackbox([]string{"hook", "--mode", "enforce"}, strings.NewReader(input), &stdout); err != nil {
			t.Fatalf("hook: %v", err)
		}
		var out blackboxHookOutput
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	refused := runHook(`{"tool":"write","cwd":"/srv/app/repo","session":"ses-1","phase":"pre","args":{"file_path":"/srv/app/secrets/token","content":"x"}}`)
	if !refused.Refuse || !strings.HasSuffix(refused.Reason, "blackbox_rule_match:path:secrets") {
		t.Fatalf("out = %+v, the critical path rule must refuse", refused)
	}
	if refused.Disposition != "blocked" {
		t.Fatalf("disposition is %q", refused.Disposition)
	}
	ordinary := runHook(`{"tool":"write","cwd":"/srv/app/repo","session":"ses-2","phase":"pre","args":{"file_path":"src/main.go","content":"x"}}`)
	if ordinary.Refuse || ordinary.Disposition != "evaluated" {
		t.Fatalf("out = %+v, an ordinary path must screen clean", ordinary)
	}
	records, _, err := captaincode.ReadBlackboxSecurityRecords(blackboxHookState(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	blocked := 0
	for _, record := range records {
		if record.Disposition == "blocked" {
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatalf("filed %d blocked records, want 1 across %d records", blocked, len(records))
	}
}

func BenchmarkBlackboxExportCommandWithPolicy(b *testing.B) {
	fixture := newBlackboxExportCommandFixture(b)
	policy := blackboxPolicyFile(b, fixture.dir, blackboxPolicyBody(), 0o600)
	base := append(fixture.args(), "--paths", policy)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		args := append([]string{}, base...)
		for position := range args {
			if args[position] == "--output" {
				args[position+1] = filepath.Join(fixture.dir, fmt.Sprintf("policy-export-%d.json", index))
			}
		}
		if err := runBlackboxExport(args, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}
