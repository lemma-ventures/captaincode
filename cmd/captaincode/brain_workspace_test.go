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
// used to follow one CAPTAIN_CWD: with QMX and ash open, the QMX sidebar
// showed ash's memory link and, worse, QMX's workers ran in ash (2026-09-12).
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
	ash := t.TempDir()

	b := teamBrain()
	var seen string
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		seen = brief
		return leg, captaincode.Result{Text: "done"}, nil
	}
	body, _ := json.Marshal(oaiChatReq{Model: "free", Messages: []oaiMessage{{Role: "user", Content: json.RawMessage(`"list the files"`)}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("X-Captain-Cwd", ash)
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, seen, "repository at "+ash, "the worker is told it works in the TUI's folder")
	assert.NotContains(t, seen, "repository at "+brainHome)
}

func TestEuclidStatusIsPerWorkspace(t *testing.T) {
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	// A second project with its own repo brain.
	repo := filepath.Join(home, "ash")
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
	ash := get(repo)
	assert.Equal(t, repo, ash.Cwd)
	assert.True(t, hasBrainKind(ash.Brains, "repo"), "the ash TUI sees ash's own brain")
	plain := get(home)
	assert.Equal(t, home, plain.Cwd)
	assert.False(t, hasBrainKind(plain.Brains, "repo"), "the other TUI does not see ash's brain")
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
	qmx := t.TempDir()
	ash := t.TempDir()
	b := teamBrain()
	b.active.begin(captaincode.Workspace{Dir: ash}, captaincode.LegGrok, "[user]\noptimize ash")
	b.pushActivity(activity{Dir: ash, Kind: "run", Leg: "grok", Text: "optimize ash"})
	b.pushActivity(activity{Dir: qmx, Kind: "run", Leg: "free", Text: "review qmx"})
	b.pushActivity(activity{Kind: "route", Leg: "director", Text: "director recovered"}) // machine-wide
	b.mu.Lock()
	b.last = &lastRoute{Task: "optimize ash", Leg: "grok", Dir: ash}
	b.lastBy = map[string]*lastRoute{ash: b.last}
	b.mu.Unlock()

	// QMX's sidebar: ash's busy grok is not its worker, ash's run is not its activity.
	rec := httptest.NewRecorder()
	b.workers(rec, httptest.NewRequest(http.MethodGet, "/v1/workers?cwd="+qmx, nil))
	var wr workersResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &wr))
	for _, row := range wr.Workers {
		assert.NotEqual(t, "busy", row.Status, "QMX must not show ash's %s as busy", row.Leg)
	}
	rec = httptest.NewRecorder()
	b.activityFeed(rec, httptest.NewRequest(http.MethodGet, "/v1/activity?cwd="+qmx, nil))
	assert.Contains(t, rec.Body.String(), "review qmx")
	assert.Contains(t, rec.Body.String(), "director recovered", "machine-wide entries reach every TUI")
	assert.NotContains(t, rec.Body.String(), "optimize ash")
	rec = httptest.NewRecorder()
	b.stats(rec, httptest.NewRequest(http.MethodGet, "/v1/stats?cwd="+qmx, nil))
	assert.Contains(t, rec.Body.String(), `"last":null`, "no route yet for QMX - not ash's")

	// ash's sidebar sees its run; the dashboard (no folder) sees everything.
	rec = httptest.NewRecorder()
	b.workers(rec, httptest.NewRequest(http.MethodGet, "/v1/workers?cwd="+ash, nil))
	assert.Contains(t, rec.Body.String(), `"busy":1`)
	rec = httptest.NewRecorder()
	b.activityFeed(rec, httptest.NewRequest(http.MethodGet, "/v1/activity", nil))
	assert.Contains(t, rec.Body.String(), "optimize ash")
	assert.Contains(t, rec.Body.String(), "review qmx")
}

func TestRouteIsRememberedPerWorkspace(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	ash := t.TempDir()
	b := teamBrain()
	_, fail := b.decideRoute(routeReq{Task: "optimize ash", Forced: "grok", ws: captaincode.Workspace{Dir: ash}})
	require.Nil(t, fail)
	rec := httptest.NewRecorder()
	b.stats(rec, httptest.NewRequest(http.MethodGet, "/v1/stats?cwd="+ash, nil))
	assert.Contains(t, rec.Body.String(), `"task":"optimize ash"`)
	rec = httptest.NewRecorder()
	b.stats(rec, httptest.NewRequest(http.MethodGet, "/v1/stats?cwd="+t.TempDir(), nil))
	assert.Contains(t, rec.Body.String(), `"last":null`)
}

func TestRepeatAndParallelListingsArePerWorkspace(t *testing.T) {
	qmx, ash := t.TempDir(), t.TempDir()
	b := teamBrain()
	b.rmu.Lock()
	b.repeatState()["rp_ash"] = &repeatThread{id: "rp_ash", dir: ash, task: "optimize ash", started: time.Now(), cancel: func() {}}
	b.rmu.Unlock()
	b.pmu.Lock()
	b.parallelState()["pl_ash"] = &parallelRun{id: "pl_ash", dir: ash, task: "review ash", started: time.Now(), cancel: func() {}}
	b.pmu.Unlock()

	// QMX's terminal: not its threads.
	assert.Contains(t, b.repeatStatus(qmx), "no repeat threads running in this folder")
	assert.Contains(t, b.repeatStatus(qmx), "1 thread(s) run in other folders")
	assert.Contains(t, b.parallelStatus(qmx), "no parallel runs in this folder")
	assert.Contains(t, b.repeatStop(qmx, ""), "nothing to stop", "a bare /repeat stop never reaches another folder's loop")
	assert.Contains(t, b.parallelStop(qmx, ""), "nothing to stop")
	assert.Empty(t, b.repeatNotice(qmx))

	// ash's terminal sees and controls them; an explicit id works from anywhere.
	assert.Contains(t, b.repeatStatus(ash), "rp_ash")
	assert.Contains(t, b.parallelStatus(ash), "pl_ash")
	assert.Contains(t, b.parallelStop(qmx, "pl_ash"), "pl_ash", "an id is unambiguous wherever it is typed")
	assert.Contains(t, b.repeatStop(ash, ""), "rp_ash")
}

func TestRepeatStopHTTPScopesToTheCallersFolder(t *testing.T) {
	qmx, ash := t.TempDir(), t.TempDir()
	b := teamBrain()
	b.rmu.Lock()
	b.repeatState()["rp_ash"] = &repeatThread{id: "rp_ash", dir: ash, task: "t", started: time.Now(), cancel: func() {}}
	b.repeatState()["rp_qmx"] = &repeatThread{id: "rp_qmx", dir: qmx, task: "t", started: time.Now(), cancel: func() {}}
	b.rmu.Unlock()

	rec := httptest.NewRecorder()
	b.repeatStopHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/repeat/stop?cwd="+qmx, nil))
	assert.Contains(t, rec.Body.String(), "rp_qmx")
	assert.NotContains(t, rec.Body.String(), "rp_ash", "`captain stop` in QMX leaves ash's loop alone")

	rec = httptest.NewRecorder()
	b.repeatStopHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/repeat/stop", nil))
	assert.Contains(t, rec.Body.String(), "rp_ash", "with no folder named, everything stops")
}

func TestLiveWorkflowStatusIsPerWorkspace(t *testing.T) {
	qmx, ash := t.TempDir(), t.TempDir()
	b := teamBrain()
	tr := newWorkflowTracker("wf_ash", captaincode.Workflow{}, nil)
	b.setLiveWorkflow(&wfLive{dir: ash, id: "wf_ash", key: "grok>codex", tracker: tr, startedAt: time.Now(), stages: 2, runs: 2})

	get := func(q string) string {
		rec := httptest.NewRecorder()
		b.workflowStatus(rec, httptest.NewRequest(http.MethodGet, "/v1/workflow/status"+q, nil))
		return rec.Body.String()
	}
	assert.Contains(t, get("?cwd="+qmx), `"active":false`, "QMX has no workflow of its own")
	assert.Contains(t, get("?cwd="+ash), "wf_ash")
	assert.Contains(t, get(""), "wf_ash", "the dashboard sees the machine's latest")
}

// The Euclid dashboard's Regenerate button probes /api/ping and POSTs
// /api/regenerate?root=<brain>; the brain answers for any brain on the
// machine, only for a directory that is a brain, and only to a page from
// file:// or localhost.
func TestDashboardRegenerateContract(t *testing.T) {
	engine := t.TempDir()
	for _, p := range []string{"engine/build-catalog.py", "dashboard/build-dashboard.py", "dashboard/index.html"} {
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
	assert.Equal(t, ".euclid/bin/build-catalog.py", out.Steps[0].Script, "the panel shows the command a human can run from the host root")

	// Pinging after reindex returns built_at
	rec = httptest.NewRecorder()
	ping = httptest.NewRequest(http.MethodGet, "/api/ping?root="+filepath.Join(home, ".euclid"), nil)
	ping.Header.Set("Origin", "null")
	b.euclidPing(rec, ping)
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"built_at":`)

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

func TestTUIConnectionTrackingAndBrainReconciler(t *testing.T) {
	home := euclidTestHome(t)
	repo := filepath.Join(home, "ash")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	shared := filepath.Join(repo, ".euclid")
	_, err := captaincode.Scaffold(shared, "repo")
	require.NoError(t, err)

	// Note activity for this repo
	noteTUIActivity(repo)
	assert.True(t, anyTUIConnected())
	roots := activeBrainRoots()
	assert.Contains(t, roots, shared)
}

// Once /api/ping answers, the dashboard reads documents through /api/file:
// answering ping without it turned every document click into "read failed."
// (ash, 2026-09-13). The file is served from the named brain's host, never
// from outside it.
func TestDashboardFileContract(t *testing.T) {
	home := euclidTestHome(t)
	repo := filepath.Join(home, "ash")
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

// A prompt typed in QMX about captaincode runs in captaincode - and its
// brain, not QMX's, is the one read and written (2026-09-13). Two repos
// named: the worker stays, their brains are read alongside.
func TestFollowTaskMovesTheWorkspaceToTheRepoTheTaskNames(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"QMX", "captaincode", "axiom"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, r, ".git"), 0o755))
	}
	t.Setenv("CAPTAIN_WORKSPACE_ROOT", root)
	t.Setenv("EUCLID_HOME", filepath.Join(root, ".euclid-main"))
	captaincode.ResetKnownReposForTest()
	b := &brain{}
	qmx := captaincode.Workspace{Dir: filepath.Join(root, "QMX"), Effort: captaincode.EffortHigh}

	ws := b.followTask(qmx, "make /repeat finish gracefully in captaincode")
	assert.Equal(t, filepath.Join(root, "captaincode"), ws.Dir)
	assert.Equal(t, captaincode.EffortHigh, ws.Effort, "the rest of the request context rides along")
	assert.Empty(t, ws.Brains)
	require.Len(t, b.acts, 1, "announced in the origin folder's feed")
	assert.Equal(t, qmx.Dir, b.acts[0].Dir)
	assert.Contains(t, b.acts[0].Text, "captaincode")

	ws = b.followTask(qmx, "compare the CI in captaincode and the axiom repo")
	assert.Equal(t, qmx.Dir, ws.Dir, "several named: the worker stays put")
	assert.ElementsMatch(t, []string{filepath.Join(root, "captaincode"), filepath.Join(root, "axiom")}, ws.Brains)

	ws = b.followTask(qmx, "fix the QMX parser")
	assert.Equal(t, qmx, ws, "its own repo is the default")
}
