package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MM38 E1–E2: brains, read set, orientation, journal, scaffold, distill.

func euclidHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_HOME", filepath.Join(home, ".euclid"))
	t.Setenv("EUCLID_HANDLE", "romain")
	t.Setenv("EUCLID_TEMPLATE_DIR", filepath.Join(home, "no-template"))
	t.Setenv("CAPTAIN_EUCLID", "")
	orientCache.key = "" // drop the 30s cache between tests
	return home
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "-q").Run())
	return dir
}

func TestReadSetIsEmptyWithoutAnyBrain(t *testing.T) {
	euclidHome(t)
	repo := gitRepo(t)
	assert.Empty(t, ReadSet(repo), "opt-in: no brain, no read set, no change to a run")
	assert.Equal(t, "", Orientation(repo))
	p, err := JournalRun(repo, JournalEntry{Task: "x"})
	require.NoError(t, err)
	assert.Equal(t, "", p, "nothing journaled without a brain")
}

func TestReadSetComposition(t *testing.T) {
	home := euclidHome(t)
	repo := gitRepo(t)
	_, err := Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	set := ReadSet(repo)
	require.Len(t, set, 1)
	assert.Equal(t, "main", set[0].Kind)
	assert.True(t, set[0].Writable, "main is the write brain when the repo has none")

	_, err = Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	set = ReadSet(filepath.Join(repo)) // no developer subtree yet
	require.Len(t, set, 2)
	assert.Equal(t, "repo", set[0].Kind)
	assert.False(t, set[0].Writable, "the shared repo brain is never the write brain")
	assert.True(t, set[1].Writable, "…so main still is")

	_, err = Scaffold(filepath.Join(repo, ".euclid", "developers", "romain"), "developer")
	require.NoError(t, err)
	set = ReadSet(filepath.Join(repo, "sub", "dir")) // a subdirectory of the repo
	_ = os.MkdirAll(filepath.Join(repo, "sub", "dir"), 0o755)
	set = ReadSet(filepath.Join(repo, "sub", "dir"))
	require.Len(t, set, 3)
	assert.Equal(t, []string{"developer", "repo", "main"}, []string{set[0].Kind, set[1].Kind, set[2].Kind}, "precedence: mine → repo → main")
	assert.True(t, set[0].Writable)
	assert.False(t, set[2].Writable, "exactly one write brain")
	wb, ok := WriteBrain(repo)
	require.True(t, ok)
	assert.Equal(t, "developer", wb.Kind)
	assert.Equal(t, "me@"+filepath.Base(repo), wb.Label)

	t.Setenv("CAPTAIN_EUCLID", "0")
	assert.Empty(t, ReadSet(repo), "kill switch")
}

func TestDeveloperHandle(t *testing.T) {
	home := euclidHome(t)
	assert.Equal(t, "romain", DeveloperHandle())
	t.Setenv("EUCLID_HANDLE", "Romain P.")
	assert.Equal(t, "romain-p", DeveloperHandle(), "slugified")
	t.Setenv("EUCLID_HANDLE", "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".euclid"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".euclid", "handle"), []byte("rp\n"), 0o644))
	assert.Equal(t, "rp", DeveloperHandle(), "the main brain's handle file")
}

func TestOrientationRendersWithinBudget(t *testing.T) {
	home := euclidHome(t)
	repo := gitRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".euclid"), 0o755))
	shared := filepath.Join(repo, ".euclid")
	require.NoError(t, os.MkdirAll(filepath.Join(shared, "developers", "romain"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(shared, "MAP.md"), []byte("# MAP\n\n| area | path | what lives there |\n|---|---|---|\n| plan | docs/ARC-IMPLEMENTATION-PLAN.md | the work packages |\n| specs | docs/arc-implementation/ | D00, D01 specs |\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(shared, "BRAIN.md"), []byte("---\nname: brain\n---\n# BRAIN\n\n> quote to skip\n\nD00, A01-A, P01, O04 landed; D01 half done (parameters.md, recursion-contract.md missing).\n\nMore text.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(shared, "developers", "romain", "BRAIN.md"), []byte("# BRAIN\n\nI am mid-way through wiring value routing; deploy atomically.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".euclid", "WISDOM.md"), []byte("# WISDOM\n\nNever cp over a running binary; mv atomically.\n"), 0o644))

	out := Orientation(repo)
	assert.Contains(t, out, "<euclid>")
	assert.Contains(t, out, `<brain source="me@`+filepath.Base(repo)+`">I am mid-way`, "write brain's snapshot first")
	assert.Contains(t, out, "D01 half done", "shared BRAIN thesis, front matter and quote skipped")
	assert.Contains(t, out, "docs/ARC-IMPLEMENTATION-PLAN.md", "MAP rows")
	assert.NotContains(t, out, "| area |", "header row dropped")
	assert.Contains(t, out, `<wisdom source="main">Never cp`, "main brain lessons")
	assert.Less(t, len(out), 2600, "within the default budget")

	// Budget is enforced, and the block still closes.
	t.Setenv("CAPTAIN_EUCLID_ORIENTATION_CHARS", "400")
	orientCache.key = ""
	small := Orientation(repo)
	assert.Less(t, len(small), 520)
	assert.True(t, strings.HasSuffix(strings.TrimSpace(small), "</euclid>"))
}

func TestJournalRunWritesScrubbedEntry(t *testing.T) {
	home := euclidHome(t)
	repo := gitRepo(t)
	_, err := Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	log := filepath.Join(t.TempDir(), "r1-grok.log")
	require.NoError(t, os.WriteFile(log, []byte("# captain worker log\n---\n[3s] ⚙ Read docs/plan.md\n[9s] ⚙ edit pkg/a.go, pkg/b.go\n[12s] ⚙ shell go test ./...\n[14s] ⚙ Read docs/plan.md\nanswer\n---\ndone in 14s\n"), 0o644))
	p, err := JournalRun(repo, JournalEntry{Kind: "worker", Task: "  implement the plan   with key sk-abcdefghijklmnopqrstuvwxyz1234 ", Leg: "grok",
		Outcome: "ok", DurationMs: 14000, Files: FilesFromWorkerLog(log), Log: log})
	require.NoError(t, err)
	assert.Contains(t, p, filepath.Join(".euclid", "journal", "activity-"))
	entries, err := ReadJournal(EuclidBrain{Root: filepath.Join(home, ".euclid")}, time.Time{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	e := entries[0]
	assert.Equal(t, "implement the plan with key <redacted>", e.Task, "secrets scrubbed, whitespace collapsed")
	assert.Equal(t, []string{"docs/plan.md", "pkg/a.go", "pkg/b.go"}, e.Files, "read/edit paths, deduplicated, sorted; shell commands excluded")
	assert.Equal(t, RepoRoot(repo), e.Repo, "git resolves /var → /private/var")
	assert.Equal(t, "grok", e.Leg)
	// since-filter
	later, err := ReadJournal(EuclidBrain{Root: filepath.Join(home, ".euclid")}, time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.Empty(t, later)
}

func TestScaffoldCreatesRegistersOnce(t *testing.T) {
	home := euclidHome(t)
	root := filepath.Join(home, ".euclid")
	created, err := Scaffold(root, "main")
	require.NoError(t, err)
	assert.Contains(t, strings.Join(created, "\n"), "BRAIN.md")
	assert.FileExists(t, filepath.Join(root, "links.yml"))
	assert.DirExists(t, filepath.Join(root, "journal"))
	again, err := Scaffold(root, "main")
	require.NoError(t, err)
	assert.Empty(t, again, "idempotent: existing files are never overwritten")

	repo := gitRepo(t)
	created, err = Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(repo, ".euclid", "euclid.yml"))
	assert.FileExists(t, filepath.Join(repo, ".euclid", "developers", "README.md"))
	dev, err := Scaffold(filepath.Join(repo, ".euclid", "developers", "romain"), "developer")
	require.NoError(t, err)
	joined := strings.Join(dev, "\n")
	assert.Contains(t, joined, "BRAIN.md")
	assert.NotContains(t, joined, "SOUL.md", "a developer subtree has no charter of its own")
}

func TestDistillPromptParseAndApply(t *testing.T) {
	home := euclidHome(t)
	root := filepath.Join(home, ".euclid")
	_, err := Scaffold(root, "main")
	require.NoError(t, err)
	brain := EuclidBrain{Root: root, Kind: "main", Writable: true, Label: "main"}
	entries := []JournalEntry{{At: time.Now(), Kind: "worker", Task: "add value routing", Leg: "gemini", Outcome: "ok", Files: []string{"pkg/value.go"}}}
	prompt := DistillPrompt(brain, entries)
	assert.True(t, IsDistillRequest(prompt))
	assert.Contains(t, prompt, "=== current BRAIN.md ===")
	assert.Contains(t, prompt, "pkg/value.go")
	assert.Contains(t, prompt, "Respond with JSON only")

	raw := "Here you go:\n```json\n{\"summary\":\"one run\",\"edits\":[{\"file\":\"BRAIN.md\",\"mode\":\"replace_section\",\"anchor\":\"## Current state\",\"text\":\"- Active front: value routing\\n- Last landed: pkg/value.go\",\"why\":\"journal\"},{\"file\":\"memory/MEMORIES.md\",\"mode\":\"append\",\"text\":\"- gemini added value routing (token sk-abcdefghijklmnopqrstuvwxyz1234)\",\"why\":\"fact\"},{\"file\":\"SOUL.md\",\"mode\":\"append\",\"text\":\"nope\",\"why\":\"forbidden\"}]}\n```"
	d, err := ParseDistillation(raw)
	require.NoError(t, err)
	assert.Equal(t, "one run", d.Summary)
	require.Len(t, d.Edits, 2, "SOUL edit dropped")
	assert.Contains(t, d.Edits[1].Text, "<redacted>", "scrubbed")

	touched, err := ApplyEdits(brain, d.Edits)
	require.NoError(t, err)
	assert.Len(t, touched, 2)
	b, _ := os.ReadFile(filepath.Join(root, "BRAIN.md"))
	assert.Contains(t, string(b), "- Active front: value routing")
	assert.NotContains(t, string(b), "- Active front:\n", "the section body was replaced, not appended")
	m, _ := os.ReadFile(filepath.Join(root, "memory", "MEMORIES.md"))
	assert.Contains(t, string(m), "gemini added value routing")

	_, err = ApplyEdits(EuclidBrain{Root: root, Kind: "repo", Writable: false, Label: "repo:x"}, d.Edits)
	require.Error(t, err, "a read-only brain is never written")
	patch := RenderPatch(EuclidBrain{Root: root, Kind: "repo", Label: "repo:x"}, d)
	assert.Contains(t, patch, "## BRAIN.md · replace_section · ## Current state")
	assert.Contains(t, patch, "```markdown")

	require.NoError(t, MarkDistilled(brain, time.Now()))
	assert.False(t, LastDistilledAt(brain).IsZero())
}

func TestReplaceSection(t *testing.T) {
	doc := "# T\n\n## A\n\nold a\n\n## B\n\nold b\n"
	out := replaceSection(doc, "## A", "new a")
	assert.Contains(t, out, "## A\n\nnew a\n\n## B\n\nold b")
	assert.NotContains(t, out, "old a")
	out = replaceSection(doc, "## C", "new c")
	assert.Contains(t, out, "## C\n\nnew c", "missing heading is appended")
	assert.Contains(t, out, "old b")
}

// A task that names other repositories reads their brains after the
// session's own and before the linked ones (2026-09-13).
func TestReadSetWithNamedReposReadsTheirBrains(t *testing.T) {
	t.Setenv("CAPTAIN_EUCLID", "1")
	root := t.TempDir()
	t.Setenv("EUCLID_HOME", filepath.Join(root, "main-brain"))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "main-brain"), 0o755))
	for _, r := range []string{"DLM", "captaincode", "nobrain"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, r, ".git"), 0o755))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "DLM", ".euclid"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "captaincode", ".euclid"), 0o755))

	set := ReadSetWith(filepath.Join(root, "DLM"), []string{filepath.Join(root, "captaincode"), filepath.Join(root, "nobrain")})
	var labels []string
	for _, b := range set {
		labels = append(labels, b.Kind+":"+b.Label)
	}
	assert.Equal(t, []string{"repo:repo:DLM", "main:main", "named:repo:captaincode"}, labels, "own, then named; a repo without a brain adds nothing")
	for _, b := range set {
		if b.Kind == "named" {
			assert.False(t, b.Writable, "read-only: the write brain stays the session's own")
		}
	}
	assert.Equal(t, ReadSet(filepath.Join(root, "DLM")), ReadSetWith(filepath.Join(root, "DLM"), nil))
}
