package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The checks the git hooks run, against a real repository: staged lines and
// paths, a commit message, and every commit a push would send.
func TestLeakcheckRefusesPrivateNamesInWhatGitWouldPublish(t *testing.T) {
	if os.Getenv("CAPTAIN_LEAKCHECK_CHILD") != "" {
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
	run := func(args string) (int, string) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLeakcheckRefusesPrivateNamesInWhatGitWouldPublish$")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "CAPTAIN_LEAKCHECK_CHILD=1", "CAPTAIN_LEAKCHECK_ARGS="+args, "CAPTAIN_PRIVATE_NAMES="+list)
		out, _ := cmd.CombinedOutput()
		return cmd.ProcessState.ExitCode(), string(out)
	}
	git("init", "-q")
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
