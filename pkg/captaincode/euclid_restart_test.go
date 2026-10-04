package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestartRejectsRevisionWithoutConfiguredRetrieval(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "captaincode.sh"))
	require.NoError(t, err)
	text := string(raw)
	start := strings.Index(text, "check_retrieval_ref() {")
	require.GreaterOrEqual(t, start, 0)
	end := strings.Index(text[start:], "\n}\n")
	require.Greater(t, end, 0)
	function := text[start : start+end+3]
	repo := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "-q", repo).Run())
	path := filepath.Join(repo, "pkg", "captaincode", "euclid_engine.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	commit := func(body string) {
		require.NoError(t, os.WriteFile(path, []byte(body), 0600))
		require.NoError(t, exec.Command("git", "-C", repo, "add", ".").Run())
		out, err := exec.Command("git", "-C", repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture").CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run := func(config string) (string, error) {
		cmd := exec.Command("bash", "-c", function+"\ncheck_retrieval_ref HEAD")
		cmd.Env = append(os.Environ(), "SCRIPT_DIR="+repo, "CAPTAIN_EUCLID_MCP_CONFIG="+config)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	commit("package captaincode\n")
	_, err = run("")
	require.NoError(t, err)
	out, err := run("/fixture/euclid.json")
	require.Error(t, err)
	require.Contains(t, out, "--checkout")
	require.Contains(t, out, "installed brain is unchanged")
	commit("package captaincode\n// CAPTAIN_EUCLID_MCP_CONFIG\nfunc EngineSearchOptions() {}\n")
	_, err = run("/fixture/euclid.json")
	require.NoError(t, err)
	require.Less(t, strings.Index(text, "if ! check_retrieval_ref"), strings.Index(text, "git clone -q --shared"), "reject before compiling or installing the selected revision")
}
