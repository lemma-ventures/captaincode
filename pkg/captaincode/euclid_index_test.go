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
	repo := filepath.Join(home, "ash")
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

// The rebuild runs the brain's OWN copy of the build scripts, so the command
// the dashboard prints for a manual run - `python3 .euclid/bin/build-dashboard.py`
// - is the command that ran, and a brain still builds its dashboard when the
// engine checkout is not on the machine.
func TestReindexVendorsTheScriptsIntoTheBrainsBin(t *testing.T) {
	engine := fakeEngine(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_HOME", filepath.Join(home, ".euclid"))
	_, err := Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	root := filepath.Join(home, ".euclid")

	res := Reindex(root, 0)
	require.True(t, res.OK, "%+v", res)
	require.Len(t, res.Steps, 2)
	assert.FileExists(t, filepath.Join(root, "bin", "build-catalog.py"))
	assert.FileExists(t, filepath.Join(root, "bin", "build-dashboard.py"))
	assert.FileExists(t, filepath.Join(root, "bin", "index.html"), "the page is vended beside the scripts")
	assert.Equal(t, ".euclid/bin/build-catalog.py", res.Steps[0].Script, "the report names the host-relative command")
	assert.Equal(t, ".euclid/bin/build-dashboard.py", res.Steps[1].Script)
	assert.FileExists(t, filepath.Join(root, "dashboard", "index.html"), "…and the page still lands beside the data")

	// One the brain kept: the copy is newer than the engine's, so it is not
	// touched.
	pin := filepath.Join(root, "bin", "build-dashboard.py")
	require.NoError(t, os.WriteFile(pin, []byte("# pinned by this brain\n"), 0o644))
	n, err := vendorScripts(root)
	require.NoError(t, err)
	assert.Zero(t, n, "an engine that has not moved past the copy does not overwrite it")
	b, err := os.ReadFile(pin)
	require.NoError(t, err)
	assert.Equal(t, "# pinned by this brain\n", string(b))

	// The engine moving ahead refreshes the copy: the brain runs the build
	// that produced the page it is about to read.
	stale := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(pin, stale, stale))
	require.NoError(t, os.Chtimes(filepath.Join(engine, "dashboard", "build-dashboard.py"), time.Now(), time.Now()))
	n, err = vendorScripts(root)
	require.NoError(t, err)
	assert.Greater(t, n, 0, "a build script the engine rewrote is copied forward")
	b, err = os.ReadFile(pin)
	require.NoError(t, err)
	assert.Contains(t, string(b), "import os", "the refreshed copy is the engine's script again")

	// A repository brain that commits its registers must not commit the
	// vendored scripts with them.
	repo := filepath.Join(home, "src", "widget")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	_, err = Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	n, err = vendorScripts(filepath.Join(repo, ".euclid"))
	require.NoError(t, err)
	assert.Greater(t, n, 0)
	ignore, err := os.ReadFile(filepath.Join(repo, ".euclid", ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(ignore), "bin/\n", "vendored scripts are derived, like index/ and dashboard/")
}

// The Euclid checkout carries its own brain: the dashboard of
// <euclid>/.euclid is built by the engine in that same checkout. With no
// CAPTAIN_EUCLID_ENGINE the Regenerate button still ran nothing ("no Euclid
// engine found", 2026-10-04). The host's own engine is vended into the
// brain's bin and `python3 .euclid/bin/build-dashboard.py` runs; no other
// directory is searched.
func TestReindexUsesTheEngineOfTheBrainsOwnHost(t *testing.T) {
	host := fakeEngine(t)
	t.Setenv("CAPTAIN_EUCLID_ENGINE", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_HOME", filepath.Join(home, ".euclid"))
	root := filepath.Join(host, ".euclid")
	_, err := Scaffold(root, "repo")
	require.NoError(t, err)

	res := Reindex(root, 0)
	require.True(t, res.OK, "%+v", res)
	require.Len(t, res.Steps, 2)
	assert.Equal(t, ".euclid/bin/build-catalog.py", res.Steps[0].Script)
	assert.Equal(t, ".euclid/bin/build-dashboard.py", res.Steps[1].Script)
	assert.FileExists(t, filepath.Join(host, ".euclid", "index", "build-dashboard.py.ran"))

	// A brain whose host is not an engine checkout still needs the
	// configured engine: nothing beside it is searched.
	other := filepath.Join(home, "src", "widget", ".euclid")
	_, err = Scaffold(other, "repo")
	require.NoError(t, err)
	res = Reindex(other, 0)
	assert.False(t, res.OK)
	assert.Contains(t, res.Error, "no Euclid engine found")
}

// The template's corpus is the axiom repo's (docs + .euclid, Python): a Rust
// repo scaffolded from it indexed nothing under its crate. The corpus is
// detected from the repository.
func TestScaffoldDetectsTheRepositoryCorpus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EUCLID_TEMPLATE_DIR", filepath.Join(home, "none"))
	repo := filepath.Join(home, "ash")
	for _, p := range []string{"ash/src/merkle.rs", "ash/src/fri.rs", "docs/PLAN.md", "scripts/bench.py", "target/release/junk.rs", "node_modules/x/y.js"} {
		require.NoError(t, os.MkdirAll(filepath.Join(repo, filepath.Dir(p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(repo, p), []byte("x"), 0o644))
	}
	roots, exts := DetectCorpus(repo)
	assert.Equal(t, []string{"ash", "docs", "scripts", ".euclid"}, roots, "build output and vendored trees are not corpus")
	assert.Equal(t, []string{".rs", ".py"}, exts, "most frequent code extension first")

	cfg := "roots:\n  - docs\n  - .euclid\n\ncode_exts: [.py]\n\nregisters:\n  - name=SOUL\n"
	out := ApplyCorpus(cfg, roots, exts)
	assert.Contains(t, out, "roots:\n  - ash\n  - docs\n  - scripts\n  - .euclid\n")
	assert.Contains(t, out, "code_exts: [.rs, .py]\n")
	assert.Contains(t, out, "registers:\n  - name=SOUL\n", "every other key is left alone")

	_, err := Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	b, _ := os.ReadFile(filepath.Join(repo, ".euclid", "euclid.yml"))
	assert.Contains(t, string(b), "  - ash\n")
	assert.Contains(t, string(b), "code_exts: [.rs, .py]")
}
