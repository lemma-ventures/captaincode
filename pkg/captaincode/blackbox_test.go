package captaincode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func blackboxFixtureSnapshot() BlackboxSnapshot {
	return BlackboxSnapshot{
		Schema: 1, ID: "fixture", Source: "synthetic", Revision: "rev-1",
		Categories: map[string]bool{"dependency": true, "protected_path": true, "secret_exposure": true, "pattern": true},
		Packages: []BlackboxPackageRule{
			{ID: "pkg-exact", Ecosystem: "npm", Name: "example-package", Version: "1.2.3", Kind: "known_malware", Severity: "critical"},
			{ID: "pkg-all", Ecosystem: "pypi", Name: "example-python", Version: "*", Kind: "known_malware", Severity: "critical"},
		},
		Paths:    []BlackboxPathRule{{ID: "path-exact", Path: "config/private/**", Kind: "protected_path", Severity: "critical"}},
		Patterns: []BlackboxPatternRule{{ID: "pattern-one", Category: "pattern", Pattern: `dangerous-example`, Kind: "heuristic", Severity: "warning"}},
	}
}

func blackboxFixtureAction() BlackboxAction {
	return BlackboxAction{Schema: 1, ID: "action-1", Host: "opencode", Phase: "pre", Tool: "bash", Kind: "shell", ParseStatus: "parsed"}
}

func TestBlackboxExactPackagesAndBenignVersions(t *testing.T) {
	snapshot := blackboxFixtureSnapshot()
	for _, test := range []struct {
		name, ecosystem, pkg, version, rule string
		find                                bool
	}{
		{"exact", "npm", "example-package", "1.2.3", "pkg-exact", true},
		{"other version", "npm", "example-package", "1.2.4", "", false},
		{"all versions", "pypi", "example-python", "", "pkg-all", true},
		{"ecosystem", "cargo", "example-package", "1.2.3", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			action := blackboxFixtureAction()
			action.Packages = []BlackboxPackage{{Ecosystem: test.ecosystem, Name: test.pkg, Version: test.version}}
			result := EvaluateBlackbox(context.Background(), action, snapshot)
			if result.Status != "complete" {
				t.Fatalf("status = %s: %+v", result.Status, result.Coverage)
			}
			if test.find && (len(result.Findings) != 1 || result.Findings[0].RuleID != test.rule) {
				t.Fatalf("findings = %+v", result.Findings)
			}
			if !test.find && len(result.Findings) != 0 {
				t.Fatalf("findings = %+v", result.Findings)
			}
		})
	}
}

func TestBlackboxProtectedPathsAndSecretWarnings(t *testing.T) {
	action := blackboxFixtureAction()
	action.Kind = "file"
	action.Tool = "write"
	action.Paths = []string{"./config/private/nested/token.json"}
	action.Text = "sk-test-1234567890abcdefghijkl"
	result := EvaluateBlackbox(context.Background(), action, blackboxFixtureSnapshot())
	if len(result.Findings) != 2 {
		t.Fatalf("findings = %+v", result.Findings)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("sk-test-1234567890abcdefghijkl")) || bytes.Contains(encoded, []byte("token.json")) {
		t.Fatalf("result exposes action data: %s", encoded)
	}
}

func TestBlackboxDisabledCategoryProducesNoFinding(t *testing.T) {
	snapshot := blackboxFixtureSnapshot()
	snapshot.Categories["dependency"] = false
	action := blackboxFixtureAction()
	action.Packages = []BlackboxPackage{{Ecosystem: "npm", Name: "example-package", Version: "1.2.3"}}
	result := EvaluateBlackbox(context.Background(), action, snapshot)
	if len(result.Findings) != 0 || result.Status != "incomplete" {
		t.Fatalf("result = %+v", result)
	}
}

func TestBlackboxIncompleteInputIsNotClean(t *testing.T) {
	for _, action := range []BlackboxAction{
		{Schema: 1, Tool: "unknown", Kind: "unknown", Phase: "pre", ParseStatus: "parsed"},
		{Schema: 1, Tool: "read", Kind: "file", Phase: "pre", ParseStatus: "parsed", Truncated: true},
		{Schema: 1, Tool: "read", Kind: "file", Phase: "pre", ParseStatus: "unsupported"},
		{Schema: 1, Tool: "read", Kind: "file", Phase: "pre", ParseStatus: "parsed", OmittedFields: []string{"patch"}},
	} {
		result := EvaluateBlackbox(context.Background(), action, blackboxFixtureSnapshot())
		if result.Status != "incomplete" {
			t.Fatalf("status = %s for %+v", result.Status, action)
		}
	}
}

func TestBlackboxDecodeRejectsUnsupportedAndOversizedInput(t *testing.T) {
	snapshot := blackboxFixtureSnapshot()
	snapshot.Patterns[0].Pattern = `(?=lookahead)`
	body, _ := json.Marshal(snapshot)
	if _, err := DecodeBlackboxSnapshot(bytes.NewReader(body)); err == nil || !strings.Contains(err.Error(), "unsupported pattern") {
		t.Fatalf("error = %v", err)
	}
	if _, err := DecodeBlackboxSnapshot(bytes.NewReader(append(body, []byte(` {}`)...))); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	if _, err := DecodeBlackboxAction(strings.NewReader(strings.Repeat(" ", BlackboxMaxActionBytes+1))); err == nil {
		t.Fatal("accepted oversized action")
	}
}

func TestBlackboxDecodeComputesSnapshotIdentity(t *testing.T) {
	body, _ := json.Marshal(blackboxFixtureSnapshot())
	snapshot, err := DecodeBlackboxSnapshot(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Digest) != 64 || snapshot.Source != "synthetic" {
		t.Fatalf("snapshot identity = %+v", snapshot)
	}
	result := EvaluateBlackbox(context.Background(), blackboxFixtureAction(), snapshot)
	if result.SnapshotDigest != snapshot.Digest {
		t.Fatalf("result digest = %q", result.SnapshotDigest)
	}
}

func TestBlackboxNormalizeHostMappings(t *testing.T) {
	body, err := os.ReadFile("testdata/blackbox/parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		UpstreamRevision     string   `json:"upstream_revision"`
		ReferenceDifferences []string `json:"reference_differences"`
		Cases                []struct {
			Name       string          `json:"name"`
			Host       string          `json:"host"`
			Tool       string          `json:"tool"`
			Input      json.RawMessage `json:"input"`
			Kind       string          `json:"kind"`
			Paths      int             `json:"paths"`
			Packages   int             `json:"packages"`
			Incomplete bool            `json:"incomplete"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.UpstreamRevision != "7d697683e0d56608cfe3fee374d6a6866702058a" || len(fixture.ReferenceDifferences) != 4 || len(fixture.Cases) != 11 {
		t.Fatalf("incomplete parity fixture: %+v", fixture)
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			action, gaps := NormalizeBlackboxAction(test.Host, "pre", test.Tool, test.Input)
			if action.Kind != test.Kind || len(action.Paths) != test.Paths || len(action.Packages) != test.Packages {
				t.Fatalf("action = %+v", action)
			}
			if (len(gaps) != 0 || action.ParseStatus != "parsed") != test.Incomplete {
				t.Fatalf("gaps = %v, parse = %s", gaps, action.ParseStatus)
			}
		})
	}
}

func TestBlackboxCancellationAndBenchmarkBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := EvaluateBlackbox(ctx, blackboxFixtureAction(), blackboxFixtureSnapshot())
	if result.Status != "error" || len(result.Errors) == 0 || result.Errors[0] != "evaluation_cancelled" {
		t.Fatalf("result = %+v", result)
	}
	started := time.Now()
	result = EvaluateBlackbox(context.Background(), blackboxFixtureAction(), blackboxFixtureSnapshot())
	if result.Status != "complete" || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("bounded evaluation = %+v, %s", result, time.Since(started))
	}
}

func BenchmarkBlackboxEvaluate(b *testing.B) {
	snapshot := blackboxFixtureSnapshot()
	for i := len(snapshot.Packages); i < 1024; i++ {
		snapshot.Packages = append(snapshot.Packages, BlackboxPackageRule{ID: "pkg", Ecosystem: "npm", Name: "benign", Version: "1.0.0", Kind: "known_malware", Severity: "critical"})
	}
	action := blackboxFixtureAction()
	for i := 0; i < 32; i++ {
		action.Packages = append(action.Packages, BlackboxPackage{Ecosystem: "npm", Name: "example-package", Version: "1.2.4"})
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		EvaluateBlackbox(context.Background(), action, snapshot)
	}
}
