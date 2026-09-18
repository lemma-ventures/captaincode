package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One brain, several captain-code TUIs, each open in its own repo. The brain
// used to follow one CAPTAIN_CWD: with DLM and arc open, the DLM sidebar
// showed arc's memory link and, worse, DLM's workers ran in arc (2026-09-12).
// Every request now names its folder; the brain never moves.

func TestWorkspaceOf_HeaderThenQueryThenDefault(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	t.Setenv("CAPTAIN_CWD", other)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Captain-Cwd", dir)
	assert.Equal(t, dir, workspaceOf(r).Dir, "the TUI's provider header wins")

	r = httptest.NewRequest(http.MethodGet, "/v1/euclid/status?cwd="+dir, nil)
	assert.Equal(t, dir, workspaceOf(r).Dir, "the sidebar plugin passes its folder as a query")

	r = httptest.NewRequest(http.MethodGet, "/v1/euclid/status", nil)
	assert.Equal(t, other, workspaceOf(r).Dir, "no folder named: the brain's own default")

	r = httptest.NewRequest(http.MethodGet, "/v1/euclid/status", nil)
	r.Header.Set("X-Captain-Cwd", filepath.Join(dir, "does-not-exist"))
	assert.Equal(t, other, workspaceOf(r).Dir, "a folder that does not exist is ignored, never a surprise cwd for a worker")
	r.Header.Set("X-Captain-Cwd", "relative/path")
	assert.Equal(t, other, workspaceOf(r).Dir, "a relative path is ignored")
}

func TestChatWorkerRunsInTheRequestsWorkspace(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	t.Setenv("CAPTAIN_WORKER_LOGS", "0")
	brainHome := t.TempDir()
	t.Setenv("CAPTAIN_CWD", brainHome) // where the brain was launched: NOT where this TUI works
	arc := t.TempDir()

	b := teamBrain()
	var seen string
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		seen = brief
		return leg, captaincode.Result{Text: "done"}, nil
	}
	body, _ := json.Marshal(oaiChatReq{Model: "free", Messages: []oaiMessage{{Role: "user", Content: json.RawMessage(`"list the files"`)}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("X-Captain-Cwd", arc)
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, seen, "repository at "+arc, "the worker is told it works in the TUI's folder")
	assert.NotContains(t, seen, "repository at "+brainHome)
}

func TestEuclidStatusIsPerWorkspace(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	// A second project with its own repo brain.
	repo := filepath.Join(home, "arc")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	_, err = captaincode.Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)

	b := teamBrain()
	get := func(cwd string) euclidStatus {
		rec := httptest.NewRecorder()
		b.euclidStatusHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/euclid/status?cwd="+cwd, nil))
		require.Equal(t, 200, rec.Code)
		var st euclidStatus
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
		return st
	}
	arc := get(repo)
	assert.Equal(t, repo, arc.Cwd)
	assert.True(t, hasBrainKind(arc.Brains, "repo"), "the arc TUI sees arc's own brain")
	plain := get(home)
	assert.Equal(t, home, plain.Cwd)
	assert.False(t, hasBrainKind(plain.Brains, "repo"), "the other TUI does not see arc's brain")
}

func hasBrainKind(brains []captaincode.EuclidBrain, kind string) bool {
	for _, br := range brains {
		if br.Kind == kind {
			return true
		}
	}
	return false
}

func TestDetachedRoundKeepsItsWorkspace(t *testing.T) {
	dir := t.TempDir()
	r := chatRequestFrom(t.Context(), oaiChatReq{Model: "free", ws: captaincode.Workspace{Dir: dir}})
	assert.Equal(t, dir, r.Header.Get("X-Captain-Cwd"), "a /repeat or /parallel round runs where the TUI that started it works")
}

func TestSidebarSeesOnlyItsOwnWorkspace(t *testing.T) {
	dlm := t.TempDir()
	arc := t.TempDir()
	b := teamBrain()
	b.active.begin(captaincode.Workspace{Dir: arc}, captaincode.LegGrok, "[user]\noptimize arc")
	b.pushActivity(activity{Dir: arc, Kind: "run", Leg: "grok", Text: "optimize arc"})
	b.pushActivity(activity{Dir: dlm, Kind: "run", Leg: "free", Text: "review dlm"})
	b.pushActivity(activity{Kind: "route", Leg: "director", Text: "director recovered"}) // machine-wide
	b.mu.Lock()
	b.last = &lastRoute{Task: "optimize arc", Leg: "grok", Dir: arc}
	b.lastBy = map[string]*lastRoute{arc: b.last}
	b.mu.Unlock()

	// DLM's sidebar: arc's busy grok is not its worker, arc's run is not its activity.
	rec := httptest.NewRecorder()
	b.workers(rec, httptest.NewRequest(http.MethodGet, "/v1/workers?cwd="+dlm, nil))
	var wr workersResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &wr))
	for _, row := range wr.Workers {
		assert.NotEqual(t, "busy", row.Status, "DLM must not show arc's %s as busy", row.Leg)
	}
	rec = httptest.NewRecorder()
	b.activityFeed(rec, httptest.NewRequest(http.MethodGet, "/v1/activity?cwd="+dlm, nil))
	assert.Contains(t, rec.Body.String(), "review dlm")
	assert.Contains(t, rec.Body.String(), "director recovered", "machine-wide entries reach every TUI")
	assert.NotContains(t, rec.Body.String(), "optimize arc")
	rec = httptest.NewRecorder()
	b.stats(rec, httptest.NewRequest(http.MethodGet, "/v1/stats?cwd="+dlm, nil))
	assert.Contains(t, rec.Body.String(), `"last":null`, "no route yet for DLM - not arc's")

	// arc's sidebar sees its run; the dashboard (no folder) sees everything.
	rec = httptest.NewRecorder()
	b.workers(rec, httptest.NewRequest(http.MethodGet, "/v1/workers?cwd="+arc, nil))
	assert.Contains(t, rec.Body.String(), `"busy":1`)
	rec = httptest.NewRecorder()
	b.activityFeed(rec, httptest.NewRequest(http.MethodGet, "/v1/activity", nil))
	assert.Contains(t, rec.Body.String(), "optimize arc")
	assert.Contains(t, rec.Body.String(), "review dlm")
}

func TestRouteIsRememberedPerWorkspace(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	arc := t.TempDir()
	b := teamBrain()
	_, fail := b.decideRoute(routeReq{Task: "optimize arc", Forced: "grok", ws: captaincode.Workspace{Dir: arc}})
	require.Nil(t, fail)
	rec := httptest.NewRecorder()
	b.stats(rec, httptest.NewRequest(http.MethodGet, "/v1/stats?cwd="+arc, nil))
	assert.Contains(t, rec.Body.String(), `"task":"optimize arc"`)
	rec = httptest.NewRecorder()
	b.stats(rec, httptest.NewRequest(http.MethodGet, "/v1/stats?cwd="+t.TempDir(), nil))
	assert.Contains(t, rec.Body.String(), `"last":null`)
}

func TestRepeatAndParallelListingsArePerWorkspace(t *testing.T) {
	dlm, arc := t.TempDir(), t.TempDir()
	b := teamBrain()
	b.rmu.Lock()
	b.repeatState()["rp_arc"] = &repeatThread{id: "rp_arc", dir: arc, task: "optimize arc", started: time.Now(), cancel: func() {}}
	b.rmu.Unlock()
	b.pmu.Lock()
	b.parallelState()["pl_arc"] = &parallelRun{id: "pl_arc", dir: arc, task: "review arc", started: time.Now(), cancel: func() {}}
	b.pmu.Unlock()

	// DLM's terminal: not its threads.
	assert.Contains(t, b.repeatStatus(dlm), "no repeat threads running in this folder")
	assert.Contains(t, b.repeatStatus(dlm), "1 thread(s) run in other folders")
	assert.Contains(t, b.parallelStatus(dlm), "no parallel runs in this folder")
	assert.Contains(t, b.repeatStop(dlm, ""), "nothing to stop", "a bare /repeat stop never reaches another folder's loop")
	assert.Contains(t, b.parallelStop(dlm, ""), "nothing to stop")
	assert.Empty(t, b.repeatNotice(dlm))

	// arc's terminal sees and controls them; an explicit id works from anywhere.
	assert.Contains(t, b.repeatStatus(arc), "rp_arc")
	assert.Contains(t, b.parallelStatus(arc), "pl_arc")
	assert.Contains(t, b.parallelStop(dlm, "pl_arc"), "pl_arc", "an id is unambiguous wherever it is typed")
	assert.Contains(t, b.repeatStop(arc, ""), "rp_arc")
}

func TestRepeatStopHTTPScopesToTheCallersFolder(t *testing.T) {
	dlm, arc := t.TempDir(), t.TempDir()
	b := teamBrain()
	b.rmu.Lock()
	b.repeatState()["rp_arc"] = &repeatThread{id: "rp_arc", dir: arc, task: "t", started: time.Now(), cancel: func() {}}
	b.repeatState()["rp_dlm"] = &repeatThread{id: "rp_dlm", dir: dlm, task: "t", started: time.Now(), cancel: func() {}}
	b.rmu.Unlock()

	rec := httptest.NewRecorder()
	b.repeatStopHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/repeat/stop?cwd="+dlm, nil))
	assert.Contains(t, rec.Body.String(), "rp_dlm")
	assert.NotContains(t, rec.Body.String(), "rp_arc", "`captain stop` in DLM leaves arc's loop alone")

	rec = httptest.NewRecorder()
	b.repeatStopHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/repeat/stop", nil))
	assert.Contains(t, rec.Body.String(), "rp_arc", "with no folder named, everything stops")
}

func TestLiveWorkflowStatusIsPerWorkspace(t *testing.T) {
	dlm, arc := t.TempDir(), t.TempDir()
	b := teamBrain()
	tr := newWorkflowTracker("wf_arc", captaincode.Workflow{}, nil)
	b.setLiveWorkflow(&wfLive{dir: arc, id: "wf_arc", key: "grok>codex", tracker: tr, startedAt: time.Now(), stages: 2, runs: 2})

	get := func(q string) string {
		rec := httptest.NewRecorder()
		b.workflowStatus(rec, httptest.NewRequest(http.MethodGet, "/v1/workflow/status"+q, nil))
		return rec.Body.String()
	}
	assert.Contains(t, get("?cwd="+dlm), `"active":false`, "DLM has no workflow of its own")
	assert.Contains(t, get("?cwd="+arc), "wf_arc")
	assert.Contains(t, get(""), "wf_arc", "the dashboard sees the machine's latest")
}

// The Euclid dashboard's Regenerate button probes /api/ping and POSTs
// /api/regenerate?root=<brain>; the brain answers for any brain on the
// machine, only for a directory that is a brain, and only to a page from
// file:// or localhost.
func TestDashboardRegenerateContract(t *testing.T) {
	engine := t.TempDir()
	for _, p := range []string{"engine/build-catalog.py", "dashboard/build-dashboard.py"} {
		require.NoError(t, os.MkdirAll(filepath.Join(engine, filepath.Dir(p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(engine, p), []byte("print('ok')\n"), 0o755))
	}
	t.Setenv("CAPTAIN_EUCLID_ENGINE", engine)
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	b := teamBrain()

	rec := httptest.NewRecorder()
	ping := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	ping.Header.Set("Origin", "null") // a file:// page
	b.euclidPing(rec, ping)
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
	assert.Equal(t, "null", rec.Header().Get("Access-Control-Allow-Origin"))

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/regenerate?root="+filepath.Join(home, ".euclid"), nil)
	req.Header.Set("Origin", "null")
	b.euclidReindex(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var out struct {
		OK    bool `json:"ok"`
		Steps []struct {
			Script string `json:"script"`
			OK     bool   `json:"ok"`
		} `json:"steps"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.True(t, out.OK)
	require.Len(t, out.Steps, 2)
	assert.Equal(t, "build-catalog.py", out.Steps[0].Script, "the dashboard renders the step list")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/regenerate?root="+t.TempDir(), nil)
	b.euclidReindex(rec, req)
	assert.Equal(t, 404, rec.Code, "not a brain: nothing runs")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/regenerate?root="+filepath.Join(home, ".euclid"), nil)
	req.Header.Set("Origin", "https://evil.example")
	b.euclidReindex(rec, req)
	assert.Equal(t, 403, rec.Code, "a web page elsewhere cannot drive the engine")
}

// Once /api/ping answers, the dashboard reads documents through /api/file:
// answering ping without it turned every document click into "read failed."
// (arc, 2026-09-13). The file is served from the named brain's host, never
// from outside it.
func TestDashboardFileContract(t *testing.T) {
	home := euclidTestHome(t)
	repo := filepath.Join(home, "arc")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "docs", "PLAN.md"), []byte("# Plan\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "secret.txt"), []byte("no"), 0o644))
	_, err := captaincode.Scaffold(filepath.Join(repo, ".euclid"), "repo")
	require.NoError(t, err)
	b := teamBrain()
	get := func(q string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/file?"+q, nil)
		req.Header.Set("Origin", "null")
		b.euclidFile(rec, req)
		return rec.Code, rec.Body.String()
	}
	root := filepath.Join(repo, ".euclid")
	code, body := get("path=docs/PLAN.md&root=" + root)
	assert.Equal(t, 200, code)
	assert.Contains(t, body, `"content":"# Plan\n"`)
	code, _ = get("path=../secret.txt&root=" + root)
	assert.Equal(t, 403, code, "no path escape")
	code, _ = get("path=/etc/passwd&root=" + root)
	assert.Equal(t, 403, code)
	code, _ = get("path=docs/PLAN.md&root=" + t.TempDir())
	assert.Equal(t, 404, code, "not a brain")
}

// A prompt typed in DLM about captaincode runs in captaincode - and its
// brain, not DLM's, is the one read and written (2026-09-13). Two repos
// named: the worker stays, their brains are read alongside.
func TestFollowTaskMovesTheWorkspaceToTheRepoTheTaskNames(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"DLM", "captaincode", "lemma"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, r, ".git"), 0o755))
	}
	t.Setenv("CAPTAIN_WORKSPACE_ROOT", root)
	t.Setenv("EUCLID_HOME", filepath.Join(root, ".euclid-main"))
	captaincode.ResetKnownReposForTest()
	b := &brain{}
	dlm := captaincode.Workspace{Dir: filepath.Join(root, "DLM"), Effort: captaincode.EffortHigh}

	ws := b.followTask(dlm, "make /repeat finish gracefully in captaincode")
	assert.Equal(t, filepath.Join(root, "captaincode"), ws.Dir)
	assert.Equal(t, captaincode.EffortHigh, ws.Effort, "the rest of the request context rides along")
	assert.Empty(t, ws.Brains)
	require.Len(t, b.acts, 1, "announced in the origin folder's feed")
	assert.Equal(t, dlm.Dir, b.acts[0].Dir)
	assert.Contains(t, b.acts[0].Text, "captaincode")

	ws = b.followTask(dlm, "compare the CI in captaincode and the lemma repo")
	assert.Equal(t, dlm.Dir, ws.Dir, "several named: the worker stays put")
	assert.ElementsMatch(t, []string{filepath.Join(root, "captaincode"), filepath.Join(root, "lemma")}, ws.Brains)

	ws = b.followTask(dlm, "fix the DLM parser")
	assert.Equal(t, dlm, ws, "its own repo is the default")
}
