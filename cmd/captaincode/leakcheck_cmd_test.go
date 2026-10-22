package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The checks the git hooks run, against a real repository: staged lines and
// paths, a commit message, and every commit a push would send.
func TestLeakcheckRefusesPrivateNamesInWhatGitWouldPublish(t *testing.T) {
	if os.Getenv("CAPTAIN_LEAKCHECK_CHILD") != "" {
		os.Setenv("HOME", os.Getenv("CAPTAIN_LEAKCHECK_HOME")) // TestMain gave the child a HOME of its own
		cmdLeakcheck(strings.Fields(os.Getenv("CAPTAIN_LEAKCHECK_ARGS")))
		return
	}
	list := filepath.Join(t.TempDir(), "names")
	require.NoError(t, os.WriteFile(list, []byte("zephyrine\n"), 0o600))
	repo := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	home := t.TempDir()
	run := func(args string) (int, string) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLeakcheckRefusesPrivateNamesInWhatGitWouldPublish$")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "CAPTAIN_LEAKCHECK_HOME="+home, "CAPTAIN_LEAKCHECK_CHILD=1", "CAPTAIN_LEAKCHECK_ARGS="+args, "CAPTAIN_PRIVATE_NAMES="+list)
		out, _ := cmd.CombinedOutput()
		return cmd.ProcessState.ExitCode(), string(out)
	}
	git("init", "-q")
	git("remote", "add", "origin", "https://github.com/acme/open-tool.git")
	seedVisibility(t, home, map[string]string{filepath.Dir(git("rev-parse", "--path-format=absolute", "--git-common-dir")): "public"})
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.go"), []byte("// a loop fix\n"), 0o644))
	git("add", "a.go")
	code, _ := run("--staged")
	assert.Equal(t, 0, code, "a neutral change passes")
	git("commit", "-q", "-m", "fix the loop")
	base := git("rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.go"), []byte("// a loop fix seen in Zephyrine\n"), 0o644))
	git("add", "a.go")
	code, out := run("--staged")
	assert.Equal(t, 3, code, "a staged line naming a private project is refused")
	assert.Contains(t, out, `names "zephyrine"`)

	msg := filepath.Join(t.TempDir(), "msg")
	require.NoError(t, os.WriteFile(msg, []byte("fix the zephyrine loop\n"), 0o644))
	code, _ = run("--message-file " + msg)
	assert.Equal(t, 3, code, "so is a commit message")

	git("commit", "-q", "-m", "fix the loop again")
	code, out = run("--range " + base + "..HEAD")
	assert.Equal(t, 3, code, "and a commit a push would send")
	assert.Contains(t, out, "diff")
}

// seedVisibility writes the visibility cache GitHub's answers are kept in,
// so the checks below never ask GitHub.
func seedVisibility(t *testing.T, home string, vis map[string]string) {
	t.Helper()
	cache := map[string]map[string]any{}
	for k, v := range vis {
		cache[k] = map[string]any{"vis": v, "at": time.Now()}
	}
	b, err := json.Marshal(cache)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".captaincode"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".captaincode", "repo-visibility.json"), b, 0o600))
}

// A private repository names itself and the user's other private projects;
// what leaves it for a public repository is still checked.
func TestLeakcheckLetsAPrivateRepositoryNameItsOwnProjects(t *testing.T) {
	if os.Getenv("CAPTAIN_LEAKCHECK_CHILD") != "" {
		os.Setenv("HOME", os.Getenv("CAPTAIN_LEAKCHECK_HOME")) // TestMain gave the child a HOME of its own
		cmdLeakcheck(strings.Fields(os.Getenv("CAPTAIN_LEAKCHECK_ARGS")))
		return
	}
	list := filepath.Join(t.TempDir(), "names")
	require.NoError(t, os.WriteFile(list, []byte("zephyrine\nquillon\n"), 0o600))
	repo, home := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run := func(stdin, args string) (int, string) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLeakcheckLetsAPrivateRepositoryNameItsOwnProjects$")
		cmd.Dir = repo
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = append(os.Environ(), "CAPTAIN_LEAKCHECK_HOME="+home, "CAPTAIN_LEAKCHECK_CHILD=1", "CAPTAIN_LEAKCHECK_ARGS="+args, "CAPTAIN_PRIVATE_NAMES="+list)
		out, _ := cmd.CombinedOutput()
		return cmd.ProcessState.ExitCode(), string(out)
	}
	git("init", "-q")
	git("remote", "add", "origin", "https://github.com/acme/zephyrine.git")
	git("remote", "add", "mirror", "https://github.com/acme/open-mirror.git")
	seedVisibility(t, home, map[string]string{
		filepath.Dir(git("rev-parse", "--path-format=absolute", "--git-common-dir")): "private",
		"https://github.com/acme/zephyrine.git":                                      "private",
		"https://github.com/acme/open-mirror.git":                                    "public",
	})
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("# zephyrine\nreads results from quillon\n"), 0o644))
	git("add", "README.md")

	code, out := run("", "--staged")
	assert.Equal(t, 0, code, "its own name and a sibling private project pass in a private repository")
	assert.Contains(t, out, "nothing checked")
	code, _ = run("", "--public --staged")
	assert.Equal(t, 3, code, "--public checks text bound for a public repository")

	git("commit", "-q", "-m", "zephyrine readme")
	head := git("rev-parse", "HEAD")
	refs := "refs/heads/main " + head + " refs/heads/main 0000000000000000000000000000000000000000\n"
	code, _ = run(refs, "--pre-push origin https://github.com/acme/zephyrine.git")
	assert.Equal(t, 0, code, "a push to the private remote passes")
	code, out = run(refs, "--pre-push mirror https://github.com/acme/open-mirror.git")
	assert.Equal(t, 3, code, "a push to a public remote is checked")
	assert.Contains(t, out, `names "zephyrine"`)
}
