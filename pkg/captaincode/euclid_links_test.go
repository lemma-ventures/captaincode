package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MM38 E3–E4: links between repos, linked brains in the read set (read-only,
// weighted), proposals from manifests and the journal, search across brains
// with source tags, other developers' subtrees search-only.

func TestParseLinksBlock(t *testing.T) {
	yml := "roots:\n  - docs\nlinks:\n  depends_on:\n    - lemma\n    - github.com/lemma-ventures/euclid\n  related: [Compliance, \"DLM\"]\nother:\n  depends_on:\n    - not-a-link\n"
	links := parseLinksBlock(yml)
	assert.Equal(t, []Link{{To: "lemma", Kind: "depends_on"}, {To: "github.com/lemma-ventures/euclid", Kind: "depends_on"}, {To: "Compliance", Kind: "related"}, {To: "DLM", Kind: "related"}}, links)
	assert.Empty(t, parseLinksBlock("roots:\n  - docs\n"))
}

func TestAddLinkToYAML(t *testing.T) {
	out := addLinkToYAML("roots:\n  - docs\n", "depends_on", "lemma")
	assert.Equal(t, []Link{{To: "lemma", Kind: "depends_on"}}, parseLinksBlock(out))
	out = addLinkToYAML(out, "depends_on", "euclid")
	out = addLinkToYAML(out, "related", "DLM")
	assert.Equal(t, []Link{{To: "lemma", Kind: "depends_on"}, {To: "euclid", Kind: "depends_on"}, {To: "DLM", Kind: "related"}}, parseLinksBlock(out))
	assert.Contains(t, out, "roots:\n  - docs", "the rest of the file is untouched")
	inline := addLinkToYAML("links:\n  related: [a, b]\n", "related", "c")
	assert.Equal(t, []Link{{To: "a", Kind: "related"}, {To: "b", Kind: "related"}, {To: "c", Kind: "related"}}, parseLinksBlock(inline))
}

// workspace builds sibling repos with brains under one parent, as ~/Gits is.
func workspace(t *testing.T, names ...string) (string, map[string]string) {
	t.Helper()
	ws := t.TempDir()
	repos := map[string]string{}
	for _, n := range names {
		dir := filepath.Join(ws, n)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, exec_git(dir, "init", "-q"))
		_, err := Scaffold(filepath.Join(dir, ".euclid"), "repo")
		require.NoError(t, err)
		repos[n] = dir
	}
	return ws, repos
}

func TestLinkedBrainsAreReadOnlyAndWeighted(t *testing.T) {
	home := euclidHome(t)
	_, err0 := Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err0)
	_, repos := workspace(t, "arc", "lemma", "compliance")
	arc := repos["arc"]
	require.NoError(t, os.WriteFile(filepath.Join(repos["lemma"], ".euclid", "BRAIN.md"), []byte("# BRAIN\n\nLemma's PoR claim encoding is frozen at v3.\n"), 0o644))
	where, err := AddLink(arc, "lemma", "depends_on")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(arc, ".euclid", "euclid.yml"), where, "with a repo brain the link is committed with it")
	_, err = AddLink(arc, "github.com/lemma-ventures/compliance", "related")
	require.NoError(t, err)
	_, err = AddLink(arc, "nowhere", "related")
	require.NoError(t, err, "an unresolvable target is declared, not rejected")
	_, err = AddLink(arc, "lemma", "depends_on")
	require.NoError(t, err, "idempotent")

	set := ReadSet(arc)
	kinds := []string{}
	for _, b := range set {
		kinds = append(kinds, b.Kind)
	}
	assert.Equal(t, []string{"repo", "main", "linked", "linked"}, kinds, "own brains first, then dependencies, then related")
	assert.Equal(t, "repo:lemma", set[2].Label)
	assert.Equal(t, 0.8, set[2].Weight)
	assert.Equal(t, "repo:compliance", set[3].Label)
	assert.Equal(t, 0.5, set[3].Weight)
	for _, b := range set[2:] {
		assert.False(t, b.Writable, "linked brains are never written")
	}
	orientCache.key = ""
	assert.Contains(t, Orientation(arc), `<brain source="repo:lemma">Lemma's PoR claim encoding`, "a linked brain's snapshot reaches orientation")

	// Search sees the linked brain, tags the source, and weights it below local.
	require.NoError(t, os.WriteFile(filepath.Join(arc, ".euclid", "BRAIN.md"), []byte("# BRAIN\n\nArc's PoR claim encoding wraps lemma's.\n"), 0o644))
	hits := EuclidSearch(arc, "PoR claim encoding", 5)
	require.GreaterOrEqual(t, len(hits), 2)
	assert.Equal(t, "repo:arc", hits[0].Source, "the local brain outranks the linked one on an otherwise equal match")
	assert.Equal(t, "repo:lemma", hits[1].Source)
	assert.Greater(t, hits[0].Score, hits[1].Score)
}

func TestMainBrainLinksWhenTheRepoHasNoBrain(t *testing.T) {
	home := euclidHome(t)
	_, err := Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	ws := t.TempDir()
	plain := filepath.Join(ws, "plain")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	require.NoError(t, exec_git(plain, "init", "-q"))
	other := filepath.Join(ws, "other")
	require.NoError(t, os.MkdirAll(other, 0o755))
	_, err = Scaffold(filepath.Join(other, ".euclid"), "repo")
	require.NoError(t, err)
	where, err := AddLink(RepoRoot(plain), "other", "related")
	require.NoError(t, err)
	assert.Equal(t, mainLinksPath(), where, "no repo brain → the main brain remembers the link")
	set := ReadSet(plain)
	require.Len(t, set, 2)
	assert.Equal(t, "main", set[0].Kind)
	assert.True(t, set[0].Writable)
	assert.Equal(t, "repo:other", set[1].Label)
}

func TestProposeLinksFromManifestsAndJournal(t *testing.T) {
	euclidHome(t)
	_, repos := workspace(t, "captaincode", "euclid", "buzz", "lemma")
	cc := repos["captaincode"]
	require.NoError(t, os.WriteFile(filepath.Join(cc, "go.mod"), []byte("module github.com/lemma-ventures/captaincode\n\ngo 1.22\n\nrequire (\n\tgithub.com/lemma-ventures/euclid v0.1.0\n\tgithub.com/stretchr/testify v1.9.0\n)\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cc, "package.json"), []byte(`{"dependencies":{"buzz":"^1.0.0","effect":"^3"}}`), 0o644))
	journal := []JournalEntry{{Files: []string{filepath.Join(repos["lemma"], "docs", "spec.md"), "pkg/x.go"}}, {Files: []string{filepath.Join(repos["lemma"], "src", "a.rs")}}}
	props := ProposeLinks(cc, journal)
	targets := map[string]string{}
	whys := map[string]string{}
	for _, p := range props {
		targets[filepath.Base(p.Target)] = p.Kind
		whys[filepath.Base(p.Target)] = p.Why
	}
	assert.Equal(t, "depends_on", targets["euclid"], "go.mod require with a sibling checkout")
	assert.Equal(t, "depends_on", targets["buzz"], "package.json dependency with a sibling checkout")
	assert.Equal(t, "related", targets["lemma"], "journal touched files there")
	assert.Contains(t, whys["lemma"], "2 journaled runs")
	assert.NotContains(t, targets, "testify", "no local checkout → not proposed")
	// Declared links are not re-proposed.
	_, err := AddLink(cc, "euclid", "depends_on")
	require.NoError(t, err)
	props = ProposeLinks(cc, journal)
	for _, p := range props {
		assert.NotEqual(t, repos["euclid"], p.Target)
	}
}

func TestOtherDevelopersAreSearchOnly(t *testing.T) {
	euclidHome(t)
	_, repos := workspace(t, "arc")
	arc := repos["arc"]
	for _, h := range []string{"romain", "alice"} {
		_, err := Scaffold(filepath.Join(arc, ".euclid", "developers", h), "developer")
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(arc, ".euclid", "developers", "alice", "WISDOM.md"), []byte("# WISDOM\n\nThe zk verifier bench needs 32 GB; run it on the box, never the laptop.\n"), 0o644))
	orientCache.key = ""
	o := Orientation(arc)
	assert.NotContains(t, o, "zk verifier bench", "another developer's subtree is never warm-loaded")
	labels := []string{}
	for _, b := range ReadSet(arc) {
		labels = append(labels, b.Label)
	}
	assert.NotContains(t, labels, "dev:alice", "…nor in the read set")
	hits := EuclidSearch(arc, "zk verifier bench laptop", 3)
	require.NotEmpty(t, hits, "but search reaches it when relevant")
	assert.Equal(t, "dev:alice", hits[0].Source)
	assert.Equal(t, "developer-other", hits[0].Kind)
	assert.LessOrEqual(t, hits[0].Score, 1.0*hits[0].Score/weightOtherDev*weightOtherDev+1e-9)
}

func TestSearchCoversJournalAndRegisters(t *testing.T) {
	home := euclidHome(t)
	_, err := Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	cwd := t.TempDir()
	_, err = JournalRun(cwd, JournalEntry{At: time.Now(), Kind: "worker", Task: "implement value routing for the cheap path", Leg: "gemini", Outcome: "ok", Files: []string{"pkg/captaincode/value.go"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".euclid", "WISDOM.md"), []byte("# WISDOM\n\nDeploy the brain atomically: mv, never cp over the running binary.\n"), 0o644))
	hits := EuclidSearch(cwd, "value routing", 5)
	require.NotEmpty(t, hits)
	assert.Equal(t, "journal", hits[0].File)
	assert.Contains(t, hits[0].Text, "value.go")
	hits = EuclidSearch(cwd, "deploy atomically", 5)
	require.NotEmpty(t, hits)
	assert.Equal(t, "WISDOM.md", hits[0].File)
	assert.Equal(t, "main", hits[0].Source)
	assert.Empty(t, EuclidSearch(cwd, "", 5))

	body, label, ok := ReadRegister(cwd, "", "wisdom")
	require.True(t, ok)
	assert.Equal(t, "main", label)
	assert.Contains(t, body, "atomically")
	runs := RecentRuns(cwd, 5)
	require.Len(t, runs, 1)
	assert.Equal(t, "gemini", runs[0].Leg)
}

func exec_git(dir string, args ...string) error {
	return exec.Command("git", append([]string{"-C", dir}, args...)...).Run()
}
