package main

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func euclidDashboardPath(root string) string {
	return "/euclid/dashboard/" + base64.RawURLEncoding.EncodeToString([]byte(root)) + "/index.html"
}

func (b *brain) euclidDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErr(w, 405, "GET or HEAD only")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/euclid/dashboard/"), "/", 2)
	if len(parts) != 2 {
		writeErr(w, 404, "dashboard not found")
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	root := string(decoded)
	if err != nil || !filepath.IsAbs(root) || !captaincode.IsBrainRoot(root) {
		writeErr(w, 404, "dashboard not found")
		return
	}
	rel := parts[1]
	allowed := false
	switch rel {
	case "index.html", "app.js", "style.css", "data.js", "data.json", "catalog-lite.js", "graph-3d.js":
		allowed = true
	default:
		name := strings.TrimPrefix(rel, "bodies/")
		allowed = strings.HasPrefix(rel, "bodies/shard-") && strings.HasSuffix(name, ".js") && !strings.ContainsAny(name, "/\\")
	}
	if !allowed {
		writeErr(w, 404, "dashboard file not found")
		return
	}
	dash, err := filepath.EvalSymlinks(filepath.Join(root, "dashboard"))
	rootReal, rootErr := filepath.EvalSymlinks(root)
	if err != nil || rootErr != nil || !strings.HasPrefix(dash, rootReal+string(filepath.Separator)) {
		writeErr(w, 404, "dashboard not built")
		return
	}
	full, err := filepath.EvalSymlinks(filepath.Join(dash, rel))
	if err != nil || !strings.HasPrefix(full, dash+string(filepath.Separator)) {
		writeErr(w, 404, "dashboard file not found")
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeErr(w, 404, "dashboard file not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 8<<20 {
		writeErr(w, 404, "dashboard file not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.ServeContent(w, r, rel, st.ModTime(), f)
}
