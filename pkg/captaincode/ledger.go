package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Ledger is captain's local state: per-directory opencode sessions,
// per-(leg,cwd) conversation threads, per-leg cooldowns, and a capped
// decision/outcome log powering `captain why` / `captain quota` / `captain
// stats`. JSON on disk; upgrade to SQLite when history outgrows it
// (CAPTAIN_CODE_SPEC.md §6).
type Ledger struct {
	Sessions      map[string]string    `json:"sessions"`   // logical name (e.g. "__captain__") -> opencode session id
	Threads       map[string]ThreadRef `json:"threads"`    // "<leg>@<cwd>" -> the leg's persistent opencode session for that project - real memory/thread reuse across captain invocations, not a fresh session every dispatch
	Cooldowns     map[Leg]time.Time    `json:"cooldowns"`  // leg -> open-again time
	WorkerSeq     int                  `json:"worker_seq"` // monotonic worker tab number
	Events        []Event              `json:"events"`
	Charges       []Charge             `json:"charges,omitempty"`        // versioned task/stage/attempt/call accounting (accounting.go)
	Decisions     []Decision           `json:"decisions,omitempty"`      // why each task went where it went (decision.go)
	Shadows       []ShadowRecord       `json:"shadows,omitempty"`        // decision-leg answers beside choices no decision record covers (shadow.go)
	Quotas        map[Leg]Quota        `json:"quotas,omitempty"`         // last observed quota state per leg (quota.go, M2.3)
	Budgets       []Budget             `json:"budgets,omitempty"`        // shared resource envelopes per root task (budget.go, M2.4)
	TaskStates    []TaskState          `json:"task_states,omitempty"`    // durable lifecycle per task (lifecycle.go, M3.3)
	AttemptStates []AttemptState       `json:"attempt_states,omitempty"` // durable lifecycle per attempt (lifecycle.go, M3.3)
	Handoffs      []HandoffBrief       `json:"handoffs,omitempty"`       // structured handoff briefs per task (handoff.go, M3.5)
	Outcomes      []OutcomeEvidence    `json:"outcomes,omitempty"`       // task-wide acceptance evidence (outcome.go, M5.1)
	Snapshots     []PolicySnapshot     `json:"snapshots,omitempty"`      // versioned policy snapshots (policy.go, M5.4)
	Canaries      []Canary             `json:"canaries,omitempty"`       // bounded opt-in canaries (policy.go, M5.4)
	// DirectorMode is the standing director policy (director_pick.go):
	// "frontier" | "quality" | "auto", or "fixed:<leg>" for a pinned leg;
	// "" means the env/default director. Set by /captain <word>, kept across
	// brain restarts.
	DirectorMode string `json:"director_mode,omitempty"`
	path         string
}

// ThreadRef is a persisted opencode session identity for one leg in one
// project directory: reusing it across turns is what gives that leg real
// conversational memory (the worker recalls earlier turns), same as an
// OpenCode/Claude Code/Codex chat session naturally would.
type ThreadRef struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
}

// ThreadKey identifies a leg's persistent thread for the given directory.
func ThreadKey(leg Leg, cwd string) string {
	return string(leg) + "@" + cwd
}

// ResetThreads drops all persisted threads for cwd, so the next dispatch to
// any leg starts a brand-new, memory-free session (e.g. `/new` in the REPL).
func (l *Ledger) ResetThreads(cwd string) {
	suffix := "@" + cwd
	for k := range l.Threads {
		if len(k) > len(suffix) && k[len(k)-len(suffix):] == suffix {
			delete(l.Threads, k)
		}
	}
}

// NextWorkerSeq numbers worker sessions (the <#> in w-<model>-<#> tabs).
func (l *Ledger) NextWorkerSeq() int {
	l.WorkerSeq++
	return l.WorkerSeq
}

type Event struct {
	At       time.Time `json:"at"`
	Task     string    `json:"task"`
	Class    Class     `json:"class"`
	Leg      Leg       `json:"leg"`
	Reason   string    `json:"reason"`
	Outcome  string    `json:"outcome"` // ok | fail | rate_limited
	Tokens   int       `json:"tokens"`
	CostUSD  float64   `json:"cost_usd,omitempty"`
	Duration int64     `json:"duration_ms"`
	Iter     int       `json:"iter,omitempty"`
	Quality  float64   `json:"quality,omitempty"` // manager assessment 0-10; 0 = unscored
	Verdict  string    `json:"verdict,omitempty"`
	Team     string    `json:"team,omitempty"` // canonical ensemble key (TeamKey) on fan-out runs; an event with Team set and NO Leg is the ensemble-level aggregate
	Workflow string    `json:"workflow,omitempty"`
	Domain   string    `json:"domain,omitempty"` // triaged work domain (code|editorial|research|general), for per-domain priors // canonical CWL key (Workflow.Key) on user-designed workflow runs; an event with Workflow set and NO Leg is the workflow-level aggregate
	Error    string    `json:"error,omitempty"`  // captured on fail/rate_limited, for `captain why` diagnosis
	// Accounting identity (M1.2): which task this run belongs to and which
	// attempt of it this was, so a decision row can be joined to the charges
	// it caused. CostStatus says whether CostUSD was billed, priced by us, or
	// simply not reported - a $0 with no status is the bug this replaces.
	TaskID     string      `json:"task_id,omitempty"`
	AttemptID  string      `json:"attempt_id,omitempty"`
	CostStatus UsageStatus `json:"cost_status,omitempty"`
}

// LegStats is the 3-axis scorecard (performance, quality, cost) per leg,
// aggregated from recorded events. Fed back into the manager's Plan prompt
// so routing improves with evidence - the local seed of PERFORMANCE_TRACKING.md.
type LegStats struct {
	N             int
	Scored        int     // how many of the N runs have a manager quality score
	AvgQuality    float64 // over scored ok-events only; meaningless when Scored==0
	AvgDurationMs int64
	AvgTokens     int
	TotalCostUSD  float64              // sum of real $ cost where the leg reports it (0 for subscription legs)
	Fails         int                  // PROVIDER-fault failures (stalls, outages, empty answers) - reliability is a routing signal
	HarnessFails  int                  // failures that were OUR bugs (auth, keychain, crash residue) - never held against the model
	ByClass       map[Class]ClassStat  // quality broken down per task class - a leg strong on medium can be weak on high
	ByDomain      map[Domain]ClassStat // quality per work domain (code/editorial/research/general)
	LastAt        time.Time            // most recent run of any outcome; the freshness of everything above (M2.1)
}

// ClassStat is one (leg-or-team, class) quality bucket.
type ClassStat struct {
	N          int
	Scored     int
	AvgQuality float64
}

// classAgg accumulates ClassStat buckets.
type classAgg map[Class]*struct {
	n, scored int
	q         float64
}

func (c classAgg) add(e Event) {
	b, ok := c[e.Class]
	if !ok {
		b = &struct {
			n, scored int
			q         float64
		}{}
		c[e.Class] = b
	}
	b.n++
	if e.Quality > 0 {
		b.scored++
		b.q += e.Quality
	}
}

func (c classAgg) finish() map[Class]ClassStat {
	if len(c) == 0 {
		return nil
	}
	out := map[Class]ClassStat{}
	for cl, b := range c {
		st := ClassStat{N: b.n, Scored: b.scored}
		if b.scored > 0 {
			st.AvgQuality = b.q / float64(b.scored)
		}
		out[cl] = st
	}
	return out
}

// TeamKey canonicalizes a fan-out worker set into an ensemble identity:
// sorted, deduped, "+"-joined. One leg is not a team (empty key) - solo runs
// stay on the per-leg scorecards.
func TeamKey(legs []Leg) string {
	seen := map[Leg]bool{}
	names := []string{}
	for _, l := range legs {
		if l != "" && !seen[l] {
			seen[l] = true
			names = append(names, string(l))
		}
	}
	if len(names) < 2 {
		return ""
	}
	sort.Strings(names)
	return strings.Join(names, "+")
}

// TeamStat is an ensemble's scorecard, built from aggregate team events
// (Team set, no Leg) - the unit that lets routing learn "this COMBINATION of
// models works on this class of task".
type TeamStat struct {
	N          int
	Scored     int
	AvgQuality float64
	ByClass    map[Class]ClassStat
}

// TeamStats aggregates ensemble-level events per canonical team key.
func (l *Ledger) TeamStats() map[string]TeamStat {
	type agg struct {
		n, scored int
		q         float64
		byClass   classAgg
	}
	sum := map[string]*agg{}
	for _, e := range l.Events {
		if e.Team == "" || e.Leg != "" || e.Outcome != "ok" {
			continue // only ensemble-level aggregates
		}
		a, ok := sum[e.Team]
		if !ok {
			a = &agg{byClass: classAgg{}}
			sum[e.Team] = a
		}
		a.n++
		if e.Quality > 0 {
			a.scored++
			a.q += e.Quality
		}
		a.byClass.add(e)
	}
	out := map[string]TeamStat{}
	for team, a := range sum {
		ts := TeamStat{N: a.n, Scored: a.scored, ByClass: a.byClass.finish()}
		if a.scored > 0 {
			ts.AvgQuality = a.q / float64(a.scored)
		}
		out[team] = ts
	}
	return out
}

func (l *Ledger) Stats() map[Leg]LegStats {
	sum := map[Leg]*struct {
		n, scored     int
		q, cost       float64
		durMs, tokens int64
	}{}
	byClass := map[Leg]classAgg{}
	byDomain := map[Leg]map[Domain]*struct {
		n, scored int
		q         float64
	}{}
	fails := map[Leg]int{}
	harness := map[Leg]int{}
	last := map[Leg]time.Time{}
	for _, e := range l.Events {
		if e.Leg != "" && e.At.After(last[e.Leg]) {
			last[e.Leg] = e.At // a failed run still dates the evidence
		}
		if e.Leg != "" && e.Outcome != "ok" {
			// Harness faults (our bugs: auth/keychain/crash) must not read as
			// model unreliability - claude "failed" 15× during the keychain
			// incident while being the best-rated leg (usage analysis F1/I4).
			if harnessFault(e.Error) {
				harness[e.Leg]++
			} else {
				fails[e.Leg]++
			}
		}
		if e.Outcome != "ok" || e.Leg == "" {
			continue // team aggregates (no leg) have their own TeamStats
		}
		// Class-conditioned priors describe how well the ROUTER's own choices
		// work out. A workflow's legs were chosen by the user and its task was
		// never classified by a director, so those runs stay out of ByClass -
		// they still count for N, duration, cost, reliability and the leg's
		// overall assessed quality (spec: WORKFLOW_LANGUAGE.md §6).
		if e.Workflow == "" {
			if byClass[e.Leg] == nil {
				byClass[e.Leg] = classAgg{}
			}
			byClass[e.Leg].add(e)
		}
		if e.Domain != "" {
			if byDomain[e.Leg] == nil {
				byDomain[e.Leg] = map[Domain]*struct {
					n, scored int
					q         float64
				}{}
			}
			b := byDomain[e.Leg][Domain(e.Domain)]
			if b == nil {
				b = &struct {
					n, scored int
					q         float64
				}{}
				byDomain[e.Leg][Domain(e.Domain)] = b
			}
			b.n++
			if e.Quality > 0 {
				b.scored++
				b.q += e.Quality
			}
		}
		s, ok := sum[e.Leg]
		if !ok {
			s = &struct {
				n, scored     int
				q, cost       float64
				durMs, tokens int64
			}{}
			sum[e.Leg] = s
		}
		s.n++
		s.durMs += e.Duration
		s.tokens += int64(e.Tokens)
		s.cost += e.CostUSD
		if e.Quality > 0 {
			s.scored++
			s.q += e.Quality
		}
	}
	out := map[Leg]LegStats{}
	for leg, n := range fails {
		if _, ok := sum[leg]; !ok {
			out[leg] = LegStats{Fails: n}
		}
	}
	for leg, n := range harness {
		if st, ok := out[leg]; ok {
			st.HarnessFails = n
			out[leg] = st
		} else if _, seen := sum[leg]; !seen {
			out[leg] = LegStats{HarnessFails: n}
		}
	}
	for leg, s := range sum {
		st := LegStats{N: s.n, Scored: s.scored, AvgDurationMs: s.durMs / int64(s.n), AvgTokens: int(s.tokens / int64(s.n)), TotalCostUSD: s.cost, Fails: fails[leg], HarnessFails: harness[leg], ByClass: byClass[leg].finish()}
		if s.scored > 0 {
			st.AvgQuality = s.q / float64(s.scored)
		}
		if bd := byDomain[leg]; len(bd) > 0 {
			st.ByDomain = map[Domain]ClassStat{}
			for d, b := range bd {
				cs := ClassStat{N: b.n, Scored: b.scored}
				if b.scored > 0 {
					cs.AvgQuality = b.q / float64(b.scored)
				}
				st.ByDomain[d] = cs
			}
		}
		out[leg] = st
	}
	for leg, at := range last {
		st, ok := out[leg]
		if !ok {
			continue // a leg with neither ok runs nor failures has no stats row
		}
		st.LastAt = at
		out[leg] = st
	}
	return out
}

const maxEvents = 500

func LoadLedger() (*Ledger, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".captaincode")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		_ = os.Rename(filepath.Join(home, ".superagent"), dir) // carry over pre-rename state
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &Ledger{
		Sessions:  map[string]string{},
		Threads:   map[string]ThreadRef{},
		Cooldowns: map[Leg]time.Time{},
		path:      filepath.Join(dir, "state.json"),
	}
	data, err := os.ReadFile(l.path)
	if err == nil {
		_ = json.Unmarshal(data, l) // corrupt state starts fresh, never blocks
	}
	if l.Threads == nil { // upgrading state.json written before Threads existed
		l.Threads = map[string]ThreadRef{}
	}
	return l, nil
}

// Persistent says whether this ledger has a file behind it. A background
// charge (memory consolidation) saves itself because no turn follows that
// would; an in-memory ledger must not be asked to write to "".
func (l *Ledger) Persistent() bool { return l.path != "" }

func (l *Ledger) Save() error {
	if l.path == "" {
		return nil
	}
	// Several processes share state.json: the long-lived brain and any CLI
	// invocation each load it once and write back. A stale writer would drop
	// the rows another process appended while it ran (notably the charge rows
	// the eval attributes cost from). Merge the append-only collections the
	// file gained since this ledger loaded, so a save can only add.
	l.mergeFromDisk()
	if len(l.Events) > maxEvents {
		l.Events = l.Events[len(l.Events)-maxEvents:]
	}
	if len(l.Charges) > maxCharges {
		l.Charges = l.Charges[len(l.Charges)-maxCharges:]
	}
	if len(l.Decisions) > maxDecisions {
		l.Decisions = l.Decisions[len(l.Decisions)-maxDecisions:]
	}
	if len(l.Shadows) > maxShadows {
		l.Shadows = l.Shadows[len(l.Shadows)-maxShadows:]
	}
	if len(l.Budgets) > maxBudgets {
		l.Budgets = l.Budgets[len(l.Budgets)-maxBudgets:]
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// mergeFromDisk unions the file's append-only rows into this ledger, keyed so
// a row already held in memory wins (memory carries this process's updates).
func (l *Ledger) mergeFromDisk() {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	var disk Ledger
	if json.Unmarshal(data, &disk) != nil {
		return
	}
	l.Charges = mergeByKey(l.Charges, disk.Charges, func(c Charge) string { return c.ID })
	l.Decisions = mergeByKey(l.Decisions, disk.Decisions, jsonKey)
	l.Shadows = mergeByKey(l.Shadows, disk.Shadows, jsonKey)
	l.Budgets = mergeByKey(l.Budgets, disk.Budgets, func(b Budget) string { return b.TaskID })
	l.TaskStates = mergeByKey(l.TaskStates, disk.TaskStates, func(t TaskState) string { return t.TaskID })
	l.AttemptStates = mergeByKey(l.AttemptStates, disk.AttemptStates, func(a AttemptState) string { return a.AttemptID })
	l.Handoffs = mergeByKey(l.Handoffs, disk.Handoffs, func(h HandoffBrief) string { return h.TaskID })
	l.Outcomes = mergeByKey(l.Outcomes, disk.Outcomes, func(o OutcomeEvidence) string { return o.TaskID })
	l.Snapshots = mergeByKey(l.Snapshots, disk.Snapshots, func(s PolicySnapshot) string { return s.ID })
	l.Canaries = mergeByKey(l.Canaries, disk.Canaries, func(c Canary) string { return c.ID })
	l.Events = mergeByKey(l.Events, disk.Events, jsonKey)
}

// mergeByKey appends rows from disk whose identity is not already in memory.
func mergeByKey[T any](mem, disk []T, key func(T) string) []T {
	if len(disk) == 0 {
		return mem
	}
	seen := make(map[string]bool, len(mem))
	for _, v := range mem {
		seen[key(v)] = true
	}
	for _, v := range disk {
		k := key(v)
		if k == "" || seen[k] {
			continue
		}
		mem = append(mem, v)
		seen[k] = true
	}
	return mem
}

// jsonKey identifies a record by its full serialized form. Used for rows that
// carry no id field of their own (events, decisions), which are immutable once
// written so byte-identical records are true duplicates.
func jsonKey[T any](v T) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (l *Ledger) Record(e Event) {
	e.At = time.Now()
	stampDomain(&e) // per-domain priors need every event tagged (analysis I5)
	l.Events = append(l.Events, e)
}

// Cooldown marks a leg rate-limited. Windows differ per provider (Claude 5h,
// Codex 5h+weekly, free models minutes) - v0 uses one conservative default
// and refines from observed reset messages later.
func (l *Ledger) Cooldown(leg Leg, d time.Duration) {
	l.Cooldowns[leg] = time.Now().Add(d)
}

// harnessFault classifies a failure as OURS (auth, keychain, crash residue)
// rather than the provider's. These patterns come from real incidents - keep
// them narrow: an unknown error is the provider's until proven otherwise.
func harnessFault(msg string) bool {
	m := strings.ToLower(msg)
	for _, p := range []string{
		"not logged in", "please run /login", "secitemcopymatching", "keychain",
		"exited but did not release its output pipes",
		// Codex CLI (codex-cli leg): its own credential store, 401 when dead.
		"codex login", "401 unauthorized", "missing bearer",
		// A provider rejecting a leg's key (ErrProviderAuth): the operator's
		// key, not the model.
		"credentials rejected",
		// cursor-agent's dead login.
		"authentication required", "agent login",
	} {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

// stampDomain fills Event.Domain from triage when the caller didn't.
func stampDomain(e *Event) {
	if e.Domain == "" && e.Task != "" {
		e.Domain = string(TriageTask(e.Task).Domain)
	}
}
