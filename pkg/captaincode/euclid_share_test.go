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

// gitRepoWithBrain is repoWithBrain as a real git repository, so the
// tracked/ignored question has an answer.
func gitRepoWithBrain(t *testing.T) (repo string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo, _ = repoWithBrain(t)
	require.NoError(t, os.RemoveAll(filepath.Join(repo, ".git")))
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"add", "-A"}, {"commit", "-q", "-m", "brain"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	resetSharedTrackedCache()
	return repo
}

// A note recorded in the developer's local brain is ALSO promoted as a file
// of its own under the shared brain's notes/ - the layer that travels in
// pull requests without ever conflicting (two notes never share a file).
func TestNotesArePromotedToTheSharedBrainAsFilesOfTheirOwn(t *testing.T) {
	repo := gitRepoWithBrain(t)
	assert.True(t, SharedBrainTracked(repo))
	p, err := AppendNote(repo, "decision", "the decider stays a subprocess", "worker")
	require.NoError(t, err)
	assert.Contains(t, p, filepath.Join("developers", "tester"), "the local ledger, as before")

	notes, err := PendingNotes(filepath.Join(repo, ".euclid"))
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, "decision", notes[0].Kind)
	assert.Equal(t, "tester", notes[0].Handle)
	assert.Equal(t, "worker", notes[0].By)
	assert.Equal(t, "the decider stays a subprocess", notes[0].Text)
	assert.WithinDuration(t, time.Now(), notes[0].At, time.Minute)
	assert.True(t, strings.HasPrefix(filepath.Base(notes[0].Path), time.Now().UTC().Format("20060102T")), "timestamp first: oldest first on disk")
	assert.Contains(t, filepath.Base(notes[0].Path), "-tester-decision-")

	_, err = AppendNote(repo, "failure", "a run captain had to end", "captain")
	require.NoError(t, err)
	notes, _ = PendingNotes(filepath.Join(repo, ".euclid"))
	require.Len(t, notes, 2)
	assert.NotEqual(t, notes[0].Path, notes[1].Path)
	assert.Equal(t, "- "+time.Now().Format("2006-01-02")+" a run captain had to end _(captain · tester)_", notes[1].LedgerLine())
}

// A brain the repository ignores stays local: nothing is promoted.
func TestNoPromotionWhenTheBrainIsIgnored(t *testing.T) {
	repo := gitRepoWithBrain(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".euclid/\n"), 0o644))
	resetSharedTrackedCache()
	assert.False(t, SharedBrainTracked(repo))
	_, err := AppendNote(repo, "memory", "kept local", "worker")
	require.NoError(t, err)
	notes, _ := PendingNotes(filepath.Join(repo, ".euclid"))
	assert.Empty(t, notes)
}

// The fold (what CI runs on main): the model's edits reach BRAIN/WISDOM
// only, every note is appended verbatim to its ledger, and the note files
// are gone - so a second fold has nothing to do.
func TestFoldWritesRegistersAndLedgersAndRetiresTheNotes(t *testing.T) {
	repo := gitRepoWithBrain(t)
	shared := filepath.Join(repo, ".euclid")
	for _, n := range [][3]string{{"decision", "the decider stays a subprocess", "worker"}, {"memory", "the cap is 256 KiB", "worker"}, {"failure", "grok graded poor on the plan", "director"}} {
		_, err := AppendNote(repo, n[0], n[1], n[2])
		require.NoError(t, err)
	}
	notes, err := PendingNotes(shared)
	require.NoError(t, err)
	require.Len(t, notes, 3)

	prompt := FoldPrompt(shared, notes)
	assert.True(t, IsDistillRequest(prompt), "kept out of the scorecards and the journal like a distill")
	assert.Contains(t, prompt, "the decider stays a subprocess")
	assert.Contains(t, prompt, "=== current BRAIN.md ===")

	d, err := ParseFold(`{"summary":"decider settled","edits":[
	  {"file":"BRAIN.md","mode":"replace_section","anchor":"## Current state","text":"- Active front: the subprocess decider.","why":"decision"},
	  {"file":"memory/MEMORIES.md","mode":"append","text":"smuggled","why":"not allowed"},
	  {"file":"WISDOM.md","mode":"append","text":"- A poor grade on a plan is a brief problem, not a leg problem.","why":"lesson"}]}`)
	require.NoError(t, err)
	require.Len(t, d.Edits, 2, "ledger edits by the model are dropped: the notes go there verbatim")

	r, err := Fold(shared, notes, &d)
	require.NoError(t, err)
	assert.True(t, r.ByModel)
	assert.Len(t, r.Edits, 2)
	assert.Len(t, r.Removed, 3)
	brain, _ := os.ReadFile(filepath.Join(shared, "BRAIN.md"))
	assert.Contains(t, string(brain), "the subprocess decider")
	assert.NotContains(t, string(brain), "bounded recursion under the G0", "the stale section was replaced")
	wisdom, _ := os.ReadFile(filepath.Join(shared, "WISDOM.md"))
	assert.Contains(t, string(wisdom), "brief problem")
	ledger, _ := os.ReadFile(filepath.Join(shared, "memory", "decisions-ledger.md"))
	assert.Contains(t, string(ledger), "the decider stays a subprocess _(tester)_")
	mem, _ := os.ReadFile(filepath.Join(shared, "memory", "MEMORIES.md"))
	assert.Contains(t, string(mem), "the cap is 256 KiB")
	assert.NotContains(t, string(mem), "smuggled")
	fails, _ := os.ReadFile(filepath.Join(shared, "memory", "FAILURES.md"))
	assert.Contains(t, string(fails), "grok graded poor on the plan _(director · tester)_")

	again, _ := PendingNotes(shared)
	assert.Empty(t, again, "folded notes are retired")
	r2, err := Fold(shared, again, nil)
	require.NoError(t, err)
	assert.Zero(t, r2.Notes)

	// No model at all: the ledgers still take the notes.
	_, err = AppendNote(repo, "question", "does the fold need a model?", "worker")
	require.NoError(t, err)
	notes, _ = PendingNotes(shared)
	r3, err := Fold(shared, notes, nil)
	require.NoError(t, err)
	assert.False(t, r3.ByModel)
	q, _ := os.ReadFile(filepath.Join(shared, "memory", "open-questions.md"))
	assert.Contains(t, string(q), "does the fold need a model?")
}

func TestRepoScaffoldKeepsDeveloperBrainsLocal(t *testing.T) {
	repo, _ := repoWithBrain(t)
	ig, err := os.ReadFile(filepath.Join(repo, ".euclid", ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(ig), "developers/")
	assert.FileExists(t, filepath.Join(repo, ".euclid", SharedNotesDir, ".gitkeep"))
}
