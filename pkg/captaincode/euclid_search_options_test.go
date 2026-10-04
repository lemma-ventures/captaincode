package captaincode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEngineSearchOptionsForwarded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", "")
	cmd := exec.Command("git", "init", "-q", root)
	require.NoError(t, cmd.Run())
	bin := filepath.Join(root, ".euclid", "bin")
	require.NoError(t, os.MkdirAll(bin, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "search.py"), []byte("import sys,json\nprint(json.dumps(sys.argv[1:]))\n"), 0600))
	for _, journals := range []bool{false, true} {
		out, ok := EngineSearchOptions(root, "--query-is-data", "doc", 3, SearchOptions{Scope: "docs", IncludeJournals: journals})
		require.True(t, ok)
		var args []string
		require.NoError(t, json.Unmarshal([]byte(out), &args))
		expected := []string{"--limit", "3", "--lane", "doc", "--scope", "docs"}
		if journals {
			expected = append(expected, "--include-journals")
		}
		expected = append(expected, "--", "--query-is-data")
		require.Equal(t, expected, args)
	}
	_, ok := EngineSearchOptions(root, "test", "doc", 5, SearchOptions{Scope: "unknown"})
	require.False(t, ok)
}

func TestEngineFailureDoesNotReturnPartialResults(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "search.py")
	require.NoError(t, os.WriteFile(script, []byte("import sys\nprint('partial result')\nsys.exit(2)\n"), 0600))
	out, err := runEngine(root, script, nil, time.Second)
	require.Error(t, err)
	require.Empty(t, out)
}
func TestWorkerMCPForwardsOnlyRetrievalSettings(t *testing.T) {
	repo, _ := repoWithBrain(t)
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", "/trusted/euclid.json")
	t.Setenv("CAPTAIN_EUCLID_ENGINE", "/trusted/engine")
	t.Setenv("EUCLID_PROVIDER_MCP", "/trusted/codeintel.json")
	t.Setenv("CAPTAIN_RETRIEVAL_LOG", "off")
	t.Setenv("EUCLID_ROOT", "/wrong/workspace")
	t.Setenv("OPENAI_API_KEY", "fixture-secret")
	_, _, env, ok := euclidMCPServer(repo)
	require.True(t, ok)
	require.Equal(t, map[string]string{
		"CAPTAIN_CWD":               repo,
		"CAPTAIN_EUCLID_MCP_CONFIG": "/trusted/euclid.json",
		"CAPTAIN_EUCLID_ENGINE":     "/trusted/engine",
		"EUCLID_PROVIDER_MCP":       "/trusted/codeintel.json",
		"CAPTAIN_RETRIEVAL_LOG":     "off",
	}, env)
	for _, args := range [][]string{ClaudeMCPArgs(repo), CodexMCPArgs(repo)} {
		value := strings.Join(args, " ")
		require.Contains(t, value, "/trusted/euclid.json")
		require.NotContains(t, value, "fixture-secret")
		require.NotContains(t, value, "/wrong/workspace")
	}
	require.Equal(t, CodexMCPArgs(repo), CodexMCPArgs(repo))
}
