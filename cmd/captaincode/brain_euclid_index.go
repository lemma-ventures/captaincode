package main

// The Euclid dashboard's Regenerate button talks to a "local engine": it
// probes GET /api/ping and POSTs /api/regenerate, rendering the returned
// steps. Euclid ships dashboard-serve.py for that, one process per brain on
// port 8787. The brain is already a local daemon with every brain in reach,
// so it answers the same contract for any brain: the page names the root it
// was built from (?root=…, derived from its own file:// path), the sidebar
// names the folder (?cwd=). Rebuilding an index is the only thing these
// endpoints can do, and only for a directory that is a brain (IsBrainRoot).

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// corsForDashboard lets the dashboard page call the brain. A file:// page
// sends Origin "null"; a page served by dashboard-serve.py or the brain
// itself comes from 127.0.0.1. Nothing else is answered.
func corsForDashboard(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	ok := origin == "" || origin == "null" ||
		strings.HasPrefix(origin, "http://127.0.0.1") || strings.HasPrefix(origin, "http://localhost")
	if !ok {
		writeErr(w, 403, "origin not allowed")
		return false
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(204)
		return false
	}
	return true
}

// euclidPing: the dashboard's liveness probe.
func (b *brain) euclidPing(w http.ResponseWriter, r *http.Request) {
	if !corsForDashboard(w, r) {
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "engine": "captain", "brains": b.reindexTargets(r)})
}

// reindexTargets resolves which brains a request means: ?root= (the
// dashboard names its own brain), else the folder's local brain and the
// main brain (the sidebar, `captain euclid reindex`), narrowed by
// ?brain=main|local.
func (b *brain) reindexTargets(r *http.Request) []string {
	if root := strings.TrimSpace(r.URL.Query().Get("root")); root != "" {
		root = filepath.Clean(root)
		if captaincode.IsBrainRoot(root) {
			return []string{root}
		}
		return nil
	}
	ws := workspaceOf(r)
	var out []string
	which := r.URL.Query().Get("brain")
	if which == "" || which == "main" || which == "all" {
		if mb := captaincode.MainBrainPath(); captaincode.IsBrainRoot(mb) {
			out = append(out, mb)
		}
	}
	if which == "" || which == "local" || which == "all" {
		if repo := captaincode.RepoRoot(ws.Dir); repo != "" {
			if shared := filepath.Join(repo, ".euclid"); captaincode.IsBrainRoot(shared) {
				out = append(out, shared)
			}
		}
	}
	return out
}

// euclidReindex: POST /api/regenerate and /v1/euclid/reindex. Rebuilds the
// catalog and dashboard of each target; the response is the dashboard's
// Regenerate contract (ok, steps, finished_at) for the last target, plus
// every result under "results".
func (b *brain) euclidReindex(w http.ResponseWriter, r *http.Request) {
	if !corsForDashboard(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST only")
		return
	}
	// ?create=1 (the sidebar's "memory: <repo> (create)" link): scaffold the
	// folder's repo brain first and fill its registers from the repo's docs,
	// so the first click yields a brain, not an error.
	if r.URL.Query().Get("create") == "1" {
		ws := workspaceOf(r)
		if repo := captaincode.RepoRoot(ws.Dir); repo != "" && !captaincode.IsBrainRoot(filepath.Join(repo, ".euclid")) {
			captaincode.EnsureBrains(ws.Dir, nil)
			if _, _, err := b.bootstrap(ws, true); err != nil {
				fmt.Printf("captain brain: euclid bootstrap of %s: %v\n", repo, err)
			}
		}
	}
	targets := b.reindexTargets(r)
	if len(targets) == 0 {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "no Euclid brain for this request (?root= must be a brain directory; ?cwd= a folder with one)", "steps": []any{}, "finished_at": time.Now().Format(time.RFC3339)})
		return
	}
	var results []captaincode.IndexResult
	ok := true
	var steps []captaincode.IndexStep
	for _, root := range targets {
		res := captaincode.Reindex(root, 5*time.Minute)
		results = append(results, res)
		ok = ok && res.OK
		steps = append(steps, res.Steps...)
	}
	out := map[string]any{"ok": ok, "steps": steps, "finished_at": time.Now().Format(time.RFC3339), "results": results}
	if !ok {
		for _, res := range results {
			if res.Error != "" {
				out["error"] = res.Error
			}
		}
	}
	writeJSON(w, 200, out)
}

// The page switches to its "local engine" tier as a whole once /api/ping
// answers: the document viewer reads through /api/file and the search box
// may use /api/search|ask|git. Answering ping without the rest turned every
// document click into "read failed." (arc, 2026-09-13). The brain serves the
// whole contract of dashboard-serve.py, for any brain the page names.

// apiRoot resolves the brain a read-only request is about: ?root= (the page
// names its own), else the folder's repo brain, else the main brain.
func (b *brain) apiRoot(r *http.Request) string {
	if root := strings.TrimSpace(r.URL.Query().Get("root")); root != "" {
		root = filepath.Clean(root)
		if captaincode.IsBrainRoot(root) {
			return root
		}
		return ""
	}
	ws := workspaceOf(r)
	if repo := captaincode.RepoRoot(ws.Dir); repo != "" {
		if shared := filepath.Join(repo, ".euclid"); captaincode.IsBrainRoot(shared) {
			return shared
		}
	}
	if mb := captaincode.MainBrainPath(); captaincode.IsBrainRoot(mb) {
		return mb
	}
	return ""
}

// euclidFile: GET /api/file?path=<repo-relative>&root=<brain> - one readable
// text file of the brain's host, never outside it.
func (b *brain) euclidFile(w http.ResponseWriter, r *http.Request) {
	if !corsForDashboard(w, r) {
		return
	}
	root := b.apiRoot(r)
	if root == "" {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "no Euclid brain for this request"})
		return
	}
	host := filepath.Dir(root)
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		writeJSON(w, 403, map[string]any{"ok": false, "error": "refused (out of repo / not a readable file)"})
		return
	}
	full, err := filepath.EvalSymlinks(filepath.Join(host, rel))
	hostReal, _ := filepath.EvalSymlinks(host)
	if err != nil || hostReal == "" || !strings.HasPrefix(full, hostReal+string(filepath.Separator)) {
		writeJSON(w, 403, map[string]any{"ok": false, "error": "refused (out of repo / not a readable file)"})
		return
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() || st.Size() > 8<<20 {
		writeJSON(w, 403, map[string]any{"ok": false, "error": "refused (out of repo / not a readable file)"})
		return
	}
	content, err := os.ReadFile(full)
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": rel, "content": string(content)})
}

// euclidSearchHTTP: GET /api/search|/api/ask?q=&lane=&limit= (ask.py) and
// /api/git?q= (git-recall.py), in dashboard-serve.py's shape.
func (b *brain) euclidSearchHTTP(w http.ResponseWriter, r *http.Request) {
	if !corsForDashboard(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "missing q"})
		return
	}
	root := b.apiRoot(r)
	if root == "" {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "no Euclid brain for this request"})
		return
	}
	host := filepath.Dir(root)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var text, tool, lane string
	var err error
	if strings.HasSuffix(r.URL.Path, "/git") {
		tool, lane = "git-recall.py", "git"
		text, err = captaincode.EngineRecall(host, q, "", limit)
	} else {
		tool, lane = "ask.py", strings.TrimSpace(r.URL.Query().Get("lane"))
		switch lane {
		case "doc", "code", "git", "both", "all":
		default:
			lane = "both"
		}
		text, err = captaincode.EngineAsk(host, q, lane, limit)
	}
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "tool": tool, "lane": lane, "query": q, "text": text, "paths": pathsIn(text)})
}

var relPathRe = regexp.MustCompile(`(?m)(?:^|[\s(\[])((?:docs|src|journal|scripts|arc|packages|tools|apps|\.euclid)/[^\s):,]+\.[A-Za-z0-9]{1,6})`)

// pathsIn lists the repo-relative paths an engine report mentions, in order,
// once each.
func pathsIn(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range relPathRe.FindAllStringSubmatch(text, -1) {
		p := m[1]
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}
