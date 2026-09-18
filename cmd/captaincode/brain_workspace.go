package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// One brain serves every captain-code TUI on the machine, each opened in its
// own folder. The folder is the WORKSPACE: where workers run, which local
// Euclid brain is read and journaled, which project's shared context and
// worker list a request is about. It used to be the brain's CAPTAIN_CWD - one
// value per process - so with two TUIs open, the second launcher restarted the
// brain (killing the first one's workers) and every worker of the first TUI
// then ran in the second one's repo (live 2026-07-19, 2026-09-12). Now the
// TUI says which folder it is in on every request and the brain never moves.

// workspaceHeader is set by the TUI's captain provider on every chat call
// (`captain init` writes it into opencode.jsonc, from the launcher's
// CAPTAIN_CWD); the sidebar plugin passes the same folder as ?cwd=.
const workspaceHeader = "X-Captain-Cwd"

// workspaceOf resolves the workspace a request is for: the header, else the
// query, else the brain's own default. Only an existing absolute directory
// is accepted - the brain listens on localhost, but a bad path must not make
// a worker run somewhere surprising.
func workspaceOf(r *http.Request) captaincode.Workspace {
	dir := strings.TrimSpace(r.Header.Get(workspaceHeader))
	if dir == "" {
		dir = strings.TrimSpace(r.URL.Query().Get("cwd"))
	}
	if dir != "" && filepath.IsAbs(dir) {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return captaincode.Workspace{Dir: filepath.Clean(dir)}
		}
	}
	return defaultWorkspace()
}

// workspaceFilter is the read side: a sidebar asking for ITS project's
// workers, activity and last route passes ?cwd=; with no folder named (the
// dashboard aggregator, curl) the answer is machine-wide.
func workspaceFilter(r *http.Request) (string, bool) {
	dir := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if dir == "" {
		dir = strings.TrimSpace(r.Header.Get(workspaceHeader))
	}
	if dir == "" {
		return "", false
	}
	return filepath.Clean(dir), true
}

// defaultWorkspace is the brain's own folder (CAPTAIN_CWD, else its cwd): the
// CLI commands, and any caller that sent no workspace.
func defaultWorkspace() captaincode.Workspace {
	ws := captaincode.DefaultWorkspace()
	if ws.Dir == "" {
		ws.Dir, _ = os.Getwd()
	}
	return ws
}

// followTask moves the workspace to the repository the task names, when it
// names exactly one other than the TUI's own: the worker runs there and
// that repository's brain is the one read and written. Several named: the
// workspace stays and their brains are read alongside (reporefs.go). The
// move is announced in the origin folder's feed - a worker running
// elsewhere must never be a surprise.
func (b *brain) followTask(ws captaincode.Workspace, task string) captaincode.Workspace {
	refs := captaincode.RepoRefs(task, ws.Dir)
	switch len(refs) {
	case 0:
		return ws
	case 1:
		fmt.Printf("captain brain: task names %s - the worker runs there, its brain reads and writes\n", refs[0])
		b.pushActivity(activity{Dir: ws.Dir, Kind: "route", Leg: "workspace", Model: filepath.Base(refs[0]),
			Text: "task names " + filepath.Base(refs[0]) + " - worker and memory move there"})
		ws.Dir = refs[0]
		return ws
	}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, filepath.Base(r))
	}
	fmt.Printf("captain brain: task names %s - their brains are read alongside %s\n", strings.Join(names, ", "), ws.Dir)
	b.pushActivity(activity{Dir: ws.Dir, Kind: "route", Leg: "workspace", Model: "brains",
		Text: "task names " + strings.Join(names, ", ") + " - their memory is read here"})
	ws.Brains = refs
	return ws
}
