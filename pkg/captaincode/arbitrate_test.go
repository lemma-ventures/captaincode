package captaincode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveKeepsWinnerAndUncontested(t *testing.T) {
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{
		{Leg: "claude", Worker: "w1-claude", ChangedFiles: []string{"a.go", "b.go"}},
		{Leg: "codex", Worker: "w2-codex", ChangedFiles: []string{"b.go"}},
		{Leg: "glm", Worker: "w3-glm", ChangedFiles: []string{"docs.md"}},
	})
	if got := ic.Contested(); strings.Join(got, ",") != "w1-claude,w2-codex" {
		t.Fatalf("contested = %v", got)
	}
	r, err := ic.Resolve("w2-codex", "tests pass")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != IntegrationResolved || r.Winner != "w2-codex" || r.Ruling != "tests pass" {
		t.Fatalf("resolved = %+v", r)
	}
	if strings.Join(r.Dropped, ",") != "w1-claude" {
		t.Fatalf("dropped = %v, want only the losing contender (w3 touched nothing contested)", r.Dropped)
	}
	if !strings.Contains(r.Summary(), "settled on w2-codex") {
		t.Fatalf("summary = %q", r.Summary())
	}
}

func TestResolveRefusesANonContender(t *testing.T) {
	ic := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{
		{Leg: "claude", Worker: "w1-claude", ChangedFiles: []string{"a.go"}},
		{Leg: "codex", Worker: "w2-codex", ChangedFiles: []string{"a.go"}},
		{Leg: "glm", Worker: "w3-glm", ChangedFiles: []string{"docs.md"}},
	})
	for _, bad := range []string{"w3-glm", "grok", ""} {
		if _, err := ic.Resolve(bad, ""); err == nil {
			t.Fatalf("Resolve(%q) accepted a worker that was not in the conflict", bad)
		}
	}
	clean := BuildIntegrationCandidate("t1", "s1", "rev", []PatchManifest{{Leg: "claude", ChangedFiles: []string{"a.go"}}})
	if _, err := clean.Resolve("claude", ""); err == nil {
		t.Fatal("a clean candidate has nothing to resolve")
	}
}

// Two workers edit the same file; the ruling lands the winner's version
// whole, never a splice, and the loser's untouched files stay untouched.
func TestApplyResolvedLandsOnlyTheWinner(t *testing.T) {
	repo, rev := artifactFixtureRepo(t)
	edit := func(files map[string]string) string {
		wt, err := NewWorktree(context.Background(), repo, rev)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { wt.Close() })
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(wt.Dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return wt.Dir
	}
	diffDir := t.TempDir()
	capture := func(dir, leg, worker string) PatchManifest {
		m, err := CaptureManifest(context.Background(), dir, diffDir, rev, "t1", "s1", "", leg)
		if err != nil {
			t.Fatal(err)
		}
		m.Worker = worker
		return m
	}
	mA := capture(edit(map[string]string{"a.txt": "claude's alpha\n", "claude-only.txt": "c\n"}), "claude", "w1-claude")
	mB := capture(edit(map[string]string{"a.txt": "codex's alpha\n"}), "codex", "w2-codex")
	mC := capture(edit(map[string]string{"b.txt": "glm's beta\n"}), "glm", "w3-glm")
	ic := BuildIntegrationCandidate("t1", "s1", rev, []PatchManifest{mA, mB, mC})
	if ic.Status != IntegrationConflicted {
		t.Fatalf("status = %s", ic.Status)
	}
	resolved, err := ic.Resolve("w2-codex", "smaller change")
	if err != nil {
		t.Fatal(err)
	}
	target, _ := artifactFixtureRepo(t)
	if err := ApplyIntegrationCandidate(context.Background(), target, resolved); err != nil {
		t.Fatalf("apply: %v", err)
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(target, name))
		return string(b)
	}
	if got := read("a.txt"); got != "codex's alpha\n" {
		t.Fatalf("a.txt = %q, want the winner's version", got)
	}
	if got := read("b.txt"); got != "glm's beta\n" {
		t.Fatalf("b.txt = %q, an uncontested worker's change should land", got)
	}
	if _, err := os.Stat(filepath.Join(target, "claude-only.txt")); !os.IsNotExist(err) {
		t.Fatal("the losing worker's changes were applied in part")
	}
}

func TestArbitrationPromptAndRuling(t *testing.T) {
	contenders := map[string]Contender{
		"w2-codex":  {Leg: LegCodex, Text: "rewrote the parser", Files: []string{"parse.go"}, Evidence: "tests passed"},
		"w1-claude": {Leg: LegClaude, Text: "patched the lexer", Files: []string{"parse.go", "lex.go"}},
	}
	p := arbitrationPrompt("fix the parser", contenders)
	for _, want := range []string{"not merged", `worker "w1-claude"`, `worker "w2-codex"`, "evidence: tests passed", "parse.go, lex.go", "fix the parser"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	for _, want := range []string{"## 5. One whole winner per group", "Objective evidence outranks a confident report", `{"winner"`} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing the skill's arbitration step (%q):\n%s", want, p)
		}
	}
	if strings.Contains(p, "## 6.") {
		t.Fatal("the prompt should carry section 5 alone, not the rest of the skill")
	}
	if strings.Index(p, "w1-claude") > strings.Index(p, "w2-codex") {
		t.Fatal("contenders should be listed in a stable order")
	}
	r, err := checkRuling(Ruling{Winner: " w2-codex ", Reason: "tests pass\nsmaller diff"}, contenders)
	if err != nil || r.Winner != "w2-codex" || r.Reason != "tests pass smaller diff" {
		t.Fatalf("ruling = %+v, %v", r, err)
	}
	if _, err := checkRuling(Ruling{Winner: "codex"}, contenders); err == nil {
		t.Fatal("a ruling naming a leg instead of a worker id must be refused")
	}
	if _, err := (Manager{}).Arbitrate("x", map[string]Contender{"w1": {}}); err == nil {
		t.Fatal("one contender is not a conflict")
	}
}

func TestApplyChecksEveryDiffBeforeWriting(t *testing.T) {
	repo, rev := artifactFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wt.Close() })
	if err := os.WriteFile(filepath.Join(wt.Dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := CaptureManifest(context.Background(), wt.Dir, t.TempDir(), rev, "t1", "s1", "", "claude")
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.diff")
	if err := os.WriteFile(bad, []byte("not a diff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ic := BuildIntegrationCandidate("t1", "s1", rev, []PatchManifest{
		m,
		{Leg: "codex", Worker: "w2", ChangedFiles: []string{"b.txt"}, DiffPath: bad},
	})
	if ic.Status != IntegrationClean {
		t.Fatalf("status = %s", ic.Status)
	}
	target, _ := artifactFixtureRepo(t)
	if err := ApplyIntegrationCandidate(context.Background(), target, ic); err == nil {
		t.Fatal("a diff that does not apply should refuse the candidate")
	}
	got, err := os.ReadFile(filepath.Join(target, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\n" {
		t.Fatalf("a.txt = %q; a later bad diff must leave earlier files unwritten", got)
	}
}

// The director's arbitration step is embedded from a copy of the published
// skill. A copy that drifted would have the director follow rules the
// published skill no longer states.
func TestLandParallelSkillMatchesThePublishedOne(t *testing.T) {
	published, err := os.ReadFile(filepath.Join("..", "..", "skills", "land-parallel-agent-work", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != landParallelSkill {
		t.Fatal("pkg/captaincode/skills/land_parallel_agent_work.md drifted from skills/land-parallel-agent-work/SKILL.md - copy it over")
	}
}
