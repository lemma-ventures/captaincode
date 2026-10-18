package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardSameOriginView(t *testing.T) {
	home := euclidTestHome(t)
	root := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(root, "main")
	require.NoError(t, err)
	dash := filepath.Join(root, "dashboard")
	require.NoError(t, os.MkdirAll(filepath.Join(dash, "bodies"), 0o755))
	for name, data := range map[string]string{"index.html": "<script src=\"app.js\"></script>", "app.js": "window.ready=true;", "bodies/shard-a.js": "window.body=true;"} {
		require.NoError(t, os.WriteFile(filepath.Join(dash, name), []byte(data), 0o644))
	}
	engine := t.TempDir()
	for _, name := range []string{"engine/build-catalog.py", "dashboard/build-dashboard.py"} {
		require.NoError(t, os.MkdirAll(filepath.Join(engine, filepath.Dir(name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(engine, name), []byte("print('ok')\n"), 0o644))
	}
	t.Setenv("CAPTAIN_EUCLID_ENGINE", engine)
	b := teamBrain()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ping", b.euclidPing)
	mux.HandleFunc("/api/regenerate", b.euclidReindex)
	mux.HandleFunc("/euclid/dashboard/", b.euclidDashboard)
	h := browserGuard(mux)
	request := func(method, path, origin, site string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Sec-Fetch-Site", site)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	ping := request("GET", "/api/ping?root="+root, "null", "cross-site")
	require.Equal(t, 200, ping.Code)
	var info struct {
		DashboardPath string `json:"dashboard_path"`
		SameOrigin    bool   `json:"regenerate_same_origin"`
	}
	require.NoError(t, json.Unmarshal(ping.Body.Bytes(), &info))
	assert.True(t, info.SameOrigin)
	want := "/euclid/dashboard/" + base64.RawURLEncoding.EncodeToString([]byte(root)) + "/index.html"
	require.Equal(t, want, info.DashboardPath)
	page := request("GET", want, "", "none")
	require.Equal(t, 200, page.Code)
	assert.Contains(t, page.Body.String(), "app.js")
	assert.Equal(t, "no-store", page.Header().Get("Cache-Control"))
	assert.Contains(t, page.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
	for _, name := range []string{"app.js", "bodies/shard-a.js"} {
		asset := filepath.Dir(want) + "/" + name
		assert.Equal(t, 200, request("GET", asset, "", "same-origin").Code)
	}
	assert.Equal(t, 403, request("POST", "/api/regenerate?root="+root, "null", "cross-site").Code)
	assert.Equal(t, 403, request("POST", "/api/regenerate?root="+root, "https://evil.example", "cross-site").Code)
	res := request("POST", "/api/regenerate?root="+root, "http://127.0.0.1:14097", "same-origin")
	require.Equal(t, 200, res.Code, res.Body.String())
	assert.Contains(t, res.Body.String(), `"ok":true`)
	assert.Contains(t, res.Body.String(), "build-dashboard.py")
}

func TestDashboardStaticBoundary(t *testing.T) {
	home := euclidTestHome(t)
	root := filepath.Join(home, ".euclid")
	_, err := captaincode.Scaffold(root, "main")
	require.NoError(t, err)
	dash := filepath.Join(root, "dashboard")
	require.NoError(t, os.MkdirAll(dash, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dash, "index.html"), []byte("page"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(root, "BRAIN.md"), filepath.Join(dash, "data.js")))
	prefix := "/euclid/dashboard/" + base64.RawURLEncoding.EncodeToString([]byte(root)) + "/"
	b := teamBrain()
	for _, name := range []string{"../BRAIN.md", "%2e%2e/BRAIN.md", "BRAIN.md", "data.js", "bodies/../index.html", "bodies/shard-a.js", "index.html/extra"} {
		rec := httptest.NewRecorder()
		b.euclidDashboard(rec, httptest.NewRequest("GET", prefix+name, nil))
		assert.NotEqual(t, 200, rec.Code, name)
	}
	for _, method := range []string{"POST", "DELETE"} {
		rec := httptest.NewRecorder()
		b.euclidDashboard(rec, httptest.NewRequest(method, prefix+"index.html", nil))
		assert.Equal(t, 405, rec.Code)
	}
	rec := httptest.NewRecorder()
	b.euclidDashboard(rec, httptest.NewRequest("GET", "/euclid/dashboard/invalid/index.html", nil))
	assert.Equal(t, 404, rec.Code)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "index.html"), []byte("outside"), 0o644))
	otherRoot := filepath.Join(t.TempDir(), ".euclid")
	_, err = captaincode.Scaffold(otherRoot, "repo")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(otherRoot, "dashboard")))
	require.NoError(t, os.Symlink(outside, filepath.Join(otherRoot, "dashboard")))
	rec = httptest.NewRecorder()
	b.euclidDashboard(rec, httptest.NewRequest("GET", euclidDashboardPath(otherRoot), nil))
	assert.Equal(t, 404, rec.Code)
}
