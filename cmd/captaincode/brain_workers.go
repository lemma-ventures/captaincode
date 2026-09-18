package main

// GET /v1/workers - which legs are working, idle or cooling, and on what.
//
// The sidebar polls this every second. It matters more on stock opencode than
// it did on the fork: the brain narrates a run on the reasoning channel, and
// the ai-sdk openai-compatible path drops that, so this panel is the only live
// evidence that a long run is alive rather than wedged.

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// activeRun is a worker currently executing.
type activeRun struct {
	started time.Time
	task    string
	dir     string // the workspace the run is for
}

// activeRuns is the live per-leg view. Keyed by leg because that is what the
// panel shows; a leg running twice at once keeps the first start time, which
// is the one worth displaying ("this leg has been busy for 4m").
type activeRuns struct {
	mu   sync.Mutex
	runs map[captaincode.Leg]activeRun
}

func (a *activeRuns) begin(ws captaincode.Workspace, l captaincode.Leg, task string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runs == nil {
		a.runs = map[captaincode.Leg]activeRun{}
	}
	if _, busy := a.runs[l]; !busy {
		a.runs[l] = activeRun{started: time.Now(), task: promptPeek(lastUserTurn(task)), dir: ws.Dir}
	}
}

func (a *activeRuns) end(l captaincode.Leg) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.runs, l)
}

func (a *activeRuns) snapshot() map[captaincode.Leg]activeRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[captaincode.Leg]activeRun, len(a.runs))
	for k, v := range a.runs {
		out[k] = v
	}
	return out
}

type workerRow struct {
	Leg          string `json:"leg"`
	Status       string `json:"status"` // busy | cooling | idle
	Title        string `json:"title"`
	Task         string `json:"task,omitempty"`
	Runs         int    `json:"runs"`
	ElapsedMs    int64  `json:"elapsed_ms,omitempty"`
	CoolingUntil int64  `json:"coolingUntil,omitempty"`
}

func (b *brain) workers(w http.ResponseWriter, r *http.Request) {
	live := b.active.snapshot()
	now := time.Now()
	// A sidebar sees its own project's runs. Cooldowns and run counts stay
	// machine-wide: a rate limit is per account, whichever folder hit it.
	if dir, mine := workspaceFilter(r); mine {
		for l, run := range live {
			if run.dir != dir {
				delete(live, l)
			}
		}
	}

	b.mu.Lock()
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
			row.ElapsedMs = now.Sub(run.started).Milliseconds()
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
	writeJSON(w, 200, map[string]any{"workers": rows, "busy": len(live)})
}
