package captaincode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Euclid augments the brain: every feature below degrades to "nothing" when
// its input is missing, never to an error that gates a run.

func repoWithBrain(t *testing.T) (repo string, brain EuclidBrain) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_HOME", filepath.Join(home, ".euclid"))
	t.Setenv("EUCLID_HANDLE", "tester")
	t.Setenv("EUCLID_TEMPLATE_DIR", filepath.Join(home, "none"))
	t.Setenv("CAPTAIN_EUCLID_ENGINE", filepath.Join(home, "no-engine"))
	repo = filepath.Join(home, "arc")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Arc\n\nArc is the reference IVC. The verifier cap is 256 KiB per aggregate. See docs/PLAN.md.\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "docs", "PLAN.md"), []byte("# Plan\n\nMilestone G4 closes bounded recursion (APORF2 stays frozen).\n"), 0o644))
	root := filepath.Join(repo, ".euclid")
	_, err := Scaffold(root, "repo")
	require.NoError(t, err)
	_, err = Scaffold(filepath.Join(root, "developers", "tester"), "developer")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "BRAIN.md"), []byte("# BRAIN\n\n## Current state\n\n- Active front: bounded recursion under the G0 contracts.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "MAP.md"), []byte("# MAP\n\n| area | path | what |\n|---|---|---|\n| verifier | arc/src/decider.rs | the decider |\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "WISDOM.md"), []byte("# WISDOM\n\n- Never raise the 256 KiB cap to make a benchmark pass.\n"), 0o644))
	return repo, EuclidBrain{Root: root, Kind: "repo", Label: "repo:arc"}
}

func TestDirectorMemoryCarriesDecisionsLessonsAndFailures(t *testing.T) {
	repo, brain := repoWithBrain(t)
	_, err := AppendNote(repo, "failure", "round 3 waited an hour for a benchmark that had died with its round", "captain")
	require.NoError(t, err)
	_, err = AppendNote(repo, "decision", "keep APORF2 bytes frozen; recursion work goes through ARCRF002", "worker")
	require.NoError(t, err)
	m := DirectorMemory(repo)
	assert.Contains(t, m, "bounded recursion under the G0 contracts", "BRAIN thesis")
	assert.Contains(t, m, "arc/src/decider.rs", "MAP rows")
	assert.Contains(t, m, "Never raise the 256 KiB cap", "WISDOM")
	assert.Contains(t, m, "waited an hour for a benchmark", "recent FAILURES")
	assert.Contains(t, m, "APORF2 bytes frozen", "recent decisions")
	assert.Less(t, len(m), 3000, "bounded")
	_ = brain
	assert.Empty(t, DirectorMemory(t.TempDir()), "no brain: nothing, never an error")
}

func TestAppendNoteWritesTheWriteBrainOnly(t *testing.T) {
	repo, brain := repoWithBrain(t)
	p, err := AppendNote(repo, "question", "does the decider need N04 before recursion?", "worker")
	require.NoError(t, err)
	assert.Contains(t, p, filepath.Join("developers", "tester", "memory", "open-questions.md"), "the developer subtree is the write brain; the shared repo brain is PR-gated")
	b, _ := os.ReadFile(p)
	assert.Contains(t, string(b), time.Now().Format("2006-01-02")+" does the decider need N04")
	_, err = AppendNote(repo, "poem", "x", "worker")
	assert.Error(t, err, "unknown kinds are refused")
	_ = brain
}

func TestMCPArgsForTheCLILegs(t *testing.T) {
	repo, _ := repoWithBrain(t)
	args := ClaudeMCPArgs(repo)
	require.Len(t, args, 2)
	assert.Equal(t, "--mcp-config", args[0])
	assert.Contains(t, args[1], `"euclid"`)
	assert.Contains(t, args[1], `"CAPTAIN_CWD":"`+repo+`"`, "the server is pinned to the workspace")
	cargs := CodexMCPArgs(repo)
	assert.Contains(t, strings.Join(cargs, " "), `mcp_servers.euclid.args=["euclid","mcp"]`)
	assert.Contains(t, strings.Join(cargs, " "), `mcp_servers.euclid.env={CAPTAIN_CWD="`+repo+`"}`)
	t.Setenv("CAPTAIN_EUCLID", "0")
	assert.Nil(t, ClaudeMCPArgs(repo), "Euclid off: the CLI runs as before")
	assert.Nil(t, CodexMCPArgs(repo))
}

func TestParseProbesKeepsOnlyWhatVerifies(t *testing.T) {
	repo, brain := repoWithBrain(t)
	reply := `{"orientation": [["active front", "bounded recursion under the G0 contracts", "README.md"], ["made up", "quantum widgets", null]],
	 "recall": [["what closes bounded recursion", "Milestone G4"], ["what is the cap", "verifier cap is 256 KiB"], ["needle in question", "APORF2 stays frozen APORF2"], ["nowhere", "not in any doc"]],
	 "composition": [["planning docs", "all the planning documents", ["README.md", "docs/PLAN.md"]], ["thin", "one member only", ["README.md"]]]}`
	ps, err := ParseProbes(reply, brain, repo)
	require.NoError(t, err)
	assert.Len(t, ps.Orientation, 1, "a needle absent from every register is dropped")
	assert.Equal(t, "README.md", ps.Orientation[0][2])
	assert.Len(t, ps.Recall, 2, "a needle absent from the docs, or contained in its own question, is dropped")
	assert.Len(t, ps.Composition, 1, "an answer set needs at least two verified members")
	assert.Equal(t, 4, ps.Dropped)
	written, err := WriteProbes(brain, ps)
	require.NoError(t, err)
	assert.Len(t, written, 1, "orientation needs 3 probes, recall 3, composition 1: only composition qualifies here")
	assert.FileExists(t, filepath.Join(brain.Root, "probes", "composition.json"))
}

func TestJournalRunWritesAMarkdownPageWithTokensSpent(t *testing.T) {
	repo, brain := repoWithBrain(t)
	p, err := JournalRun(repo, JournalEntry{Kind: "worker", Task: "/frontier address bounded recursion", Leg: "claude", Outcome: "ok",
		DurationMs: 61000, Tokens: 48231, CostUSD: 0.42, Summary: "Wrote the NTT product.\n\nAll 40 tests pass.", Files: []string{"arc/src/decider.rs"}})
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(p, ".jsonl"), "the ledger line is still written")
	pages, _ := filepath.Glob(filepath.Join(brain.Root, "developers", "tester", "journal", "*_claude-*.md"))
	require.Len(t, pages, 1, "one page per worker run beside the ledger")
	b, _ := os.ReadFile(pages[0])
	assert.Contains(t, string(b), "**Tokens-Spent**: 48231", "the field the dashboard's Tokens tab reads")
	assert.Contains(t, string(b), "**Cost-USD**: 0.4200")
	assert.Contains(t, string(b), "type: journal", "front-matter for the catalog")
	assert.Contains(t, string(b), "- `arc/src/decider.rs`")
	_, err = JournalRun(repo, JournalEntry{Kind: "distill", Task: "distill", Outcome: "ok"})
	require.NoError(t, err)
	pages, _ = filepath.Glob(filepath.Join(brain.Root, "developers", "tester", "journal", "*.md"))
	assert.Len(t, pages, 1, "bookkeeping runs get no page")
}
