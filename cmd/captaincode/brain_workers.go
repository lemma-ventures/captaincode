package main

// GET /v1/workers - which legs are working, idle or cooling, and on what.
//
// The sidebar polls this every second. It matters more on stock opencode than
// it did on the fork: the brain narrates a run on the reasoning channel, and
// the ai-sdk openai-compatible path drops that, so this panel is the only live
// evidence that a long run is alive rather than wedged.

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// activeRun is a worker currently executing.
type activeRun struct {
	leg     captaincode.Leg
	started time.Time
	task    string
	dir     string             // the workspace the run is for
	origin  string             // the TUI folder the request came from
	effort  captaincode.Effort // how hard it was asked to think ("" = the transport's default)
}

// activeRuns holds every run in progress, one entry per run. The panel shows
// one row per leg, but two TUIs can run the same leg at once: keyed by leg,
// the second run was invisible, and the first run to end removed the other
// one too (a claude run in one folder vanished when a claude run elsewhere
// finished, and repoBusy then saw an idle checkout that was not).
type activeRuns struct {
	mu   sync.Mutex
	next uint64
	runs map[uint64]activeRun
}

// begin records a run and returns the function that ends exactly that run.
func (a *activeRuns) begin(ws captaincode.Workspace, l captaincode.Leg, task string) (end func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runs == nil {
		a.runs = map[uint64]activeRun{}
	}
	a.next++
	id := a.next
	a.runs[id] = activeRun{leg: l, started: time.Now(), task: promptPeek(lastUserTurn(task)), dir: ws.Dir, origin: ws.Origin, effort: ws.Effort}
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			delete(a.runs, id)
		})
	}
}

// list is every run in progress, oldest first.
func (a *activeRuns) list() []activeRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]activeRun, 0, len(a.runs))
	for _, run := range a.runs {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].started.Before(out[j].started) })
	return out
}

// snapshot is the oldest run of each busy leg: the one worth displaying
// ("this leg has been busy for 4m").
func (a *activeRuns) snapshot() map[captaincode.Leg]activeRun {
	out := map[captaincode.Leg]activeRun{}
	for _, run := range a.list() {
		if _, seen := out[run.leg]; !seen {
			out[run.leg] = run
		}
	}
	return out
}

// runRow is one run in progress, for a view that shows every worker on the
// machine (several TUIs, several folders) rather than one row per leg.
type runRow struct {
	Leg       string `json:"leg"`
	Task      string `json:"task,omitempty"`
	Effort    string `json:"effort,omitempty"`
	ElapsedMs int64  `json:"elapsed_ms"`
	Started   int64  `json:"started"` // Unix milliseconds
	Dir       string `json:"dir,omitempty"`
	Origin    string `json:"origin,omitempty"`
}

type workerRow struct {
	Leg          string `json:"leg"`
	Status       string `json:"status"` // busy | cooling | idle
	Title        string `json:"title"`
	Task         string `json:"task,omitempty"`
	Effort       string `json:"effort,omitempty"` // the running request's effort (max = the ceiling)
	Runs         int    `json:"runs"`
	ElapsedMs    int64  `json:"elapsed_ms,omitempty"`
	CoolingUntil int64  `json:"coolingUntil,omitempty"`
	// From names the TUI folder a run came from when it is not this one: a
	// task that named this repository was typed elsewhere.
	From string `json:"from,omitempty"`
}

func (b *brain) workers(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	// A sidebar sees its own project's runs. Cooldowns and run counts stay
	// machine-wide: a rate limit is per account, whichever folder hit it.
	dir, mine := workspaceFilter(r)
	all := b.active.list()
	runs := make([]runRow, 0, len(all))
	live := map[captaincode.Leg]activeRun{}
	for _, run := range all {
		if mine && run.dir != dir && !insideRepo(run.dir, dir) {
			continue
		}
		if _, seen := live[run.leg]; !seen {
			live[run.leg] = run
		}
		runs = append(runs, runRow{
			Leg: string(run.leg), Task: run.task, Effort: string(run.effort),
			ElapsedMs: now.Sub(run.started).Milliseconds(), Started: run.started.UnixMilli(),
			Dir: run.dir, Origin: run.origin,
		})
	}

	if !b.lockForPoll(w, r) {
		return
	}
	stats := b.ledger.Stats()
	cool := make(map[captaincode.Leg]time.Time, len(b.ledger.Cooldowns))
	for l, t := range b.ledger.Cooldowns {
		cool[l] = t
	}
	b.mu.Unlock()

	rows := make([]workerRow, 0, len(captaincode.AllLegs))
	for _, l := range captaincode.AllLegs {
		if !captaincode.ServesTasks(l) {
			continue // never runs a task, so never a worker row
		}
		spec, _ := captaincode.Spec(l)
		row := workerRow{Leg: string(l), Status: "idle", Title: spec.Display, Runs: stats[l].N}
		if until, ok := cool[l]; ok && until.After(now) {
			row.Status, row.CoolingUntil = "cooling", until.UnixMilli()
		}
		if run, ok := live[l]; ok {
			row.Status = "busy"
			row.Task = run.task
			row.Effort = string(run.effort)
			row.ElapsedMs = now.Sub(run.started).Milliseconds()
			if mine && run.origin != "" && filepath.Clean(run.origin) != dir {
				row.From = filepath.Base(run.origin)
			}
			row.CoolingUntil = 0
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		rank := func(s string) int {
			switch s {
			case "busy":
				return 0
			case "cooling":
				return 1
			}
			return 2
		}
		if rank(rows[i].Status) != rank(rows[j].Status) {
			return rank(rows[i].Status) < rank(rows[j].Status)
		}
		return rows[i].Leg < rows[j].Leg
	})
	writeJSON(w, 200, map[string]any{"workers": rows, "busy": len(runs), "runs": runs})
}

// insideRepo: run dir is a worktree of the repository at dir. An isolated
// worker runs in a temporary worktree; the panel of the repository it works
// on still lists it.
func insideRepo(runDir, dir string) bool {
	return strings.Contains(filepath.Base(runDir), "captain-wt-") && captaincode.GitRoot(dir) != "" &&
		gitCommonDir(runDir) == gitCommonDir(dir)
}
