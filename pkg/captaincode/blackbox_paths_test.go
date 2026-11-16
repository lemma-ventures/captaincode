package captaincode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The operator path policy is the only source of protected-path rules: the
// snapshot contract leaves that category to local configuration, and the
// integration plan requires an explicit operator choice before a path rule
// can refuse an action.
func pathPolicyFixture() string {
	return `[
		{"id":"path:secrets","path":"/srv/app/secrets/**","kind":"protected_path","severity":"critical"},
		{"id":"path:keys","path":"/srv/app/keys/*.key","kind":"protected_path","severity":"high"}
	]`
}

func TestDecodeBlackboxPathPolicyAcceptsValidRules(t *testing.T) {
	rules, err := DecodeBlackboxPathPolicy(strings.NewReader(pathPolicyFixture()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("carries %d rules, want 2", len(rules))
	}
	if rules[0].ID != "path:secrets" || rules[0].Path != "/srv/app/secrets/**" || rules[0].Severity != "critical" {
		t.Fatalf("first rule is %+v", rules[0])
	}
	if rules[1].ID != "path:keys" || rules[1].Kind != "protected_path" {
		t.Fatalf("second rule is %+v", rules[1])
	}
}

// pathPolicyBody builds one policy document with `count` distinct rules.
func pathPolicyBody(count int) string {
	entries := make([]string, 0, count)
	for index := 0; index < count; index++ {
		entries = append(entries, fmt.Sprintf(`{"id":"path:r%d","path":"p%d/**","kind":"protected_path","severity":"critical"}`, index, index))
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func TestDecodeBlackboxPathPolicyRejections(t *testing.T) {
	longPath, _ := json.Marshal([]map[string]any{
		{"id": "path:long", "path": strings.Repeat("a", 4097), "kind": "protected_path", "severity": "critical"},
	})
	cases := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `[{"id":"path:a","path":"a/**","kind":"protected_path","severity":"critical","oops":1}]`},
		{name: "trailing data", body: pathPolicyFixture() + `{}`},
		{name: "oversized", body: strings.Repeat(" ", BlackboxPathPolicyMaxBytes+1) + `[]`},
		{name: "empty policy", body: `[]`},
		{name: "not an array", body: `{"id":"path:a","path":"a/**","kind":"protected_path","severity":"critical"}`},
		{name: "unknown kind", body: `[{"id":"path:a","path":"a/**","kind":"not a finding kind","severity":"critical"}]`},
		{name: "blank severity", body: `[{"id":"path:a","path":"a/**","kind":"protected_path","severity":""}]`},
		{name: "unsafe id", body: `[{"id":"path rule","path":"a/**","kind":"protected_path","severity":"critical"}]`},
		{name: "blank path", body: `[{"id":"path:a","path":"","kind":"protected_path","severity":"critical"}]`},
		{name: "oversized path", body: string(longPath)},
		{name: "path with a null byte", body: `[{"id":"path:a","path":"a\u0000b","kind":"protected_path","severity":"critical"}]`},
		{name: "bad glob", body: `[{"id":"path:a","path":"home/[","kind":"protected_path","severity":"critical"}]`},
		{name: "duplicate id", body: `[{"id":"path:a","path":"a/**","kind":"protected_path","severity":"critical"},{"id":"path:a","path":"b/**","kind":"protected_path","severity":"critical"}]`},
		{name: "rule limit", body: pathPolicyBody(BlackboxPathPolicyMaxRules + 1)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := DecodeBlackboxPathPolicy(strings.NewReader(testCase.body)); err == nil {
				t.Fatal("the policy was accepted")
			}
		})
	}
}

func TestDecodeBlackboxPathPolicyBoundsReader(t *testing.T) {
	reader := bytes.NewReader(bytes.Repeat([]byte(" "), BlackboxPathPolicyMaxBytes+1))
	wrapped := &countingReader{reader: reader}
	if _, err := DecodeBlackboxPathPolicy(wrapped); err == nil {
		t.Fatal("an unbounded reader was accepted")
	}
	if wrapped.read > BlackboxPathPolicyMaxBytes+1 {
		t.Fatalf("read %d bytes, want at most %d", wrapped.read, BlackboxPathPolicyMaxBytes+1)
	}
}

type countingReader struct {
	reader *bytes.Reader
	read   int
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.read += count
	return count, err
}

func TestImportBlackboxUpstreamCacheCarriesLocalPathPolicy(t *testing.T) {
	policy, err := DecodeBlackboxPathPolicy(strings.NewReader(pathPolicyFixture()))
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ImportBlackboxUpstreamCache(bytes.NewReader(upstreamFixtureBytes(t)), BlackboxImportOptions{
		SnapshotID: "upstream-7d697683-paths-1",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
		LocalPaths: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := imported.Snapshot
	if !snapshot.Categories["protected_path"] {
		t.Fatal("protected_path stays disabled although the policy carries rules")
	}
	if len(snapshot.Paths) != 2 {
		t.Fatalf("carries %d path rules, want 2", len(snapshot.Paths))
	}
	if err := validateBlackboxSnapshot(&snapshot, BlackboxExportSource); err != nil {
		t.Fatalf("imported rules do not validate: %v", err)
	}
	for _, entry := range imported.Omitted {
		if strings.HasPrefix(entry, "protected_path") {
			t.Fatalf("the policy category is also omitted: %v", imported.Omitted)
		}
	}
	// This fixture still drops the upstream `fileaccess` source, so the
	// export selects the category but does not claim it complete.
	wantComplete := []string{"secret_exposure"}
	if fmt.Sprint(imported.CompleteCategories) != fmt.Sprint(wantComplete) {
		t.Fatalf("complete categories are %v, want %v", imported.CompleteCategories, wantComplete)
	}
}

func TestImportBlackboxUpstreamCacheClaimsProtectedPathComplete(t *testing.T) {
	policy, err := DecodeBlackboxPathPolicy(strings.NewReader(pathPolicyFixture()))
	if err != nil {
		t.Fatal(err)
	}
	body := mutateFixture(t, func(document map[string]any) {
		delete(document, "fileaccess")
	})
	imported, err := ImportBlackboxUpstreamCache(bytes.NewReader(body), BlackboxImportOptions{
		SnapshotID: "upstream-paths-complete-1",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
		LocalPaths: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantComplete := []string{"protected_path", "secret_exposure"}
	if fmt.Sprint(imported.CompleteCategories) != fmt.Sprint(wantComplete) {
		t.Fatalf("complete categories are %v, want %v", imported.CompleteCategories, wantComplete)
	}
}

func TestImportBlackboxUpstreamCacheBoundsPathRulesToMaxRules(t *testing.T) {
	policy, err := DecodeBlackboxPathPolicy(strings.NewReader(pathPolicyFixture()))
	if err != nil {
		t.Fatal(err)
	}
	const maxRules = 3
	if _, err := ImportBlackboxUpstreamCache(bytes.NewReader(upstreamFixtureBytes(t)), BlackboxImportOptions{
		SnapshotID: "upstream-bound-1",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
		MaxRules:   maxRules,
	}); err != nil {
		t.Fatalf("the cache alone fits: %v", err)
	}
	if _, err := ImportBlackboxUpstreamCache(bytes.NewReader(upstreamFixtureBytes(t)), BlackboxImportOptions{
		SnapshotID: "upstream-bound-2",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
		MaxRules:   maxRules,
		LocalPaths: policy,
	}); err == nil {
		t.Fatal("the policy rules escaped the import bound")
	}
}

func TestExportEnvelopeWithLocalPathPolicyDecidesEnforcement(t *testing.T) {
	policy, err := DecodeBlackboxPathPolicy(strings.NewReader(pathPolicyFixture()))
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ImportBlackboxUpstreamCache(bytes.NewReader(upstreamFixtureBytes(t)), BlackboxImportOptions{
		SnapshotID: "envelope-path-policy-1",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
		LocalPaths: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	envelope, err := ExportBlackboxEnvelope(BlackboxExportOptions{
		Snapshot:    imported.Snapshot,
		Complete:    imported.CompleteCategories,
		Omitted:     imported.Omitted,
		Revocations: imported.Revocations,
		EnvelopeID:  "envelope-path-policy-1",
		Now:         now,
		TTL:         time.Hour,
		Sequence:    1,
		Generation:  1,
		Pins: BlackboxTrustPins{
			KeyID: "fixture-key", PublicKey: publicKey,
			TrustRoot: "fixture-root", Graph: imported.Graph, Network: "fixture-network",
		},
		PrivateKey: privateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := VerifyBlackboxEnvelope(envelope, BlackboxVerifyOptions{
		Pins: BlackboxTrustPins{
			KeyID: "fixture-key", PublicKey: publicKey,
			TrustRoot: "fixture-root", Graph: imported.Graph, Network: "fixture-network",
		},
		Now:            now,
		LastGeneration: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Categories["protected_path"] || len(snapshot.Paths) != 2 {
		t.Fatalf("accepted snapshot carries %v and %d path rules", snapshot.Categories, len(snapshot.Paths))
	}
	workspace := blackboxTestWorkspaceID()
	refusedAction := BlackboxAction{
		Schema: 1, Host: "test", Phase: "pre", Tool: "write", Kind: "file",
		WorkspaceID: workspace, ParseStatus: "parsed",
		Paths: []string{"/srv/app/secrets/token"},
	}
	decision := DecideBlackboxEnforcement("pre", EvaluateBlackbox(context.Background(), refusedAction, snapshot), nil, workspace, now)
	if !decision.Refuse || decision.Disposition != "blocked" {
		t.Fatalf("decision = %+v, want a refusal on the protected path", decision)
	}
	if !strings.HasSuffix(decision.Reason, "blackbox_rule_match:path:secrets") {
		t.Fatalf("reason is %q", decision.Reason)
	}
	ordinaryAction := BlackboxAction{
		Schema: 1, Host: "test", Phase: "pre", Tool: "write", Kind: "file",
		WorkspaceID: workspace, ParseStatus: "parsed",
		Paths: []string{"src/main.go"},
	}
	ordinary := DecideBlackboxEnforcement("pre", EvaluateBlackbox(context.Background(), ordinaryAction, snapshot), nil, workspace, now)
	if ordinary.Refuse {
		t.Fatalf("decision = %+v, an ordinary workspace path must not refuse", ordinary)
	}
	unknownTool := BlackboxAction{
		Schema: 1, Host: "test", Phase: "pre", Tool: "mystery", Kind: "unknown",
		WorkspaceID: workspace, ParseStatus: "unsupported",
	}
	failedOpen := DecideBlackboxEnforcement("pre", EvaluateBlackbox(context.Background(), unknownTool, snapshot), nil, workspace, now)
	if !failedOpen.Refuse || failedOpen.Disposition != "security_check_unavailable" {
		t.Fatalf("decision = %+v, a required check that cannot run must fail closed", failedOpen)
	}
}

// blackboxTestWorkspaceID returns a workspace id the override matcher accepts.
func blackboxTestWorkspaceID() string {
	return "ws_0123456789abcdef"
}

func BenchmarkDecodeBlackboxPathPolicy(b *testing.B) {
	body := []byte(pathPolicyBody(BlackboxPathPolicyMaxRules))
	policy, err := DecodeBlackboxPathPolicy(bytes.NewReader(body))
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := DecodeBlackboxPathPolicy(bytes.NewReader(body)); err != nil {
			b.Fatal(err)
		}
	}
	if len(policy) != BlackboxPathPolicyMaxRules {
		b.Fatalf("carries %d rules", len(policy))
	}
}
