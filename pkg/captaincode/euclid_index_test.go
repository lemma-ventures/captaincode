package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A brain's index (catalog + dashboard) is rebuilt at every launch and on
// demand; a folder opened for the first time gets its repo brain scaffolded.
// The engine is a Python checkout, faked here with scripts that record where
// they ran.

func fakeEngine(t *testing.T) string {
	t.Helper()
	engine := t.TempDir()
	for _, p := range []string{"engine/build-catalog.py", "dashboard/build-dashboard.py"} {
		require.NoError(t, os.MkdirAll(filepath.Join(engine, filepath.Dir(p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(engine, p), []byte(
			"import os,sys\nroot=os.environ['EUCLID_ROOT']\nos.makedirs(os.path.join(root,'.euclid','index'),exist_ok=True)\n"+
				"open(os.path.join(root,'.euclid','index',os.path.basename(sys.argv[0])+'.ran'),'w').write(os.getcwd())\nprint('wrote')\n"), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(engine, "dashboard", "index.html"), []byte("<html>"), 0o644))
	t.Setenv("CAPTAIN_EUCLID_ENGINE", engine)
	return engine
}

func TestReindexRunsBothBuildersInTheBrainsHost(t *testing.T) {
	fakeEngine(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_HOME", filepath.Join(home, ".euclid"))
	_, err := Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)

	res := Reindex(filepath.Join(home, ".euclid"), 0)
	require.True(t, res.OK, "%+v", res)
	require.Len(t, res.Steps, 2)
	assert.Equal(t, "build-catalog", res.Steps[0].Step)
	assert.Equal(t, "build-dashboard", res.Steps[1].Step)
	ran, _ := os.ReadFile(filepath.Join(home, ".euclid", "index", "build-catalog.py.ran"))
	want, _ := filepath.EvalSymlinks(home)
	got, _ := filepath.EvalSymlinks(string(ran))
	assert.Equal(t, want, got, "the scripts run in the brain's host with EUCLID_ROOT set")
	assert.FileExists(t, filepath.Join(home, ".euclid", "dashboard", "index.html"), "the page is copied beside the data")
}

func TestReindexRefusesAnythingThatIsNotABrain(t *testing.T) {
	fakeEngine(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_HOME", filepath.Join(home, ".euclid"))
	assert.False(t, IsBrainRoot(t.TempDir()), "an arbitrary directory")
	assert.False(t, IsBrainRoot("relative/.euclid"))
	res := Reindex(t.TempDir(), 0)
	assert.False(t, res.OK)
	assert.Contains(t, res.Error, "not a Euclid brain")
}

func TestEnsureBrainsScaffoldsTheRepoBrainOnFirstLaunchAndIndexesBoth(t *testing.T) {
	fakeEngine(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_HOME", filepath.Join(home, ".euclid"))
	t.Setenv("EUCLID_HANDLE", "tester")
	t.Setenv("EUCLID_TEMPLATE_DIR", filepath.Join(home, "none"))
	repo := filepath.Join(home, "arc")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())

	rep := EnsureBrains(repo, nil)
	local, _ := filepath.EvalSymlinks(rep.Local) // git reports the resolved path on macOS
	wantLocal, _ := filepath.EvalSymlinks(filepath.Join(repo, ".euclid"))
	assert.Equal(t, wantLocal, local)
	assert.FileExists(t, filepath.Join(repo, ".euclid", "BRAIN.md"), "first launch: the repo brain exists")
	assert.FileExists(t, filepath.Join(repo, ".euclid", "developers", "tester", "BRAIN.md"), "…with the developer's subtree")
	assert.FileExists(t, filepath.Join(repo, ".euclid", "index", "build-catalog.py.ran"), "…and its index built")
	assert.FileExists(t, filepath.Join(home, ".euclid", "index", "build-catalog.py.ran"), "the main brain too")
	joined := ""
	for _, l := range rep.Lines {
		joined += l + "\n"
	}
	assert.Contains(t, joined, "created "+rep.Local)

	// Second launch: nothing new is created, both indexes are rebuilt again.
	os.Remove(filepath.Join(repo, ".euclid", "index", "build-catalog.py.ran"))
	rep = EnsureBrains(repo, nil)
	joined = ""
	for _, l := range rep.Lines {
		joined += l + "\n"
	}
	assert.NotContains(t, joined, "created")
	assert.FileExists(t, filepath.Join(repo, ".euclid", "index", "build-catalog.py.ran"), "every launch reindexes")
}

func TestEnsureBrainsOutsideARepositoryOnlyTouchesTheMainBrain(t *testing.T) {
	fakeEngine(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_HOME", filepath.Join(home, ".euclid"))
	t.Setenv("EUCLID_TEMPLATE_DIR", filepath.Join(home, "none"))
	dir := filepath.Join(home, "scratch")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	rep := EnsureBrains(dir, nil)
	assert.Empty(t, rep.Local)
	assert.NoDirExists(t, filepath.Join(dir, ".euclid"), "no repo, no repo brain")
	assert.FileExists(t, filepath.Join(home, ".euclid", "index", "build-catalog.py.ran"))
}

// The template's corpus is the lemma repo's (docs + .euclid, Python): a Rust
// repo scaffolded from it indexed nothing under its crate. The corpus is
// detected from the repository.
func TestScaffoldDetectsTheRepositoryCorpus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_TEMPLATE_DIR", filepath.Join(home, "none"))
	repo := filepath.Join(home, "arc")
	for _, p := range []string{"arc/src/merkle.rs", "arc/src/fri.rs", "docs/PLAN.md", "scripts/bench.py", "target/release/junk.rs", "node_modules/x/y.js"} {
		require.NoError(t, os.MkdirAll(filepath.Join(repo, filepath.Dir(p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(repo, p), []byte("x"), 0o644))
	}
	roots, exts := DetectCorpus(repo)
	assert.Equal(t, []string{"arc", "docs", "scripts", ".euclid"}, roots, "build output and vendored trees are not corpus")
	assert.Equal(t, []string{".rs", ".py"}, exts, "most frequent code extension first")

	cfg := "roots:\n  - docs\n  - .euclid\n\ncode_exts: [.py]\n\nregisters:\n  - name=SOUL\n"
	out := ApplyCorpus(cfg, roots, exts)
	assert.Contains(t, out, "roots:\n  - arc\n  - docs\n  - scripts\n  - .euclid\n")
	assert.Contains(t, out, "code_exts: [.rs, .py]\n")
	assert.Contains(t, out, "registers:\n  - name=SOUL\n", "every other key is left alone")

	_, err := Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	b, _ := os.ReadFile(filepath.Join(repo, ".euclid", "euclid.yml"))
	assert.Contains(t, string(b), "  - arc\n")
	assert.Contains(t, string(b), "code_exts: [.rs, .py]")
}
