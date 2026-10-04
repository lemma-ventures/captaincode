package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// curateFixture is a repository named like a private project, with a
// package name and a README title that name it too.
func curateFixture(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, exec.Command("git", "-C", root, "init", "-q").Run())
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/quillcore\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("# Quillon: ongoing proofs of storage\n"), 0o644))
	return root
}

func curateEnv(t *testing.T, vis string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_PRIVATE_NAMES", filepath.Join(home, "names"))
	prev := RepoVisibility
	RepoVisibility = func(string) string { return vis }
	t.Cleanup(func() { RepoVisibility = prev })
}

func TestCurationAddsAPrivateRepositorysNameAndProposesTheRest(t *testing.T) {
	curateEnv(t, "private")
	root := curateFixture(t, "zephyrine")
	cands := CurateCandidates(filepath.Join(root))
	byName := map[string]NameCandidate{}
	for _, c := range cands {
		byName[c.Name] = c
	}
	require.Contains(t, byName, "zephyrine")
	assert.True(t, byName["zephyrine"].Auto, "a private repository's own non-word name is added on the spot")
	assert.False(t, byName["quillcore"].Auto, "its package name is proposed")
	assert.Equal(t, "README title", byName["Quillon"].Source)
	assert.NotContains(t, byName, "proofs", "ordinary title words are not names")

	added, err := AddPrivateNames([]NameEntry{{Name: "zephyrine", Repo: "zephyrine", Source: "repository", How: NameAuto}})
	require.NoError(t, err)
	assert.Equal(t, []string{"zephyrine"}, added)
	assert.Equal(t, []string{"zephyrine"}, PrivateNames(), "the list leakcheck reads")
	assert.Equal(t, "zephyrine", NameRecords()["zephyrine"].Repo, "the record the dashboard groups by project")

	require.NoError(t, SuggestNames([]NameCandidate{byName["quillcore"]}))
	_, err = DismissPrivateNames([]string{"Quillon"})
	require.NoError(t, err)
	assert.Empty(t, CurateCandidates(root), "listed, suggested and dismissed names are never proposed again")
	assert.Equal(t, NameDismissed, NameRecords()["quillon"].How)
}

func TestCurationLeavesAPublicRepositoryAlone(t *testing.T) {
	curateEnv(t, "public")
	assert.Empty(t, CurateCandidates(curateFixture(t, "zephyrine")))
}

func TestAWordNamedRepositoryIsProposedNotAdded(t *testing.T) {
	curateEnv(t, "private")
	cands := CurateCandidates(curateFixture(t, "orchard"))
	require.NotEmpty(t, cands)
	assert.Equal(t, "orchard", cands[0].Name)
	assert.False(t, cands[0].Auto, "an ordinary word would flag ordinary prose: the user decides")
}
