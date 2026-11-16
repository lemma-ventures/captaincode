package captaincode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func upstreamFixtureBytes(t testing.TB) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/blackbox/upstream_ruleset.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func upstreamImportFixture(t testing.TB) BlackboxUpstreamImport {
	t.Helper()
	options := BlackboxImportOptions{
		SnapshotID: "upstream-7d697683-1",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
	}
	imported, err := ImportBlackboxUpstreamCache(bytes.NewReader(upstreamFixtureBytes(t)), options)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return imported
}

func mutateFixture(t testing.TB, mutate func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(upstreamFixtureBytes(t), &document); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	mutate(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return encoded
}

func TestImportBlackboxUpstreamCacheMapsSelectedRules(t *testing.T) {
	imported := upstreamImportFixture(t)
	snapshot := imported.Snapshot
	if snapshot.Source != BlackboxExportSource {
		t.Fatalf("source is %q", snapshot.Source)
	}
	if snapshot.Revision != "7d697683e0d56608cfe3fee374d6a6866702058a" {
		t.Fatalf("revision is %q", snapshot.Revision)
	}
	if err := validateBlackboxSnapshot(&snapshot, BlackboxExportSource); err != nil {
		t.Fatalf("imported rules do not validate: %v", err)
	}
	if len(snapshot.Packages) != 2 {
		t.Fatalf("carries %d package rules, want 2", len(snapshot.Packages))
	}
	if snapshot.Packages[0].ID != "dep:npm:evil-pkg@1.2.3" || snapshot.Packages[1].ID != "dep:pypi:dropper@0.1.0" {
		t.Fatalf("package rules are %q and %q", snapshot.Packages[0].ID, snapshot.Packages[1].ID)
	}
	if snapshot.Packages[0].Kind != "malware" || snapshot.Packages[0].Severity != "high" {
		t.Fatalf("first package rule carries %q/%q", snapshot.Packages[0].Kind, snapshot.Packages[0].Severity)
	}
	if len(snapshot.Patterns) != 1 || snapshot.Patterns[0].ID != "injection:simple" {
		t.Fatalf("carries %d pattern rules: %+v", len(snapshot.Patterns), snapshot.Patterns)
	}
	if snapshot.Patterns[0].Kind != "heuristic" || snapshot.Patterns[0].Category != "pattern" {
		t.Fatalf("pattern rule carries %q/%q", snapshot.Patterns[0].Kind, snapshot.Patterns[0].Category)
	}
	if len(snapshot.Paths) != 0 {
		t.Fatalf("carries %d path rules, want none", len(snapshot.Paths))
	}
	if snapshot.Categories["protected_path"] {
		t.Fatal("protected_path is enabled without rules")
	}
	if !snapshot.Categories["secret_exposure"] || !snapshot.Categories["dependency"] || !snapshot.Categories["pattern"] {
		t.Fatalf("categories are %v", snapshot.Categories)
	}
	wantOmitted := []string{
		"dependency:dep:go:vendored@1.0.0",
		"dependency:dep:npm:unknown-kind@2.0.0",
		"fileaccess",
		"ioc",
		"pattern:injection:python-only",
	}
	if fmt.Sprint(imported.Omitted) != fmt.Sprint(wantOmitted) {
		t.Fatalf("omitted is %v, want %v", imported.Omitted, wantOmitted)
	}
	wantComplete := []string{"secret_exposure"}
	if fmt.Sprint(imported.CompleteCategories) != fmt.Sprint(wantComplete) {
		t.Fatalf("complete categories are %v, want %v", imported.CompleteCategories, wantComplete)
	}
	wantRevocations := []string{"dep:npm:old-pkg@9.9.9", "ioc:domain:withdrawn.invalid"}
	if fmt.Sprint(imported.Revocations) != fmt.Sprint(wantRevocations) {
		t.Fatalf("revocations are %v, want %v", imported.Revocations, wantRevocations)
	}
	if imported.Graph != "urn:graph:demo" {
		t.Fatalf("graph is %q", imported.Graph)
	}
	if imported.SyncedAt.Unix() != 1763300000 {
		t.Fatalf("synced at %s", imported.SyncedAt.UTC().Format(time.RFC3339))
	}
}

func TestImportBlackboxUpstreamCacheCompleteScopeClaimsCompleteness(t *testing.T) {
	body := mutateFixture(t, func(document map[string]any) {
		delete(document, "fileaccess")
		delete(document, "ioc")
		dependency := document["dependency"].(map[string]any)
		dependency["go:vendored@1.0.0"].(map[string]any)["ecosystem"] = "npm"
		unknown := dependency["npm:unknown-kind@2.0.0"].(map[string]any)
		unknown["kind"] = "typosquat"
		unknown["source"] = "public"
		injection := document["injection"].([]any)
		injection[1].(map[string]any)["pattern_src"] = "rm\\s+-rf"
	})
	imported, err := ImportBlackboxUpstreamCache(bytes.NewReader(body), BlackboxImportOptions{
		SnapshotID: "upstream-clean-1",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(imported.Snapshot.Packages) != 4 || len(imported.Snapshot.Patterns) != 2 {
		t.Fatalf("carries %d package and %d pattern rules", len(imported.Snapshot.Packages), len(imported.Snapshot.Patterns))
	}
	wantComplete := []string{"dependency", "pattern", "secret_exposure"}
	if fmt.Sprint(imported.CompleteCategories) != fmt.Sprint(wantComplete) {
		t.Fatalf("complete categories are %v, want %v", imported.CompleteCategories, wantComplete)
	}
	if len(imported.Omitted) != 0 {
		t.Fatalf("omitted is %v, want none", imported.Omitted)
	}
}

func TestImportBlackboxUpstreamCacheDropsRevokedAndDuplicateRules(t *testing.T) {
	body := mutateFixture(t, func(document map[string]any) {
		dependency := document["dependency"].(map[string]any)
		dependency["npm:old-pkg@9.9.9"] = map[string]any{
			"identifier": "dep:npm:old-pkg@9.9.9", "subject": "", "description": "", "severity": "high",
			"name": "old-pkg", "source": "public", "ecosystem": "npm", "packageName": "old-pkg",
			"packageVersion": "9.9.9", "advisoryId": nil, "kind": "malware",
		}
		dependency["npm:evil-pkg@1.2.3:copy"] = map[string]any{
			"identifier": "dep:npm:evil-pkg@1.2.3", "subject": "", "description": "", "severity": "high",
			"name": "evil-pkg", "source": "public", "ecosystem": "npm", "packageName": "evil-pkg",
			"packageVersion": "1.2.3", "advisoryId": nil, "kind": "malware",
		}
	})
	imported, err := ImportBlackboxUpstreamCache(bytes.NewReader(body), BlackboxImportOptions{
		SnapshotID: "upstream-revoked-1",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	for _, rule := range imported.Snapshot.Packages {
		if rule.ID == "dep:npm:old-pkg@9.9.9" {
			t.Fatal("a revoked rule survived the import")
		}
	}
	if len(imported.Snapshot.Packages) != 2 {
		t.Fatalf("carries %d package rules, want 2", len(imported.Snapshot.Packages))
	}
	if fmt.Sprint(imported.Omitted) != fmt.Sprint([]string{
		"dependency:dep:go:vendored@1.0.0",
		"dependency:dep:npm:evil-pkg@1.2.3",
		"dependency:dep:npm:unknown-kind@2.0.0",
		"fileaccess",
		"ioc",
		"pattern:injection:python-only",
	}) {
		t.Fatalf("omitted is %v", imported.Omitted)
	}
}

func TestImportBlackboxUpstreamCacheRecordsUnsafeIdentifiers(t *testing.T) {
	body := mutateFixture(t, func(document map[string]any) {
		dependency := document["dependency"].(map[string]any)
		row := dependency["npm:evil-pkg@1.2.3"].(map[string]any)
		row["identifier"] = "dep:npm:evil-pkg@1.2.3\nrogue"
	})
	imported, err := ImportBlackboxUpstreamCache(bytes.NewReader(body), BlackboxImportOptions{
		SnapshotID: "upstream-unsafe-1",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	for _, rule := range imported.Snapshot.Packages {
		if strings.ContainsAny(rule.ID, "\n\r\t") {
			t.Fatalf("an unsafe identifier reached the rules: %q", rule.ID)
		}
	}
	if fmt.Sprint(imported.Omitted) != fmt.Sprint([]string{
		"dependency:dep:go:vendored@1.0.0",
		"dependency:dep:npm:unknown-kind@2.0.0",
		"dependency:unsafe-id",
		"fileaccess",
		"ioc",
		"pattern:injection:python-only",
	}) {
		t.Fatalf("omitted is %v", imported.Omitted)
	}
}

func TestImportBlackboxUpstreamCacheRejections(t *testing.T) {
	options := func() BlackboxImportOptions {
		return BlackboxImportOptions{
			SnapshotID: "upstream-7d697683-1",
			Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
		}
	}
	oversized := append([]byte(strings.Repeat(" ", BlackboxUpstreamMaxCacheBytes+1)), []byte("{}")...)
	trailing := append(bytes.TrimSpace(upstreamFixtureBytes(t)), []byte("{}")...)
	cases := []struct {
		name   string
		body   []byte
		handle func(*BlackboxImportOptions)
	}{
		{name: "oversized cache", body: oversized},
		{name: "unknown field", body: unknownFieldBody(t)},
		{name: "trailing data", body: trailing},
		{name: "empty snapshot id", body: upstreamFixtureBytes(t), handle: func(o *BlackboxImportOptions) { o.SnapshotID = "" }},
		{name: "empty revision", body: upstreamFixtureBytes(t), handle: func(o *BlackboxImportOptions) { o.Revision = " " }},
		{
			name: "no graph",
			body: mutateFixture(t, func(document map[string]any) {
				document["context_graph_id"] = ""
			}),
		},
		{
			name:   "rule limit",
			body:   upstreamFixtureBytes(t),
			handle: func(o *BlackboxImportOptions) { o.MaxRules = 2 },
		},
		{
			name: "wrong type",
			body: mutateFixture(t, func(document map[string]any) {
				document["curator_revoked"] = "dep:npm:old-pkg@9.9.9"
			}),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			importOptions := options()
			if testCase.handle != nil {
				testCase.handle(&importOptions)
			}
			if _, err := ImportBlackboxUpstreamCache(bytes.NewReader(testCase.body), importOptions); err == nil {
				t.Fatal("import was accepted")
			}
		})
	}
}

func unknownFieldBody(t testing.TB) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(upstreamFixtureBytes(t), &document); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	document["surprising_key"] = 1
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return encoded
}

func TestImportBlackboxUpstreamCacheBoundsReader(t *testing.T) {
	reader := io.MultiReader(bytes.NewReader(upstreamFixtureBytes(t)), &infiniteReader{})
	if _, err := ImportBlackboxUpstreamCache(reader, BlackboxImportOptions{
		SnapshotID: "upstream-7d697683-1",
		Revision:   "7d697683e0d56608cfe3fee374d6a6866702058a",
	}); err == nil {
		t.Fatal("an unbounded reader was accepted")
	}
}

type infiniteReader struct{}

func (infiniteReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = ' '
	}
	return len(buffer), nil
}

func BenchmarkImportBlackboxUpstreamCache(b *testing.B) {
	body, err := os.ReadFile("testdata/blackbox/upstream_ruleset.json")
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	options := BlackboxImportOptions{SnapshotID: "upstream-7d697683-1", Revision: "7d697683e0d56608cfe3fee374d6a6866702058a"}
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := ImportBlackboxUpstreamCache(bytes.NewReader(body), options); err != nil {
			b.Fatalf("import: %v", err)
		}
	}
}
