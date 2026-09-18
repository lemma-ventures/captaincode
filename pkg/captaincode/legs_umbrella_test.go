package captaincode

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every opencode instance on the machine shares one session database, and the
// TUI's `-c` opens the most recently updated TOP-LEVEL session in the project
// directory. Captain's workers are sessions in that same directory, so after
// any worker ran, `-c` resumed the WORKER instead of the user's own session -
// live 2026-09-11, the user opened a captain-free session whose transcript was
// a title probe and asked why the TUI showed a title-generator prompt.
//
// The TUI's filter is `parentID === undefined`. So workers are created as
// CHILDREN of one umbrella session per project: invisible to -c and to the
// top-level session list, exactly like opencode's own subagent sessions.
//
// The umbrella must NOT live in the project either: it is a top-level session,
// so `-c` resumed it - empty - the moment it was newer than the user's own
// session (every brain start made a fresh one; live 2026-09-11, second round).
// It is pinned to captain's own dir and found again after a restart.

type createdSession struct {
	title    string
	parentID string
	dir      string
}

func recordingServe(t *testing.T) (*httptest.Server, *[]createdSession, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	created := []createdSession{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/session" {
			// The list a restarted brain reads to find its old umbrella.
			mu.Lock()
			defer mu.Unlock()
			out := []map[string]string{}
			for i, c := range created {
				if c.parentID == "" && c.dir == r.URL.Query().Get("directory") {
					out = append(out, map[string]string{"id": "ses_" + string(rune('a'+i)), "title": c.title})
				}
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/session" {
			body, _ := io.ReadAll(r.Body)
			var in struct {
				Title    string `json:"title"`
				ParentID string `json:"parentID"`
			}
			_ = json.Unmarshal(body, &in)
			mu.Lock()
			created = append(created, createdSession{title: in.Title, parentID: in.ParentID, dir: r.URL.Query().Get("directory")})
			n := len(created)
			mu.Unlock()
			w.Write([]byte(`{"id":"ses_` + string(rune('a'+n-1)) + `"}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &created, &mu
}

func TestWorkerSessionsAreChildrenOfAnUmbrellaSession(t *testing.T) {
	t.Setenv("CAPTAIN_CWD", "/src/project")
	t.Setenv("HOME", t.TempDir())
	srv, created, mu := recordingServe(t)
	resetUmbrellas()

	d := &OpencodeDispatcher{BaseURL: srv.URL, Client: srv.Client(), Spawn: false, Title: "captain-grok"}
	require.NoError(t, d.ensureSession())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *created, 2, "one umbrella, then the worker under it")
	umbrella, worker := (*created)[0], (*created)[1]
	assert.Empty(t, umbrella.parentID, "the umbrella is the one top-level session captain owns")
	assert.Contains(t, umbrella.title, "captain", "…and says so in its title")
	assert.Equal(t, umbrellaDir(), umbrella.dir, "pinned to captain's dir: a top-level session in the project is what `-c` resumes")
	assert.NotEqual(t, "/src/project", umbrella.dir)
	assert.Equal(t, "ses_a", worker.parentID, "the worker is a child, so `-c` and the session list skip it")
	assert.Equal(t, "captain-grok", worker.title)
	assert.Equal(t, "/src/project", worker.dir, "still pinned to the caller's project")
}

func TestUmbrellaIsSharedAcrossWorkersOfOneProject(t *testing.T) {
	t.Setenv("CAPTAIN_CWD", "/src/project")
	t.Setenv("HOME", t.TempDir())
	srv, created, mu := recordingServe(t)
	resetUmbrellas()

	a := &OpencodeDispatcher{BaseURL: srv.URL, Client: srv.Client(), Spawn: false, Title: "captain-grok"}
	b := &OpencodeDispatcher{BaseURL: srv.URL, Client: srv.Client(), Spawn: false, Title: "captain-glm"}
	require.NoError(t, a.ensureSession())
	require.NoError(t, b.ensureSession())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *created, 3, "one umbrella for the project, two workers - not an umbrella per worker")
	assert.Equal(t, (*created)[1].parentID, (*created)[2].parentID)
}

func TestUmbrellaSurvivesABrainRestart(t *testing.T) {
	t.Setenv("CAPTAIN_CWD", "/src/project")
	t.Setenv("HOME", t.TempDir())
	srv, created, mu := recordingServe(t)
	resetUmbrellas()

	a := &OpencodeDispatcher{BaseURL: srv.URL, Client: srv.Client(), Spawn: false, Title: "captain-grok"}
	require.NoError(t, a.ensureSession())
	resetUmbrellas() // the brain restarted: its in-memory map is gone, the serve's sessions are not
	b := &OpencodeDispatcher{BaseURL: srv.URL, Client: srv.Client(), Spawn: false, Title: "captain-glm"}
	require.NoError(t, b.ensureSession())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *created, 3, "the restarted brain found its umbrella instead of leaving another empty one")
	assert.Equal(t, "ses_a", (*created)[2].parentID)
}
