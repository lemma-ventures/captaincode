package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDashboardSearchHonorsJournalFlag(t *testing.T) {
	host := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "-q", host).Run())
	root := filepath.Join(host, ".euclid")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "BRAIN.md"), []byte("brain\n"), 0o644))
	script := "import sys\nprint('\\n'.join(sys.argv[1:]))\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "search.py"), []byte(script), 0o644))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", "")

	b := &brain{}
	for _, journals := range []string{"false", "true"} {
		rec := httptest.NewRecorder()
		q := url.Values{
			"q": {"quokka"}, "lane": {"doc"}, "scope": {"docs"},
			"include_journals": {journals}, "limit": {"5"}, "root": {root},
		}
		req := httptest.NewRequest(http.MethodGet, "/api/search?"+q.Encode(), nil)
		b.euclidSearchHTTP(rec, req)
		require.Equal(t, 200, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, true, body["ok"])
		require.Equal(t, "search.py", body["tool"])
		text := body["text"].(string)
		require.Contains(t, text, "--scope")
		require.Contains(t, text, "docs")
		if journals == "true" {
			require.Contains(t, text, "--include-journals")
		} else {
			require.NotContains(t, text, "--include-journals")
		}
	}

	rec := httptest.NewRecorder()
	bad := url.Values{"q": {"quokka"}, "lane": {"doc"}, "include_journals": {"nope"}, "root": {root}}
	req := httptest.NewRequest(http.MethodGet, "/api/search?"+bad.Encode(), nil)
	b.euclidSearchHTTP(rec, req)
	require.Equal(t, 400, rec.Code)
	require.False(t, strings.Contains(rec.Body.String(), "quokka"))
}
