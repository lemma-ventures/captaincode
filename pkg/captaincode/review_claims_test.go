package captaincode

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const testCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func claimTestEvidence() ReviewEvidence {
	contrib := strings.Join([]string{
		"4. Review every rule here.",
		"5. Do not restate the diff.",
		"6. Explain *why* in the commit message. The history here is the design record;",
		"   a message that only restates the diff is a missed opportunity.",
		"",
		"## Style",
		"",
		"Comments explain the reason, not the mechanism - preferably the incident that",
		"caused the line to exist. Follow what the surrounding file already does.",
		"",
	}, "\n")
	legs := strings.Join([]string{
		"package captaincode",
		"",
		"\td.Timeout = base",
		"\td.Ceiling = ceil",
		"",
		"\t\td.Timeout = workerTimeout()",
		"\t\tres, err = d.Run(leg, task)",
		"",
	}, "\n")
	return ReviewEvidence{
		Version:       1,
		Commit:        testCommit,
		ArchiveSHA256: strings.Repeat("b", 64),
		Scope:         []string{},
		Files: []ReviewFile{
			{Path: "root/CONTRIBUTING.md", SHA256: strings.Repeat("c", 64), Text: contrib},
			{Path: "root/pkg/legs.go", SHA256: strings.Repeat("d", 64), Text: legs},
		},
		Excluded: []ReviewExclusion{{Path: "root/blob.bin", Reason: "binary_or_invalid_utf8"}},
	}
}

func claimSet(commit string, claims ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"version": 1, "declared_commit": commit, "claims": claims})
	return b
}

func claim(id, typ, path string, start, end int, quote string) map[string]any {
	return map[string]any{"id": id, "type": typ, "statement": "fixture statement " + id, "path": path, "start": start, "end": end, "quote": quote}
}

func checkResults(t *testing.T, log ClaimCheckLog) map[string]ClaimResult {
	t.Helper()
	m := map[string]ClaimResult{}
	for _, r := range log.Results {
		if _, dup := m[r.ID]; dup {
			t.Fatalf("duplicate result id %s", r.ID)
		}
		m[r.ID] = r
	}
	return m
}

func TestClaimCheckVerifiesExactCitations(t *testing.T) {
	ev := claimTestEvidence()
	claims := claimSet(testCommit,
		claim("C01", "fact", "CONTRIBUTING.md", 3, 3, "The history here is the design record;"),
		claim("C02", "fact", "CONTRIBUTING.md", 3, 4, "The history here is the design record;\n   a message that only restates the diff is a missed opportunity."),
		claim("C03", "fact", "CONTRIBUTING.md", 3, 4, "Comments explain the reason, not the mechanism"),
		claim("C04", "fact", "tiers.go", 1, 1, "anything"),
		claim("C05", "fact", "blob.bin", 1, 1, "anything"),
		claim("C06", "fact", "CONTRIBUTING.md", 3, 99, "The history here is the design record;"),
		claim("C07", "fact", "pkg/legs.go", 3, 4, "d.Ceiling = ceil"),
		claim("C08", "fact", "../escape.go", 1, 1, "anything"),
		claim("C09", "grade", "CONTRIBUTING.md", 3, 3, "The history here is the design record;"),
		claim("C10", "fact", "CONTRIBUTING.md", 0, 3, "The history here is the design record;"),
		claim("C11", "fact", "CONTRIBUTING.md", 3, 3, ""),
		claim("C12", "fact", "CONTRIBUTING.md", 3, 3, "The history here is the design record;"),
		claim("C13", "fact", "root/pkg/legs.go", 4, 4, "d.Ceiling = ceil"),
	)
	log, err := CheckClaims(ev, claims)
	if err != nil {
		t.Fatal(err)
	}
	r := checkResults(t, log)
	expect := map[string]struct{ result, reason string }{
		"C01": {"present", ""},
		"C02": {"present", ""},
		"C03": {"dropped", "quote_not_found"},
		"C04": {"dropped", "unknown_path"},
		"C05": {"dropped", "unknown_path"},
		"C06": {"dropped", "line_range_invalid"},
		"C07": {"present", ""},
		"C08": {"dropped", "invalid_claim"},
		"C09": {"dropped", "invalid_claim"},
		"C10": {"dropped", "invalid_claim"},
		"C11": {"dropped", "invalid_claim"},
		"C12": {"present", ""},
		"C13": {"present", ""},
	}
	if len(r) != len(expect) {
		t.Fatalf("got %d results, want %d: %+v", len(r), len(expect), log.Results)
	}
	for id, want := range expect {
		got, ok := r[id]
		if !ok {
			t.Fatalf("missing result %s", id)
		}
		if got.Result != want.result || got.Reason != want.reason {
			t.Errorf("%s: got %s/%s want %s/%s", id, got.Result, got.Reason, want.result, want.reason)
		}
	}
	if len(log.Results) != 13 || log.Results[0].ID != "C01" || log.Results[12].ID != "C13" {
		t.Fatal("input order not preserved")
	}
	if log.Counts.Present != 5 || log.Counts.Dropped != 8 {
		t.Fatalf("bad counts %+v", log.Counts)
	}
	if log.ArchiveSHA256 != ev.ArchiveSHA256 || log.DeclaredCommit != testCommit || len(log.ClaimsSHA256) != 64 {
		t.Fatalf("log not bound to evidence and claims: %+v", log)
	}
	again, err := CheckClaims(ev, claims)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(t, log)) != string(mustJSON(t, again)) {
		t.Fatal("check is not deterministic")
	}
}

func TestClaimCheckRejectsInvalidClaimSets(t *testing.T) {
	ev := claimTestEvidence()
	for name, raw := range map[string][]byte{
		"malformed":     []byte("{"),
		"unknown field": []byte(`{"version":1,"declared_commit":"` + testCommit + `","claims":[],"extra":true}`),
		"wrong version": []byte(`{"version":2,"declared_commit":"` + testCommit + `","claims":[]}`),
		"bad commit":    []byte(`{"version":1,"declared_commit":"main","claims":[]}`),
		"empty claims":  claimSet(testCommit),
		"duplicate ids": claimSet(testCommit, claim("C01", "fact", "CONTRIBUTING.md", 3, 3, "x"), claim("C01", "fact", "CONTRIBUTING.md", 3, 3, "x")),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CheckClaims(ev, raw); err == nil {
				t.Fatal("invalid claim set accepted")
			}
		})
	}
	var many []map[string]any
	for i := 0; i < 101; i++ {
		many = append(many, claim(fmt.Sprintf("C%03d", i), "fact", "CONTRIBUTING.md", 3, 3, "The history here is the design record;"))
	}
	if _, err := CheckClaims(ev, claimSet(testCommit, many...)); err == nil {
		t.Fatal("claim count limit ignored")
	}
	mismatch := claimSet(strings.Repeat("b", 40), claim("C01", "fact", "CONTRIBUTING.md", 3, 3, "The history here is the design record;"))
	if _, err := CheckClaims(ev, mismatch); err == nil {
		t.Fatal("commit mismatch accepted")
	}
	if _, err := CheckClaims(ReviewEvidence{Version: 2, Commit: testCommit}, claimSet(testCommit, claim("C01", "fact", "a", 1, 1, "a"))); err == nil {
		t.Fatal("invalid evidence accepted")
	}
	if _, err := CheckClaims(ReviewEvidence{Version: 1, Commit: testCommit}, claimSet(testCommit, claim("C01", "fact", "a", 1, 1, "a"))); err == nil {
		t.Fatal("empty evidence accepted")
	}
}

func TestClaimCheckNormalizesLineEndings(t *testing.T) {
	ev := claimTestEvidence()
	ev.Files = []ReviewFile{{Path: "root/README.md", Text: "line one\r\nline two\r\n"}}
	raw := claimSet(testCommit, claim("C01", "fact", "README.md", 1, 2, "line one\nline two"))
	log, err := CheckClaims(ev, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Results) != 1 || log.Results[0].Result != "present" {
		t.Fatalf("line-ending normalization failed: %+v", log.Results)
	}
}

func TestClaimCheckBoundsLongFields(t *testing.T) {
	ev := claimTestEvidence()
	long := strings.Repeat("q", 8192)
	if _, err := CheckClaims(ev, claimSet(testCommit, claim("C01", "fact", "CONTRIBUTING.md", 6, 6, long))); err != nil {
		t.Fatalf("oversized quote must drop the claim, got error: %v", err)
	}
	hugeStatement := strings.Repeat("s", 4096)
	raw := claimSet(testCommit, map[string]any{"id": "C01", "type": "fact", "statement": hugeStatement, "path": "CONTRIBUTING.md", "start": 6, "end": 6, "quote": "The history here is the design record;"})
	log, err := CheckClaims(ev, raw)
	if err != nil {
		t.Fatal(err)
	}
	if log.Results[0].Result != "dropped" || log.Results[0].Reason != "invalid_claim" {
		t.Fatalf("oversized statement accepted: %+v", log.Results[0])
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
