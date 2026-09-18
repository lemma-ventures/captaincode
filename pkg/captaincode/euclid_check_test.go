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

// The launch check: what Captain Code says about its main and local brains
// before the first prompt. Filesystem only, so it runs with the brain down.

func TestCheckReportsBothBrainsMissingWithTheCommandThatCreatesThem(t *testing.T) {
	euclidHome(t)
	repo := gitRepo(t)
	checks := CheckBrains(repo)
	require.Len(t, checks, 2)
	main, local := checks[0], checks[1]
	assert.Equal(t, "main", main.Kind)
	assert.False(t, main.Present)
	assert.Contains(t, main.Fix, "captain euclid init")
	assert.Equal(t, "local", local.Kind)
	assert.False(t, local.Present)
	assert.Contains(t, local.Fix, "captain euclid init --repo")
	out := RenderBrainChecks(checks)
	assert.Contains(t, out, "main")
	assert.Contains(t, out, "local")
	assert.Contains(t, out, "captain euclid init --repo")
}

func TestCheckFlagsTemplateRegistersAndCountsJournalSinceDistill(t *testing.T) {
	home := euclidHome(t)
	main := filepath.Join(home, ".euclid")
	_, err := Scaffold(main, "main")
	require.NoError(t, err)
	repo := gitRepo(t)

	checks := CheckBrains(repo)
	mc := checks[0]
	assert.True(t, mc.Present)
	assert.Contains(t, mc.Template, "SOUL.md", "a scaffolded main brain is still all placeholders")
	assert.Contains(t, mc.Template, "VISION.md")
	assert.Contains(t, mc.Attention, "template", "the operator is told the brain is empty")

	// Fill the doctrine, journal three runs: the check counts what waits for a distill.
	require.NoError(t, os.WriteFile(filepath.Join(main, "SOUL.md"), []byte("# SOUL\n\nDoctrine: correctness over speed.\n\n## Identity\n\n- Name: Euclid\n"), 0o644))
	for i := 0; i < 3; i++ {
		_, err := JournalRun(repo, JournalEntry{Kind: "worker", Task: "fix", Leg: "free", Outcome: "ok"})
		require.NoError(t, err)
	}
	checks = CheckBrains(repo)
	mc = checks[0]
	assert.NotContains(t, mc.Template, "SOUL.md")
	assert.Equal(t, 3, mc.Journal)
	assert.True(t, mc.Writable, "with no repo brain the main brain takes the journal")
	assert.Contains(t, RenderBrainChecks(checks), "3 runs")

	require.NoError(t, MarkDistilled(EuclidBrain{Root: main}, time.Now()))
	checks = CheckBrains(repo)
	assert.Equal(t, 0, checks[0].Journal)
}

func TestCheckLocalBrainWantsBootstrapDoctrineAndDeveloperSubtree(t *testing.T) {
	home := euclidHome(t)
	main := filepath.Join(home, ".euclid")
	_, err := Scaffold(main, "main")
	require.NoError(t, err)
	repo := gitRepo(t)
	shared := filepath.Join(repo, ".euclid")
	_, err = Scaffold(shared, "repo")
	require.NoError(t, err)

	lc := CheckBrains(repo)[1]
	assert.True(t, lc.Present)
	want, _ := filepath.EvalSymlinks(shared)
	got, _ := filepath.EvalSymlinks(lc.Root)
	assert.Equal(t, want, got)
	assert.Contains(t, lc.Template, "VISION.md", "a fresh repo brain has not been bootstrapped from its docs")
	assert.Contains(t, lc.Fix, "captain euclid bootstrap --apply")
	assert.False(t, lc.Inherits, "the main brain is a template, so there was no doctrine to inherit")
	assert.False(t, lc.Writable, "no developer subtree: runs would land in the main brain")
	assert.Contains(t, lc.Attention, "developers/romain")

	// Doctrine in the main brain + a re-scaffold = inherited; subtree = writable.
	require.NoError(t, os.WriteFile(filepath.Join(main, "SOUL.md"), []byte("# SOUL\n\nDoctrine: correctness over speed.\n\n## Identity\n\n- Name: Euclid\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(main, "WISDOM.md"), []byte("# WISDOM\n\nJudgment that keeps paying off.\n\n- Measure before you optimize.\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(shared, "SOUL.md")))
	require.NoError(t, os.Remove(filepath.Join(shared, "WISDOM.md")))
	_, err = Scaffold(shared, "repo")
	require.NoError(t, err)
	_, err = Scaffold(filepath.Join(shared, "developers", "romain"), "developer")
	require.NoError(t, err)
	lc = CheckBrains(repo)[1]
	assert.True(t, lc.Inherits)
	assert.True(t, lc.Writable)
	assert.NotContains(t, lc.Attention, "developers/romain")
}

func TestCheckNoticesAStaleCatalog(t *testing.T) {
	home := euclidHome(t)
	main := filepath.Join(home, ".euclid")
	_, err := Scaffold(main, "main")
	require.NoError(t, err)
	repo := gitRepo(t)
	assert.False(t, CheckBrains(repo)[0].Stale, "no catalog yet is not stale, it is unbuilt")

	idx := filepath.Join(main, "index", "catalog.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(idx), 0o755))
	require.NoError(t, os.WriteFile(idx, []byte("{}\n"), 0o644))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(idx, old, old))
	require.NoError(t, os.WriteFile(filepath.Join(main, "WISDOM.md"), []byte("# WISDOM\n\n- Measure first.\n"), 0o644))
	c := CheckBrains(repo)[0]
	fi, _ := os.Stat(idx)
	wi, _ := os.Stat(filepath.Join(main, "WISDOM.md"))
	assert.True(t, c.Stale, "catalog %v vs WISDOM %v", fi.ModTime(), wi.ModTime())
	assert.True(t, strings.Contains(RenderBrainChecks([]BrainCheck{c}), "catalog stale"))
}

func TestCheckDoesNotMistakeTheMainBrainForALocalOne(t *testing.T) {
	home := euclidHome(t)
	_, err := Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	// A plain folder under $HOME, not a git repo: walking up finds ~/.euclid,
	// which is the MAIN brain, not this folder's.
	scratch := filepath.Join(home, "scratch")
	require.NoError(t, os.MkdirAll(scratch, 0o755))
	assert.Equal(t, "", RepoRoot(scratch), "the main brain's parent is not a repo root")
	checks := CheckBrains(scratch)
	assert.False(t, checks[1].Present)
	assert.Contains(t, checks[1].Attention, "not a git repository")
	set := ReadSet(scratch)
	require.Len(t, set, 1, "the main brain appears once, as main")
	assert.Equal(t, "main", set[0].Kind)
}

func TestMainBrainIsNotJudgedOnItsMap(t *testing.T) {
	home := euclidHome(t)
	main := filepath.Join(home, ".euclid")
	_, err := Scaffold(main, "main")
	require.NoError(t, err)
	for _, n := range []string{"SOUL.md", "VISION.md", "WISDOM.md", "BRAIN.md", "memory/MEMORIES.md", "memory/FAILURES.md"} {
		require.NoError(t, os.WriteFile(filepath.Join(main, n), []byte("# X\n\nFilled with a real thesis line.\n\n- A real bullet.\n"), 0o644))
	}
	c := CheckBrains(gitRepo(t))[0]
	assert.Empty(t, c.Template, "MAP is per project; a filled main brain has no template registers")
	assert.Equal(t, "", c.Attention)
}
