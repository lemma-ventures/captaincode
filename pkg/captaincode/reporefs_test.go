package captaincode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A fake ~/Gits: DLM, arc, lemma, captaincode, Compliance/lemma-ventures-website.
func fakeWorkspaceRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, r := range []string{"DLM", "arc", "lemma", "captaincode", "Compliance/lemma-ventures-website", "notes", "Relay", "euclid", "HerdG", "buzz-finance"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, r, ".git"), 0o755))
	}
	require.NoError(t, os.Remove(filepath.Join(root, "notes", ".git"))) // a plain folder, not a repo
	t.Setenv("CAPTAIN_WORKSPACE_ROOT", root)
	t.Setenv("EUCLID_HOME", filepath.Join(root, ".euclid-main"))
	ResetKnownReposForTest()
	return root
}

func TestKnownReposScansTwoLevelsOfTheWorkspaceRoot(t *testing.T) {
	root := fakeWorkspaceRoot(t)
	repos := KnownRepos()
	assert.Contains(t, repos, filepath.Join(root, "DLM"))
	assert.Contains(t, repos, filepath.Join(root, "Compliance", "lemma-ventures-website"), "a repo inside a folder")
	assert.NotContains(t, repos, filepath.Join(root, "notes"), "a folder without .git or .euclid is not a repo")
}

func TestRepoRefsFollowsAPathToItsRepository(t *testing.T) {
	root := fakeWorkspaceRoot(t)
	cwd := filepath.Join(root, "DLM")
	assert.Equal(t, []string{filepath.Join(root, "captaincode")},
		RepoRefs("fix the launcher in "+root+"/captaincode/cmd/captaincode/main.go", cwd))
	assert.Equal(t, []string{filepath.Join(root, "captaincode")},
		RepoRefs("look at "+root+"/captaincode/does/not/exist.go", cwd), "a path that does not exist yet still names its repo")
	assert.Empty(t, RepoRefs("edit "+root+"/DLM/README.md", cwd), "the repo the TUI is in is not a reference")
}

func TestRepoRefsMatchesKnownNamesAsWholeWords(t *testing.T) {
	root := fakeWorkspaceRoot(t)
	cwd := filepath.Join(root, "DLM")
	assert.Equal(t, []string{filepath.Join(root, "captaincode")}, RepoRefs("in captaincode, make /repeat finish gracefully", cwd))
	assert.Equal(t, []string{filepath.Join(root, "captaincode")}, RepoRefs("Captaincode: rename the flag", cwd), "case-insensitive for a long name")
	assert.Empty(t, RepoRefs("the arc of this refactor is long", cwd), "a short lowercase name in prose is a word, not a repo")
	assert.Equal(t, []string{filepath.Join(root, "arc")}, RepoRefs("run the tests in the arc repo", cwd), "…unless a cue says repo")
	assert.Equal(t, []string{filepath.Join(root, "arc")}, RepoRefs("check "+root+"/arc too", cwd), "a path form needs no cue")
	assert.Empty(t, RepoRefs("write a dlm parser", cwd), "DLM is spelt DLM")
	assert.Empty(t, RepoRefs("what about DLM?", cwd), "the current repo is never a reference")
	assert.Empty(t, RepoRefs("captaincodex is not a repo", cwd), "whole words only")
	// Folder names that are words (2026-09-15: "prove agentic compliance" moved a worker to ~/Gits/Compliance).
	assert.Empty(t, RepoRefs("prove agentic compliance for the regulated agent", cwd))
	assert.Empty(t, RepoRefs("add a relay between the gateway and the ledger", cwd))
	assert.Empty(t, RepoRefs("Relay the message to the ledger", cwd), "capitalised at a sentence start is still prose")
	assert.Empty(t, RepoRefs("load the euclid memory first", cwd))
	assert.Equal(t, []string{filepath.Join(root, "euclid")}, RepoRefs("fix the search in the euclid repo", cwd), "a cue makes it a repo")
	assert.Equal(t, []string{filepath.Join(root, "Relay")}, RepoRefs("port this to the Relay project", cwd))
	assert.Equal(t, []string{filepath.Join(root, "HerdG")}, RepoRefs("same fix in HerdG", cwd), "not a word, spelt as the folder")
	assert.Empty(t, RepoRefs("the herdg bot", cwd))
	assert.Equal(t, []string{filepath.Join(root, "buzz-finance")}, RepoRefs("mirror it in buzz-finance", cwd), "hyphenated names are never prose")
}

func TestRepoRefsAHyphenatedNameByItsParts(t *testing.T) {
	root := fakeWorkspaceRoot(t)
	cwd := filepath.Join(root, "DLM")
	site := filepath.Join(root, "Compliance", "lemma-ventures-website")
	assert.Equal(t, []string{site}, RepoRefs("update the pricing page on the lemma website", cwd), "two of three parts, first included, outranks the bare lemma repo")
	assert.Equal(t, []string{site}, RepoRefs("deploy lemma-ventures-website", cwd))
	assert.Equal(t, []string{filepath.Join(root, "lemma")}, RepoRefs("bump the block reward in the lemma repo", cwd), "lemma is a word: the repo needs a cue")
	assert.Empty(t, RepoRefs("bump the block reward in lemma", cwd), "…without one it is the chain, not the folder")
	assert.Empty(t, RepoRefs("the website needs ventures", cwd), "without the first part it is prose")
	assert.Empty(t, RepoRefs("add the lemma bounty to the website footer", cwd), "the parts far apart are two words, not the name")
}

func TestRepoRefsSeveralReposAreAllNamed(t *testing.T) {
	root := fakeWorkspaceRoot(t)
	cwd := filepath.Join(root, "DLM")
	refs := RepoRefs("compare the CI setup in captaincode and the lemma repo", cwd)
	assert.ElementsMatch(t, []string{filepath.Join(root, "captaincode"), filepath.Join(root, "lemma")}, refs)
	t.Setenv("CAPTAIN_REPO_REFS", "0")
	assert.Empty(t, RepoRefs("in captaincode do x", cwd), "opt-out")
}
