// Captain "brain" service: the Go router exposed over a small local HTTP API so
// the opencode fork (captain-code's frontend) can call it. The brain owns the
// DECISIONS and the scorecards; the frontend owns EXECUTION and the UI.
//
// Flow per user prompt in the frontend:
//
//  1. POST /v1/route {task}          → director assesses complexity + picks the
//     worker model + writes its brief (heuristic ladder only on skip/failure).
//
//  2. frontend runs that model, streams it in opencode's TUI.
//
//  3. POST /v1/assess {task,output,…} → director scores it; the brain records the
//     event to the ledger (scorecards learn).
//
//  4. GET  /v1/stats                  → live per-leg scorecards.
//
//     captain brain            # serve on 127.0.0.1:14097 (default)
//     captain brain --addr :N  # custom address
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const brainAddr = "127.0.0.1:14097"

// cmdBrain runs the decision API. Called as `captain brain [--addr host:port]`.
func cmdBrain(args []string) {
	fs := flag.NewFlagSet("brain", flag.ExitOnError)
	addr := fs.String("addr", brainAddr, "listen address")
	_ = fs.Parse(args)

	b := &brain{
		mgr:        captaincode.Manager{Director: captaincode.Director, Port: opencodePort},
		allowed:    map[captaincode.Leg]bool{},
		escalation: captaincode.DefaultEscalationPolicy(),
		processID:  captaincode.CurrentProcessID(),
		cancelTree: captaincode.NewCancelTree(),
	}
	// CAPTAIN_LEGS=free,codex restricts the worker pool to models the fork's
	// opencode can actually run (unset = all). The director never picks a leg
	// outside this set, so it can't route to an unconfigured provider.
	if v := os.Getenv("CAPTAIN_LEGS"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if leg := captaincode.Leg(strings.TrimSpace(s)); leg != "" {
				b.allowed[leg] = true
			}
		}
	}
	if jevTriageEnabled() {
		b.jevBackends = captaincode.SystemOneBackendsFromEnv()
		if b.jev = b.jevBackends.Primary; b.jev != nil {
			fmt.Printf("captain brain: triage tier 1 asks jev (%s) first, the free-leg classify is the fallback (CAPTAIN_TRIAGE_JEV=0 turns it off)\n", b.jev.Model)
		}
		if b.jevBackends.Open != nil {
			for _, line := range b.jevBackends.Describe() {
				fmt.Println("captain brain: " + line)
			}
		}
	}
	var err error
	if b.ledger, err = captaincode.LoadLedger(); err != nil {
		fatal(err)
	}
	// M3.3: on startup, mark any in-flight tasks/attempts as interrupted.
	// The process that owned them is gone; M3.4 decides whether to resume.
	if n := b.ledger.ReconcileOnStartup(); n > 0 {
		fmt.Printf("captain brain: %d interrupted attempt(s) reconciled on startup\n", n)
		if saveErr := b.ledger.Save(); saveErr != nil {
			fmt.Fprintf(os.Stderr, "captain brain: save after reconcile: %v\n", saveErr)
		}
	}
	// M3.4: evaluate interrupted attempts with the resume policy. The default
	// is conservative — surface for review, do not auto-resume — because a
	// provider session cannot be resumed across a brain restart and a worktree
	// may have been manually changed. Abandon anything stale beyond MaxAge.
	if report := captaincode.EvaluateInterrupted(b.ledger, captaincode.DefaultResumePolicy(), time.Now()); true {
		if msg := captaincode.FormatResumeReport(report); msg != "" {
			fmt.Print("captain brain: " + msg)
			for _, as := range report.Abandon {
				b.ledger.TransitionAttempt(as.AttemptID, captaincode.StateFailed)
			}
			if len(report.Abandon) > 0 {
				b.ledger.Save()
			}
		}
	}
	// M3.3: reconcile checkpoints — verify worktree and diff survival for
	// interrupted attempts so the resume report carries whether recovery is
	// even possible. Paths on AttemptState are references; survival is fact.
	if checkpoints := b.ledger.ReconcileCheckpoints(); len(checkpoints) > 0 {
		for _, cs := range checkpoints {
			fmt.Printf("captain brain: %s\n", captaincode.FormatCheckpointStatus(cs))
		}
	}
	// Synced benchmark priors (captain priors sync --apply) override the
	// compiled cold-start anchors; local scored runs still dominate routing.
	if n, err := captaincode.LoadRegistry(""); err != nil {
		fmt.Fprintf(os.Stderr, "captain brain: legs.json: %v\n", err)
	} else if n > 0 {
		fmt.Printf("captain brain: %d registry overlay entries loaded (%d legs active)\n", n, len(captaincode.AllLegs))
	}
	if n, err := captaincode.ReloadLegNotes(); err != nil {
		fmt.Fprintf(os.Stderr, "captain brain: leg notes overlay: %v\n", err)
	} else if n > 0 {
		fmt.Printf("captain brain: %d refined leg notes loaded\n", n)
	}
	if n, err := captaincode.LoadPriorOverrides(); err != nil {
		fmt.Fprintf(os.Stderr, "captain brain: prior overrides: %v\n", err)
	} else if n > 0 {
		fmt.Printf("captain brain: %d quality priors overridden from %s\n", n, captaincode.PriorOverridesPath())
	}

	// One-shot policy sweep: a legacy or hand-edited context_policy.json is
	// canonicalized (and every correction printed) before it can grant less
	// than it appears to.
	if changed, notes := scrubContextPolicy(); changed {
		fmt.Printf("captain brain: context policy scrubbed (%d correction(s))\n", len(notes))
		for _, n := range notes {
			fmt.Printf("  · %s\n", n)
		}
	}

	b.startRosterRefresh()
	b.startADIRefresh() // the Agentic Determinism Index feed, for /deterministic (brain_pool.go)
	// Which director-capable legs can actually run here, before any policy
	// resolves: the compiled default (grok) is not installed on every machine,
	// and a director that cannot run fails every plan (live 2026-09-16).
	b.dirAvailable = directorAvailability()
	b.legReady = captaincode.LegReadiness()
	if skipped := unreadyLegs(b.legReady); skipped != "" {
		fmt.Printf("captain brain: legs that cannot run here - %s\n", skipped)
	}
	b.restoreDirectorMode() // after the roster: a mode resolves against the ranking
	// A brain with no persisted helm picks the first runnable preference now, so
	// health, /v1/director and the worker ladder agree from the first prompt
	// instead of naming the compiled default the machine cannot run.
	if b.dirLadder == nil {
		b.setDirector(b.firstRunnableDirector())
	}
	fmt.Printf("captain brain: director candidates - %s\n", directorCandidateSummary(b.dirAvailable))
	startProxy() // before the worker serve starts: its provider blocks are rewritten here
	wireProxy()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/route", b.route)
	mux.HandleFunc("/v1/assess", b.assess)
	mux.HandleFunc("/v1/stats", b.stats)
	mux.HandleFunc("/v1/workers", b.workers)
	mux.HandleFunc("/v1/roster", b.rosterHTTP)
	mux.HandleFunc("/v1/roster/upgrade", b.rosterUpgradeHTTP)
	mux.HandleFunc("/v1/legs/reopen", b.legReopenHTTP)
	mux.HandleFunc("/v1/proxy/stats", b.proxyStatsHTTP)
	mux.HandleFunc("/v1/activity", b.activityFeed)
	mux.HandleFunc("/v1/workflow/status", b.workflowStatus)
	mux.HandleFunc("/v1/repeat/stop", b.repeatStopHTTP)
	mux.HandleFunc("/v1/repeat/ctl", b.repeatCtlHTTP)
	mux.HandleFunc("/v1/btw", b.btwHTTP)
	mux.HandleFunc("/v1/interrupt", b.interruptHTTP)
	mux.HandleFunc("/v1/inbox", b.inboxHTTP)
	mux.HandleFunc("/v1/euclid/status", b.euclidStatusHTTP)
	mux.HandleFunc("/v1/euclid/distill", b.euclidDistillHTTP)
	mux.HandleFunc("/v1/euclid/fold", b.euclidFoldHTTP)
	mux.HandleFunc("/v1/euclid/bootstrap", b.euclidBootstrapHTTP)
	mux.HandleFunc("/v1/euclid/reindex", b.euclidReindex)
	// M3.4: cancellation and lifecycle visibility.
	mux.HandleFunc("/v1/cancel", b.cancelHTTP)
	mux.HandleFunc("/v1/lifecycle", b.lifecycleHTTP)
	mux.HandleFunc("/v1/resume", b.resumeHTTP)
	// M3.5: structured handoff briefs.
	mux.HandleFunc("/v1/handoff", b.handoffHTTP)
	// M5.1: task-wide outcome evidence (review, correction, regression).
	mux.HandleFunc("/v1/outcome", b.outcomeHTTP)
	// M4.1: versioned task API. One endpoint, seven operations in a stamped envelope.
	mux.HandleFunc("/v1/task", b.taskAPIHTTP)
	// Director control: get/set the active director at runtime.
	mux.HandleFunc("/v1/director", b.directorHTTP)
	// The Euclid dashboard's Regenerate button (see brain_euclid_index.go).
	mux.HandleFunc("/api/ping", b.euclidPing)
	mux.HandleFunc("/api/regenerate", b.euclidReindex)
	mux.HandleFunc("/api/file", b.euclidFile)
	mux.HandleFunc("/api/search", b.euclidSearchHTTP)
	mux.HandleFunc("/api/ask", b.euclidSearchHTTP)
	mux.HandleFunc("/api/git", b.euclidSearchHTTP)
	// OpenAI-compatible surface: opencode calls these to run captain's own
	// model wrappers (claude via claude -p). See brain_openai.go.
	mux.HandleFunc("/v1/chat/completions", b.chatCompletions)
	mux.HandleFunc("/v1/models", b.models)
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		// cwd is the brain's own folder. It no longer pins anything: every
		// request names its workspace (brain_workspace.go), so a launcher in
		// another folder reuses this brain instead of restarting it.
		w.Header().Set("X-Captain-Busy", fmt.Sprint(b.inflightRuns.Load()))
		// built: the running binary's mtime, so a launcher can tell a brain
		// that predates the captain on PATH (the sidebar it serves is loaded
		// from source and may call endpoints this brain never had, 2026-09-13).
		writeJSON(w, 200, map[string]any{"ok": true, "director": string(captaincode.Director), "cwd": defaultWorkspace().Dir, "busy": b.inflightRuns.Load(), "built": binaryMtime()})
	})

	// The launch check: the main brain and the project's local brain, as the
	// operator should hear it before the first prompt (filesystem only).
	fmt.Print(captaincode.RenderBrainChecks(captaincode.CheckBrains(euclidCwd())))
	fmt.Printf("captain brain: director=%s listening on http://%s\n", captaincode.Director, *addr)
	// SIGTERM/SIGINT (the launcher's restart, a plain kill): end every worker
	// run first. The CLI workers are this process's children; with no handler
	// the brain died instantly and they were re-parented to init, still
	// running, their answer going nowhere (a 26-minute claude -p in arc after
	// an -rr, 2026-09-19). Each run's Steer holds its stop; StopAll on every
	// live turn cancels the contexts and the transports kill their processes.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		b.steers.mu.Lock()
		turns := make([]*captaincode.Steer, 0, len(b.steers.live))
		for s := range b.steers.live {
			turns = append(turns, s)
		}
		b.steers.mu.Unlock()
		n := 0
		for _, s := range turns {
			n += len(s.StopAll())
		}
		fmt.Printf("captain brain: stopping - %d worker run(s) ended\n", n)
		time.Sleep(1500 * time.Millisecond) // let the transports reap their children
		os.Exit(0)
	}()
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fatal(err)
	}
}

type brain struct {
	mu      sync.Mutex // serialize ledger reads/writes and director calls
	mgr     captaincode.Manager
	ledger  *captaincode.Ledger
	last    *lastRoute               // most recent routing decision (for visible confirmation)
	lastBy  map[string]*lastRoute    // …and per workspace, for each TUI's own sidebar
	allowed map[captaincode.Leg]bool // CAPTAIN_LEGS allowlist (empty = all runnable)

	// planFn stubs Manager.Plan in tests; nil → real director call.
	planFn func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error)
	// assessFn stubs Manager.Assess in tests; nil → real director call.
	assessFn func(task, output, objective string) (captaincode.Assessment, error)
	// routeNoteFn stubs the director's routing of a /btw in tests (brain_btw.go).
	routeNoteFn func(note string, briefs map[captaincode.Leg]string) ([]captaincode.Leg, string, error)
	// runWorkerFn stubs worker execution in tests; nil → the real runner.
	runWorkerFn func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error)
	// assessMultiFn stubs Manager.AssessMulti in tests; nil → real director call.
	assessMultiFn func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error)
	// summarizeFn stubs compaction's summarizer in tests; nil → free leg.
	summarizeFn func(span, prev string) (string, error)
	cmu         sync.Mutex
	compact     map[string]*compactState // per-session iterative summaries (R1)
	// active is the live per-leg view served by /v1/workers.
	active activeRuns
	// steers are the turns a /btw note can reach (brain_btw.go).
	steers steerBook
	// inbox holds prompts for a folder's open TUI (brain_inbox.go).
	inbox inbox

	// autoDistill throttles background consolidation of the Euclid journal.
	autoDistill autoDistillState

	// chatFn stubs one repeat round in tests; nil → a real chatCompletions call.
	chatFn func(w *captureWriter)

	// roundSummaryFn stubs the per-round digest in tests; nil → compaction leg.
	roundSummaryFn func(text string) string
	rmu            sync.Mutex
	repeats        map[string]*repeatThread // detached /repeat threads
	pmu            sync.Mutex
	parallels      map[string]*parallelRun // detached /parallel runs
	// classifyLLMFn stubs triage tier 1 (free-leg classify) in tests.
	classifyLLMFn func(task string) (captaincode.Class, captaincode.Domain, error)
	// jev is triage tier 1 on the decision leg (TypeSafe System One) when
	// TYPESAFE_API_KEY is set and CAPTAIN_TRIAGE_JEV is not 0; nil otherwise.
	jev *captaincode.SystemOneClient
	// jevBackends is jev beside any open sidecar, with what the sidecar has
	// been promoted to decide (pkg systemone_open.go). b.jev is its primary:
	// the gate and the supervisor build their own clients from the same
	// environment and so never reach the sidecar at all.
	jevBackends captaincode.SystemOneBackends
	// compileFn stubs the workflow compile (director skill call) in tests.
	compileFn func(intent, convo string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats) (captaincode.CompiledWorkflow, error)
	// reviewFn stubs the workflow review call in tests; nil → doAssessMulti.
	reviewFn func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error)
	// captureTestFn stubs M3.2 test-evidence capture in tests; nil → the real
	// runner. A unit test must never shell out the project's whole test suite.
	captureTestFn func(ctx context.Context, dir string) (*captaincode.CheckEvidence, error)
	roster        rosterState // perf ranking + CLI versions for the sidebar (brain_roster.go)
	// frontierFn stubs the /frontier claude runner in tests.
	frontierFn func(task string, onDelta, onStatus func(string)) (captaincode.Result, error)
	// planRequiredFn observes the legs bound into a plan (test seam).
	planRequiredFn func(required []captaincode.Leg)
	// exploration (MM37): rng + test seam + tasks routed as exploration
	planHints map[captaincode.Leg]string // numeric menu hints for the plan in flight (set under mu)
	// inflightRuns counts worker runs in progress - THE busy signal for
	// deploys and `captain status`. The brain log's last line is not one: a
	// single-worker team run printed no "running" line and a restart cut an
	// 18-minute Arc cohort turn (2026-09-10).
	inflightRuns atomic.Int32
	rng          *rand.Rand
	exploreFn    func(c captaincode.Class) bool
	explored     map[string]time.Time
	// teamPlans caches route-time fan-out plans for the team wrapper (by task).
	// Own mutex: storeTeamPlan is called from route, which already holds mu.
	tmu        sync.Mutex
	teamPlans  map[string]captaincode.Plan
	teamPlanAt map[string]time.Time // when each plan was cached (teamPlanTTL)

	// Compiled-and-previewed workflows awaiting `/run wf_xxxx` (CWL, own mutex
	// for the same reason as teamPlans: compile may be called under mu).
	wmu           sync.Mutex
	workflowPlans map[string]workflowEntry
	live          *wfLive                // the running (or last) workflow on the machine, for /v1/workflow/status
	liveBy        map[string]*wfLive     // …and per workspace, for each TUI's own status
	inflight      map[string]*wfInflight // one execution per (workflow, task); retries attach
	solo          map[string]*soloRun    // same, for ordinary single-leg turns

	// Route-time task identities (ROADMAP M1.2). The director-side calls that
	// PRECEDE the worker - tier-1 classification and the director's plan -
	// happen in route, before recordRun would mint anything, so their spend
	// used to vanish. One identity is minted lazily on the first such call and
	// adopted by recordRun, making the turn a single tree. Own mutex: minted
	// with mu held (route), adopted without it.
	rtmu       sync.Mutex
	routeTurns map[string]routeTurn

	// Decision evidence awaiting a task identity (ROADMAP M2.1). A routing
	// choice is made before the turn has a task row - a fast-pathed turn may
	// never mint one - so the record waits here, keyed by task text, until
	// the turn that executes it resolves its identity. Guarded by rtmu.
	pendingDecisions map[string]pendingDecision

	amu  sync.Mutex // guards acts only (kept off the main mu so worker runs can log while a route holds mu)
	acts []activity // recent activity feed for the sidebar (routing picks + per-worker run/done)

	bmu sync.Mutex // guards budget mutations (M2.4): reserve/reconcile across concurrent workers

	// escalation (ROADMAP M2.5): bounded repair + escalation policy for
	// objective-check failures. The budget controls the aggregate ceiling;
	// the policy controls the per-failure sequence. Set at init from
	// DefaultEscalationPolicy(); overridable in tests.
	escalation captaincode.EscalationPolicy

	// processID identifies this brain process for ownership claims (M3.3).
	// A brain restart mints a new one; an interrupted attempt's owner is stale.
	processID string

	// cancelTree (ROADMAP M3.4) is the one cancellation signal for all
	// in-flight work: model calls, subprocesses, gates, review, nested work.
	// Keyed by task ID; Cancel cascades to children.
	cancelTree *captaincode.CancelTree

	// integration (ROADMAP M3.2): the last integration candidate per task,
	// so `captain why` and the review can see what each worker changed and
	// whether parallel workers conflicted. Guarded by imu.
	imu              sync.Mutex
	lastIntegrations map[string]captaincode.IntegrationCandidate

	// Director ladder (user preference 2026-07-19: claude → codex → grok).
	// The active director failing directorFallbackAfter times in a row demotes
	// to the next choice for directorFallbackFor; when the window expires the
	// first choice is retried. Guarded by mu (route holds it; recordRun
	// snapshots under it).
	dirLadder        []captaincode.Leg // [0] = first choice; nil → directorLadderFromEnv()
	dirIdx           int               // current rung in dirLadder
	dirFailStreak    int
	dirFallbackUntil time.Time
	// dirAvailable is which director-capable legs can actually run on this
	// machine (brain_director.go), computed once at startup. nil means "do not
	// filter", which is what unit tests and callers without a probe want. A
	// leg that is not available is skipped exactly like a cooling one, so the
	// compiled default (grok) cannot hold the helm on a machine with no xAI
	// credential and fail every plan (live 2026-09-16).
	dirAvailable map[captaincode.Leg]bool
	// legReady is the same question asked of every WORKER leg
	// (pkg/captaincode/readiness.go), snapshotted at startup and refreshed
	// with the roster. nil means "do not filter", which is what unit tests
	// and callers without a probe want. A leg that cannot run is rejected
	// before ranking, with its reason, instead of costing a round trip per
	// turn to discover (2026-09-22).
	legReady map[captaincode.Leg]captaincode.Readiness
	// The director policy (pkg director_pick.go): a mode re-resolves its
	// pick against the live ranking, usage and cooldowns; a memo keeps that
	// off the hot path (directorPickTTL).
	dirMode       captaincode.DirectorMode
	dirModePick   captaincode.DirectorPick
	dirModePickAt time.Time
}

const (
	directorFallbackAfter        = 3
	directorFallbackFor          = 15 * time.Minute
	directorRateLimitFallbackFor = 1 * time.Hour // overriden by the provider's reset time when available
)

// directorLadderFromEnv builds the director preference order. CAPTAIN_DIRECTOR
// accepts a comma list ("claude,codex,grok"); a single name (or unset) gets the
// compiled default first. Every other capable leg follows the preference, best
// ranked first: the preferred leg is often not installed (the compiled default
// is grok), and a helm whose only rungs cannot run fails every plan. Unknown
// names are dropped; duplicates collapse.
func directorLadderFromEnv() []captaincode.Leg {
	ladder := []captaincode.Leg{}
	seen := map[captaincode.Leg]bool{}
	add := func(l captaincode.Leg) {
		if captaincode.KnownLeg(l) && captaincode.DirectorCapable(l) && !seen[l] {
			seen[l] = true
			ladder = append(ladder, l)
		}
	}
	if v := os.Getenv("CAPTAIN_DIRECTOR"); v != "" {
		for _, s := range strings.Split(v, ",") {
			add(captaincode.Leg(strings.TrimSpace(s)))
		}
	}
	if len(ladder) == 0 {
		add(captaincode.Director)
	}
	for _, c := range captaincode.DirectorCandidates() {
		add(c.Leg)
	}
	return ladder
}

const directorPickTTL = time.Minute

// ladder is the director preference order. Under a mode, the first rung is
// the mode's current pick, re-resolved once a minute; a changed pick resets
// the fallback state, since the ladder it indexed is gone.
func (b *brain) ladder() []captaincode.Leg {
	if b.dirMode != captaincode.DirectorFixed {
		if time.Since(b.dirModePickAt) > directorPickTTL {
			if pick, ok := b.resolveDirectorMode(b.dirMode); ok {
				if pick.Leg != b.dirModePick.Leg {
					if b.dirModePick.Leg != "" {
						fmt.Printf("captain brain: director (%s) moves %s → %s - %s\n", b.dirMode, b.dirModePick.Leg, pick.Leg, pick.Reason)
					}
					b.dirLadder = directorLadderFromEnvWith(pick.Leg)
					b.dirIdx, b.dirFailStreak, b.dirFallbackUntil = 0, 0, time.Time{}
					captaincode.SetDirector(pick.Leg)
				}
				b.dirModePick = pick
			}
			b.dirModePickAt = time.Now()
		}
	}
	if b.dirLadder == nil {
		b.dirLadder = directorLadderFromEnv()
	}
	return b.dirLadder
}

// resolveDirectorMode runs the mode's policy against the world as it is now.
// The policy ranks by quality/usage and does not know what is installed, so a
// pick that cannot run here (or is cooling) is replaced by the best leg that
// can — the same skip effectiveDirector applies to the fixed ladder.
// Caller holds b.mu (usage reads the ledger; the history files are read-only).
func (b *brain) resolveDirectorMode(mode captaincode.DirectorMode) (captaincode.DirectorPick, bool) {
	var pick captaincode.DirectorPick
	switch mode {
	case captaincode.DirectorFrontier:
		pick, _ = captaincode.PickFrontierDirector()
	case captaincode.DirectorQuality:
		pick, _ = captaincode.PickQualityDirector()
	case captaincode.DirectorAuto:
		pick, _ = captaincode.PickAutoDirector(b.directorUsage(captaincode.DirectorWindow()), b.ledger.Cooldowns, b.dirModePick.Leg, time.Now())
	default:
		return captaincode.DirectorPick{}, false
	}
	if pick.Leg == "" {
		return captaincode.DirectorPick{}, false
	}
	if !b.directorOpen(pick.Leg) {
		best := b.firstRunnableDirector()
		return captaincode.DirectorPick{Leg: best, Reason: fmt.Sprintf("%s: %s is not runnable here → %s", mode, pick.Leg, best)}, true
	}
	return pick, true
}

// directorUsage is each leg's wall-clock time answering inside the trailing
// window: worker runs from the run history (every leg of a run, its full
// duration - a team charges each member) plus the ledger's director, review
// and classify calls. Seconds are what a subscription window meters, and they
// are what every leg has recorded since the first run; token counts are not.
func (b *brain) directorUsage(window time.Duration) captaincode.DirectorUsage {
	since := time.Now().Add(-window)
	usage := captaincode.DirectorUsage{}
	if recs, err := readHistory(0); err == nil {
		for _, r := range recs {
			if r.At.Before(since) {
				continue
			}
			for _, l := range r.Legs {
				usage[captaincode.Leg(l)] += time.Duration(r.DurationMs) * time.Millisecond
			}
		}
	}
	for _, c := range b.ledger.Charges {
		if c.Leg == "" || c.At.Before(since) {
			continue
		}
		switch c.Label {
		case "director", "review", "classify", "compaction", "distill":
			usage[c.Leg] += time.Duration(c.DurationMs) * time.Millisecond
		}
	}
	return usage
}

// setDirectorMode switches the policy (or pins a leg with DirectorFixed) and
// persists it, so a restart comes back with the same helm.
func (b *brain) setDirectorMode(mode captaincode.DirectorMode, fixed captaincode.Leg) (captaincode.DirectorPick, error) {
	b.dirMode = mode
	b.dirModePick, b.dirModePickAt = captaincode.DirectorPick{}, time.Time{}
	if mode == captaincode.DirectorFixed {
		b.setDirector(fixed)
		b.ledger.DirectorMode = "fixed:" + string(fixed)
		_ = b.ledger.Save()
		return captaincode.DirectorPick{Leg: fixed, Reason: "pinned"}, nil
	}
	b.ladder() // resolve now, so the answer names the pick
	if b.dirModePick.Leg == "" {
		b.dirMode = captaincode.DirectorFixed
		return captaincode.DirectorPick{}, fmt.Errorf("%s: no leg can direct", mode)
	}
	b.ledger.DirectorMode = string(mode)
	_ = b.ledger.Save()
	return b.dirModePick, nil
}

// restoreDirectorMode applies the persisted policy at brain start; the env
// (CAPTAIN_DIRECTOR=auto|frontier|quality) seeds it when nothing is persisted.
func (b *brain) restoreDirectorMode() {
	saved := b.ledger.DirectorMode
	if saved == "" {
		if m, ok := captaincode.ParseDirectorMode(strings.Split(os.Getenv("CAPTAIN_DIRECTOR"), ",")[0]); ok {
			saved = string(m)
		}
	}
	switch {
	case saved == "":
		return
	case strings.HasPrefix(saved, "fixed:"):
		if leg := captaincode.Leg(strings.TrimPrefix(saved, "fixed:")); captaincode.KnownLeg(leg) {
			b.setDirector(leg)
			fmt.Printf("captain brain: director pinned to %s (restored)\n", leg)
		}
	default:
		if m, ok := captaincode.ParseDirectorMode(saved); ok {
			b.dirMode = m
			b.ladder()
			fmt.Printf("captain brain: director mode %s (restored) → %s\n", m, b.dirModePick.Leg)
		}
	}
}

// setDirector changes the active director at runtime: rebuilds the director
// ladder with the named leg first, resets the fallback state, and calls
// captaincode.SetDirector so the worker ladder excludes the new director.
// Callers must hold b.mu.
func (b *brain) setDirector(d captaincode.Leg) {
	captaincode.SetDirector(d)
	b.dirLadder = directorLadderFromEnvWith(d)
	b.dirIdx = 0
	b.dirFailStreak = 0
	b.dirFallbackUntil = time.Time{}
}

// resetDirector restores the env/default director ladder and clears fallback
// state. The first runnable preference leads, so a reset on a machine without
// the compiled default does not put an unavailable leg back on the helm.
// Callers must hold b.mu.
func (b *brain) resetDirector() {
	b.setDirector(b.firstRunnableDirector())
}

// directorLadderFromEnvWith builds a director preference order with the given
// leg first, then every other capable leg by rank (matching directorLadderFromEnv
// but with an explicit primary instead of reading the env).
func directorLadderFromEnvWith(primary captaincode.Leg) []captaincode.Leg {
	ladder := []captaincode.Leg{}
	seen := map[captaincode.Leg]bool{}
	add := func(l captaincode.Leg) {
		if captaincode.KnownLeg(l) && captaincode.DirectorCapable(l) && !seen[l] {
			seen[l] = true
			ladder = append(ladder, l)
		}
	}
	add(primary)
	for _, c := range captaincode.DirectorCandidates() {
		add(c.Leg)
	}
	return ladder
}

// effectiveDirector returns the director to use right now: the current ladder
// rung while a fallback window is open, the first choice otherwise. Legs that
// are cooling down in the ledger (rate-limited by any call path — assessment,
// title, compaction) are skipped so the director piggybacks to the next-best
// model automatically; legs that cannot run on this machine (no credential, no
// binary — brain_director.go) are skipped the same way. Callers must either
// hold mu or tolerate a benign race (reads only).
func (b *brain) effectiveDirector() captaincode.Leg {
	l := b.ladder()
	now := time.Now()
	// If a fallback window is open, use the current rung — but skip it if
	// it is ALSO cooling in the ledger or unavailable here.
	if b.dirIdx > 0 && b.dirIdx < len(l) && now.Before(b.dirFallbackUntil) {
		if b.directorOpen(l[b.dirIdx]) {
			return l[b.dirIdx]
		}
	}
	// Walk the ladder from the top: skip legs cooling in the ledger or
	// unavailable on this machine.
	for _, leg := range l {
		if b.directorOpen(leg) {
			return leg
		}
	}
	return l[0]
}

// directorOpen reports whether a director leg can be used right now: it must be
// available on this machine (when a probe has run) and not cooling in the
// ledger. A nil dirAvailable means the probe has not run — tests and callers
// that supply their own ladder — and only cooldowns apply.
func (b *brain) directorOpen(l captaincode.Leg) bool {
	if b.dirAvailable != nil && !b.dirAvailable[l] {
		return false
	}
	if until, ok := b.ledger.Cooldowns[l]; ok && time.Now().Before(until) {
		return false
	}
	return true
}

// firstRunnableDirector is the first leg of the preference ladder that can
// actually run here, or the first preference when none can (so the failure
// names the leg the operator asked for rather than an empty helm).
//
// Runnable means installed and authenticated - NOT "not cooling": a
// cooldown is a window that reopens, and effectiveDirector already steps
// around it per request. Counting it here pinned the helm to codex for the
// rest of the day because claude happened to be benched for thirty minutes
// at the moment the brain restarted (live 2026-09-17).
func (b *brain) firstRunnableDirector() captaincode.Leg {
	l := directorLadderFromEnv()
	for _, leg := range l {
		if b.dirAvailable == nil || b.dirAvailable[leg] {
			return leg
		}
	}
	return l[0]
}

// noteDirectorOutcome tracks consecutive plan failures and walks the ladder.
// Call with the plan error (nil on success).
//
// A rate-limit error piggybacks to the next best model IMMEDIATELY: the
// director's window is closed, so retrying it on the next prompt wastes a
// full timeout cycle. A non-rate-limit failure still needs
// directorFallbackAfter consecutive misses before demoting, because a
// one-off JSON parse or network hiccup should not dethrone a good director.
func (b *brain) noteDirectorOutcome(err error) {
	if b.dirIdx != 0 && !time.Now().Before(b.dirFallbackUntil) {
		b.dirIdx = 0 // window over → back on the first choice
	}
	if err == nil {
		b.dirFailStreak = 0
		return
	}
	if errors.Is(err, captaincode.ErrRateLimited) {
		b.demoteDirector(err, "rate-limited")
		return
	}
	b.dirFailStreak++
	if b.dirFailStreak < directorFallbackAfter {
		return
	}
	b.demoteDirector(err, fmt.Sprintf("%d× in a row", directorFallbackAfter))
}

// demoteDirector advances the director ladder one rung and sets the fallback
// window. The reason string describes the trigger for the log.
func (b *brain) demoteDirector(err error, reason string) {
	b.dirFailStreak = 0
	l := b.ladder()
	from := l[min(b.dirIdx, len(l)-1)]
	if b.dirIdx < len(l)-1 {
		b.dirIdx++
	}
	dur := directorFallbackFor
	if errors.Is(err, captaincode.ErrRateLimited) {
		dur = directorRateLimitFallbackFor
		var rl *captaincode.RateLimitError
		if errors.As(err, &rl) && !rl.ResetAt.IsZero() {
			dur = time.Until(rl.ResetAt) + time.Minute
			if dur > 6*time.Hour {
				dur = 6 * time.Hour
			}
		}
	}
	b.dirFallbackUntil = time.Now().Add(dur)
	// The rung we advanced to may itself be unavailable or cooling here; report
	// the leg that will actually direct, not the raw index.
	to := b.effectiveDirector()
	if from != to {
		fmt.Printf("captain brain: director %s %s → %s directs for %s\n",
			from, reason, to, dur.Round(time.Second))
		b.pushActivity(activity{Kind: "route", Leg: string(to), Model: string(to),
			Text: fmt.Sprintf("director %s %s → %s directs for %s", from, reason, to, dur.Round(time.Second))})
	}
}

func (b *brain) plan(ws captaincode.Workspace, task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
	return b.planWith(ws, nil, task, class, prefer, open, stats, teams, allowFanOut)
}

// planWith is plan with caller-bound legs: a leg or /frontier named right
// after /team is a BINDING team member, passed to the director as such.
func (b *brain) planWith(ws captaincode.Workspace, required []captaincode.Leg, task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
	if b.planFn != nil {
		if b.planRequiredFn != nil {
			b.planRequiredFn(required)
		}
		return b.planFn(task, class, prefer, open, stats, teams, allowFanOut)
	}
	// Called under mu (route holds it): safe to read/update director state.
	// The workspace's memory rides along: the director plans with what the
	// project already decided and learned, not from the task alone.
	mgr := captaincode.Manager{Director: b.effectiveDirector(), Port: b.mgr.Port, Required: required, Hints: b.planHints, Memory: captaincode.DirectorMemoryWith(ws.Dir, ws.Brains)}
	mgr.CallLabel, mgr.OnCall = "director", b.chargeRoute(task)
	p, err := mgr.Plan(task, class, prefer, open, stats, teams, allowFanOut)
	b.noteDirectorOutcome(err)
	return p, err
}

// doAssess has the director score one worker run. taskID, when set, bills the
// scoring call (and directorJSON's corrective retry, which is a second real
// call) to that task as a "review" attempt - the learning loop is not free,
// and a baseline report that hides its own overhead is not a baseline.
// skills, when the worker held a shelf, adds the second question: of the
// procedures captain staged, which does this answer show being used, and
// were they worth their place (M3.9).
func (b *brain) doAssess(taskID, task, output, objective string, skills []captaincode.SkillRef) (captaincode.Assessment, error) {
	if b.assessFn != nil {
		return b.assessFn(task, output, objective)
	}
	// Called from recordRun WITHOUT mu - snapshot the effective director.
	b.mu.Lock()
	mgr := captaincode.Manager{Director: b.effectiveDirector(), Port: b.mgr.Port, Skills: skills}
	b.mu.Unlock()
	mgr.CallLabel, mgr.OnCall = "review", b.chargeAux(taskID)
	return mgr.Assess(task, output, objective)
}

// chargeAux bills a director-side provider call to the task that caused it:
// one attempt row per call, so a corrective retry is visible as its own try,
// and one call row carrying the money. A FAILED call is charged too - quota
// was spent producing nothing, and a report that only counts successes
// understates what routing costs. Must not be used from a path that already
// holds b.mu.
func (b *brain) chargeAux(taskID string) captaincode.CallHook {
	if taskID == "" {
		return nil
	}
	return func(leg captaincode.Leg, label string, res captaincode.Result, err error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.chargeCall(taskID, leg, label, res)
	}
}

// chargeOverhead bills a brain-side provider call that serves a TURN but is
// neither routing nor the worker - compaction's fold of the conversation,
// which runs on the way IN to the turn. The identity is MINTED, not consumed: the worker call
// that follows adopts the same task, so one turn stays one tree (M1.2).
// Must not be used from a path that already holds b.mu.
func (b *brain) chargeOverhead(task string) captaincode.CallHook {
	if b.ledger == nil {
		return nil
	}
	return func(leg captaincode.Leg, label string, res captaincode.Result, err error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.chargeCall(b.mintRouteTurn(task), leg, label, res)
	}
}

// chargeOwnTask bills a call that belongs to no user turn to a task row of
// its own: background memory work (journal distillation, register bootstrap,
// probe authoring) and the /repeat round digest, which runs after the round's
// own task identity has already been consumed. Both are real provider calls
// on the user's quota, and M1's "cost per accepted task" is wrong by whatever
// it spends off the books. It saves the ledger itself - no turn follows that
// would. Must not be used from a path that already holds b.mu.
func (b *brain) chargeOwnTask(label string) captaincode.CallHook {
	if b.ledger == nil {
		return nil
	}
	return func(leg captaincode.Leg, callLabel string, res captaincode.Result, err error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		taskID := captaincode.NewChargeID(captaincode.KindTask)
		b.ledger.RecordCharge(captaincode.Charge{ID: taskID, TaskID: taskID, Kind: captaincode.KindTask, Label: label})
		b.chargeCall(taskID, leg, callLabel, res)
		if b.ledger.Persistent() {
			if err := b.ledger.Save(); err != nil {
				fmt.Printf("captain brain: ledger not saved after %s: %v\n", label, err)
			}
		}
	}
}

// chargeRoute bills routing's OWN provider calls - tier-1 classification and
// the director's plan - to the task they are routing. The identity is minted
// on the FIRST such call: a turn that fast-paths on deterministic triage asks
// no model and must not leave an empty task row behind. Caller holds b.mu,
// and so does every path that fires the hook (route holds it across both
// calls), so the hook does not take it again.
func (b *brain) chargeRoute(task string) captaincode.CallHook {
	return func(leg captaincode.Leg, label string, res captaincode.Result, err error) {
		b.chargeCall(b.mintRouteTurn(task), leg, label, res)
	}
}

// chargeCall writes one attempt row and the call row under it. A separate
// attempt per call is deliberate: a corrective retry is its own try, not a
// second call on the first one. Only the call row carries money. Caller
// holds b.mu.
func (b *brain) chargeCall(taskID string, leg captaincode.Leg, label string, res captaincode.Result) {
	b.chargeUnder(taskID, taskID, leg, label, res.DurationMs, captaincode.CallUsage(leg, res.Tokens, res.CostUSD, nil))
}

// chargeUnder writes an attempt row under parent (the task itself for a solo
// call, a STAGE for one member of a fan-out) and the call row beneath it, and
// returns the attempt identity so the decision row can point at the same try.
// Caller holds b.mu.
func (b *brain) chargeUnder(taskID, parent string, leg captaincode.Leg, label string, durationMs int64, usage captaincode.Usage) string {
	attemptID := captaincode.NewChargeID(captaincode.KindAttempt)
	b.ledger.RecordCharge(captaincode.Charge{ID: attemptID, Parent: parent, TaskID: taskID, Kind: captaincode.KindAttempt, Leg: leg, Label: label})
	b.ledger.RecordCharge(captaincode.Charge{Parent: attemptID, TaskID: taskID, Kind: captaincode.KindCall, Leg: leg, Label: label,
		DurationMs: durationMs, Usage: usage})
	return attemptID
}

// openTask returns the identity for a turn that does NOT go through
// recordRun - a team fan-out or a workflow, where several workers answer one
// prompt. It adopts what routing minted (so the plan that chose the team is
// billed to the team's own task) or mints the task row itself.
func (b *brain) openTask(task string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.openTaskLocked(task)
}

// openTaskLocked is openTask with b.mu already held.
func (b *brain) openTaskLocked(task string) string {
	taskID, adopted := b.adoptRouteTurn(task)
	if !adopted {
		taskID = captaincode.NewChargeID(captaincode.KindTask)
		b.ledger.RecordCharge(captaincode.Charge{ID: taskID, TaskID: taskID, Kind: captaincode.KindTask, Label: truncate(task, 120)})
	}
	// This is the one point every turn passes through with its identity
	// settled, so it is where the routing evidence stops being pending and
	// becomes a record joined to the charges below it (ROADMAP M2.1).
	b.attachDecision(taskID, task)
	// Open the shared resource envelope: every provider call this turn makes
	// — worker, reroute, gate repair, narration nudge, director, review —
	// draws from one budget (ROADMAP M2.4).
	b.openBudget(taskID, truncate(task, 120))
	// M3.3: persist the task's lifecycle state. A task that already has a
	// state row (resumption, repeat) keeps its existing state; a new task
	// starts as admitted.
	if ts := b.ledger.TaskStateFor(taskID); ts == nil {
		now := time.Now()
		b.ledger.RecordTaskState(captaincode.TaskState{
			TaskID:    taskID,
			Label:     truncate(task, 120),
			State:     captaincode.StateAdmitted,
			StartedAt: now,
			UpdatedAt: now,
		})
	}
	return taskID
}

// chargeStage opens a fan-out under a task: the parallel workers of a team, or
// one stage of a workflow. Without it a turn that answers with five workers
// reads as five tasks, and the M1 report cannot say what one accepted task
// cost (ROADMAP M1.2). The stage row carries identity and timing only - the
// money stays on the call rows beneath it.
func (b *brain) chargeStage(taskID, label string) string {
	stageID := captaincode.NewChargeID(captaincode.KindStage)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ledger.RecordCharge(captaincode.Charge{ID: stageID, Parent: taskID, TaskID: taskID, Kind: captaincode.KindStage, Label: label})
	return stageID
}

// chargeMember bills one member of a fan-out to its stage and returns the
// attempt identity. A FAILED member is charged too: it consumed quota and the
// turn's bill is not just its survivors.
func (b *brain) chargeMember(taskID, stageID string, leg captaincode.Leg, label string, durationMs int64, usage captaincode.Usage) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.chargeUnder(taskID, stageID, leg, label, durationMs, usage)
}

// strictCostMode returns "strict" when the operator has set both a cost cap
// and CAPTAIN_STRICT=1, meaning legs that cannot report per-turn cost must be
// rejected at the routing gate. Returns "" in admission mode (the default).
// ROADMAP M2.4 remaining: admission/strict-mode distinction.
func (b *brain) strictCostMode() string {
	opts := captaincode.DefaultBudgetOpts()
	if opts.Strict && opts.MaxCostUSD > 0 {
		return captaincode.BudgetStrict
	}
	return ""
}

// ---- shared resource controller (ROADMAP M2.4) ----

// openBudget creates or adopts the shared resource envelope for a root task.
// Called from openTaskLocked, the single point every turn passes through, so
// every dispatch path — solo, team, workflow — gets a budget. The budget is
// persisted to the ledger so a crash with unreconciled reservations is
// visible (M2: no auto-continuation; M3 adds safe recovery).
func (b *brain) openBudget(taskID, label string) *captaincode.Budget {
	if b.ledger == nil {
		return nil
	}
	if existing := b.ledger.BudgetFor(taskID); existing != nil {
		return existing
	}
	budget := captaincode.NewBudget(taskID, label, captaincode.DefaultBudgetOpts())
	b.ledger.RecordBudget(budget)
	return budget
}

// reserveAttempt checks whether the task's budget has room for one more
// provider call and reserves it. Returns false when the cap is exceeded — the
// caller must NOT dispatch and must record a stopping reason. A task with no
// budget (nil ledger, or a budget with MaxAttempts=0) always reserves.
func (b *brain) reserveAttempt(taskID string) bool {
	if b.ledger == nil || taskID == "" {
		return true
	}
	budget := b.ledger.BudgetFor(taskID)
	if budget == nil {
		return true
	}
	b.bmu.Lock()
	defer b.bmu.Unlock()
	return budget.Reserve(1)
}

// reconcileAttempt settles one reserved call against actual usage. Called
// after every provider call completes (success or failure): the reservation
// moves to settled, and the actual cost is added. Caller does NOT hold b.mu —
// this method takes the ledger lock to persist.
func (b *brain) reconcileAttempt(taskID string, actualCostUSD float64) {
	if b.ledger == nil || taskID == "" {
		return
	}
	budget := b.ledger.BudgetFor(taskID)
	if budget == nil {
		return
	}
	b.bmu.Lock()
	budget.Reconcile(1, actualCostUSD)
	b.bmu.Unlock()
	b.mu.Lock()
	b.ledger.RecordBudget(budget)
	saveErr := b.ledger.Save()
	b.mu.Unlock()
	if saveErr != nil {
		fmt.Fprintf(os.Stderr, "captain brain: save budget: %v\n", saveErr)
	}
}

// stopBudget records why a task stopped dispatching. The first reason wins:
// the original cause is what the report needs.
func (b *brain) stopBudget(taskID, reason string) {
	if b.ledger == nil || taskID == "" {
		return
	}
	budget := b.ledger.BudgetFor(taskID)
	if budget == nil {
		return
	}
	b.bmu.Lock()
	budget.Stop(reason)
	b.bmu.Unlock()
	b.mu.Lock()
	b.ledger.RecordBudget(budget)
	b.mu.Unlock()
}

// budgetExhausted reports whether a task's budget refuses further dispatch.
func (b *brain) budgetExhausted(taskID string) bool {
	if b.ledger == nil || taskID == "" {
		return false
	}
	budget := b.ledger.BudgetFor(taskID)
	if budget == nil {
		return false
	}
	b.bmu.Lock()
	defer b.bmu.Unlock()
	return budget.Exhausted()
}

// setLastIntegration stores the most recent integration candidate for a task
// (ROADMAP M3.2), so `captain why` and the review can report what each worker
// changed and whether parallel workers conflicted.
func (b *brain) setLastIntegration(taskID string, ic captaincode.IntegrationCandidate) {
	if taskID == "" {
		return
	}
	b.imu.Lock()
	defer b.imu.Unlock()
	if b.lastIntegrations == nil {
		b.lastIntegrations = map[string]captaincode.IntegrationCandidate{}
	}
	b.lastIntegrations[taskID] = ic
}

// lastIntegration returns the most recent integration candidate for a task.
func (b *brain) lastIntegration(taskID string) (captaincode.IntegrationCandidate, bool) {
	b.imu.Lock()
	defer b.imu.Unlock()
	ic, ok := b.lastIntegrations[taskID]
	return ic, ok
}

// routeTurn is a task identity minted during routing, waiting for the worker
// turn that will adopt it.
type routeTurn struct {
	id string
	at time.Time
}

// routeTurnTTL bounds how long that identity waits, for the same reason
// teamPlanTTL bounds a cached plan: the wrapper call follows its route within
// seconds. Adopting an older one would fold two separate turns of the same
// prompt text into one task.
const routeTurnTTL = 10 * time.Minute

// mintRouteTurn returns this task's route-time identity, creating (and
// recording) it on first use. Keyed by the same truncated task text the
// exploration tag uses, which is what recordRun can reconstruct from the
// wrapper's flattened prompt. Caller holds b.mu.
func (b *brain) mintRouteTurn(task string) string {
	key := truncate(task, 120)
	b.rtmu.Lock()
	defer b.rtmu.Unlock()
	if t, ok := b.routeTurns[key]; ok && time.Since(t.at) <= routeTurnTTL {
		return t.id
	}
	if b.routeTurns == nil {
		b.routeTurns = map[string]routeTurn{}
	}
	for k, t := range b.routeTurns { // opportunistic expiry
		if time.Since(t.at) > routeTurnTTL {
			delete(b.routeTurns, k)
		}
	}
	id := captaincode.NewChargeID(captaincode.KindTask)
	b.routeTurns[key] = routeTurn{id: id, at: time.Now()}
	b.ledger.RecordCharge(captaincode.Charge{ID: id, TaskID: id, Kind: captaincode.KindTask, Label: key})
	return id
}

// routeTurnID is this turn's identity if something already minted one, and ""
// otherwise. A free call joins the turn when there is a turn to join and
// leaves no row behind when there is not - minting one here would put an
// empty task on the ledger for a turn that asked nothing that cost anything.
func (b *brain) routeTurnID(task string) string {
	b.rtmu.Lock()
	defer b.rtmu.Unlock()
	if t, ok := b.routeTurns[truncate(task, 120)]; ok && time.Since(t.at) <= routeTurnTTL {
		return t.id
	}
	return ""
}

// adoptRouteTurn consumes the identity routing minted for this task. It is
// consumed rather than read: the next turn of the same prompt text is a new
// task and must not be charged onto this one. The bool says whether routing
// actually spent anything on this task - when it did not, no task row exists
// yet and the caller mints its own.
func (b *brain) adoptRouteTurn(task string) (string, bool) {
	key := truncate(task, 120)
	b.rtmu.Lock()
	defer b.rtmu.Unlock()
	t, ok := b.routeTurns[key]
	delete(b.routeTurns, key)
	return t.id, ok && time.Since(t.at) <= routeTurnTTL
}

// lastUserTurn extracts the newest [user] segment from the wrapper's flattened
// prompt - that's the task the worker actually answered; the rest is replayed
// history that would drown the scorecard's task column.
func lastUserTurn(prompt string) string {
	i := strings.LastIndex(prompt, "[user]\n")
	if i < 0 {
		return promptPeek(prompt)
	}
	seg := prompt[i+len("[user]\n"):]
	if j := strings.Index(seg, "\n\n["); j >= 0 {
		seg = seg[:j]
	}
	return strings.TrimSpace(seg)
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

// shouldAssess decides whether a run is worth a director call - the costly
// part of the learning loop. Explore/exploit: while a leg has fewer than
// CAPTAIN_ASSESS_MIN_SCORED graded runs (default 10) every substantial run is
// scored; once converged, only every CAPTAIN_ASSESS_REFRESH_EVERY-th run
// (default 10) buys a refresh so drift is still caught. Set both to 0 to
// disable scoring entirely (recording stays on - it's free).
func (b *brain) shouldAssess(leg captaincode.Leg) bool {
	minScored := envInt("CAPTAIN_ASSESS_MIN_SCORED", 10)
	refresh := envInt("CAPTAIN_ASSESS_REFRESH_EVERY", 10)
	b.mu.Lock()
	st := b.ledger.Stats()[leg]
	b.mu.Unlock()
	if minScored > 0 && st.Scored < minScored {
		return true
	}
	return refresh > 0 && (st.N+1)%refresh == 0
}

// rerouteTarget picks the next-best leg to rerun a task whose original leg's
// provider is down/at capacity: ladder order from the failed leg's rung,
// skipping cooled-down legs (the failed one was cooled just before this call),
// honoring CAPTAIN_LEGS, and keeping image tasks on vision-capable legs.
func (b *brain) rerouteTarget(failed captaincode.Leg, prompt string, alsoExclude ...captaincode.Leg) (captaincode.Leg, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	task := lastUserTurn(prompt)
	needVision := captaincode.TaskNeedsVision(task)
	excluded := map[captaincode.Leg]bool{failed: true}
	for _, l := range alsoExclude {
		excluded[l] = true
	}

	// Reliability gate: a leg that has failed a third of its runs (provider
	// faults only - harness faults don't count against it) is not a reroute
	// target while a cleaner leg is open. Live 2026-08-07: grok stalled and the
	// task was handed to glm - 2 provider fails in 6 runs, the flakiest leg on
	// the board - which promptly stalled too.
	stats := b.ledger.Stats()
	unreliable := func(l captaincode.Leg) bool {
		st := stats[l]
		total := st.N + st.Fails
		return st.Fails >= 2 && total > 0 && st.Fails*3 >= total
	}

	// Frontier-bound work fails over by performance, not by the fast ladder:
	// claude's closed window sent an architecture task to cursor (2026-09-10).
	// These deliberate picks are exempt from the frontier-class skip below.
	var candidates []captaincode.Leg
	preferred := map[captaincode.Leg]bool{}
	if captaincode.IsFrontierClass(failed) || failed == captaincode.LegClaude {
		// The chain is the perf ranking (captaincode.FrontierChain): the
		// frontier legs by index, then every other leg by index. When the
		// frontier tier is closed, claude itself (standard settings) comes
		// first as the strongest model; when none of them is open the work
		// lands on the next most capable leg - kimi, glm - not on whatever
		// the cheap ladder had at hand.
		for _, l := range captaincode.FrontierChain(failed) {
			if excluded[l] || (len(b.allowed) > 0 && !b.allowed[l]) {
				continue
			}
			if until, ok := b.ledger.Cooldowns[l]; ok && time.Now().Before(until) {
				continue
			}
			candidates = append(candidates, l)
			preferred[l] = true
		}
	}
	if triageEnabled() {
		tr := captaincode.TriageTask(task)
		candidates = append(candidates, b.cheapLadder(tr.Class, tr.Domain, needVision)...)
	}
	// Backstop: the classic rung ladder, so a reroute always finds SOME open leg.
	rung := 0
	for i, l := range captaincode.Rungs {
		if l == failed {
			rung = i
			break
		}
	}
	backstop := b.filterAllowed(captaincode.Pick(rung, b.ledger.Cooldowns, time.Now()))
	if needVision {
		if vo := captaincode.FilterVision(backstop); len(vo) > 0 {
			backstop = vo
		}
	}
	candidates = append(candidates, backstop...)

	// Pass 1: reliable, ordinary legs only - a reroute repairs a cheaper run,
	// it does not escalate to a frontier-class leg (codex-cli: minutes per turn,
	// ChatGPT-sub quota) while an ordinary leg is open. Pass 2: anything open
	// beats nothing.
	for _, pass := range []bool{true, false} {
		for _, l := range candidates {
			if excluded[l] {
				continue
			}
			if pass && (unreliable(l) || (captaincode.IsFrontierClass(l) && !preferred[l])) {
				continue
			}
			return l, true
		}
	}
	return "", false
}

// isReroutable: failures that are the PROVIDER's fault, not the task's - the
// same task on another leg is worth a try. An empty answer counts: a leg that
// finished and said nothing produced no work at all.
func isReroutable(err error) bool {
	return errors.Is(err, captaincode.ErrProviderDown) ||
		errors.Is(err, captaincode.ErrRateLimited) ||
		errors.Is(err, captaincode.ErrWorkerStalled) ||
		errors.Is(err, captaincode.ErrEmptyOutput) ||
		errors.Is(err, captaincode.ErrRefused)
}

// runWorkerRerouted runs a leg and, when its PROVIDER fails (down/at capacity
// - not the task's fault), cools the leg and reruns the same task once on the
// next-best leg instead of erroring the user's turn (xAI capacity bursts,
// 2026-07-19). Returns the leg that actually produced the result.
//
// taskID is the root task's shared budget identity (ROADMAP M2.4): when set,
// every provider call this function makes — the initial run and each reroute
// hop — reserves against and reconciles with that budget. Empty means the
// caller has no task identity yet (solo turn before recordRun): the budget
// check is a no-op and the per-path bounds (2 hops, chain time cap) still apply.
func (b *brain) runWorkerRerouted(ws captaincode.Workspace, leg captaincode.Leg, prompt string, onDelta, onStatus func(string), taskID string) (captaincode.Leg, captaincode.Result, error) {
	// A session title never reroutes. Rerouting exists so real work survives a
	// leg going down; spending a second leg to name a conversation is waste,
	// and it is what made a /frontier turn look like it ran on the free leg.
	titleRun := captaincode.IsTitlePrompt(prompt)
	var runOneRaw func(l captaincode.Leg, od, os_ func(string)) (captaincode.Leg, captaincode.Result, error)
	runOne := func(l captaincode.Leg, od, os_ func(string)) (captaincode.Leg, captaincode.Result, error) {
		b.inflightRuns.Add(1)
		defer b.inflightRuns.Add(-1)
		b.active.begin(ws, l, prompt)
		defer b.active.end(l)
		// /interrupt typed while the turn was still being prepared (compaction,
		// routing): the worker never starts - the prompt is withdrawn, nothing
		// spent (2026-09-18; before, "no worker is running" and the turn ran).
		if !ws.Steer.Interrupted().IsZero() {
			b.pushActivity(activity{Dir: ws.Dir, Kind: "done", Leg: string(l), Model: string(l), Text: "withdrawn by /interrupt before the worker started"})
			return l, captaincode.Result{}, fmt.Errorf("%s: withdrawn before it started: %w", l, captaincode.ErrInterrupted)
		}
		ws.Steer.Began(l) // nil-safe: a /btw can now reach this run (brain_btw.go)
		defer ws.Steer.Ended(l)
		// /deterministic: pin the request to the serving tuple ADI measured
		// green (provider_prefs, temperature 0) for as long as the run lasts,
		// through the egress proxy (brain_proxy.go).
		if ws.Pool.Deterministic {
			if pin := captaincode.ADIPinFor(l); pin != nil {
				if spec, ok := captaincode.Spec(l); ok {
					defer proxyPins.hold(spec.Model, pin)()
					if t, ok := captaincode.ADIFor(l); ok && os_ != nil {
						os_(fmt.Sprintf("deterministic: %s pinned to %s (ADI green, streak %d)", l, t.Label, t.Streak))
					}
				}
			}
		}
		// Tee everything the worker says and does into its on-disk log. When
		// nobody streams (team/workflow paths) the log still gets the text -
		// a cut run's narrative must survive somewhere (the Arc cohort's
		// report existed only in Claude's own transcript, 2026-09-10).
		wl := openWorkerLog(ws, l, prompt)
		if wl != nil {
			if od != nil {
				inner := od
				od = func(d string) { wl.delta(d); inner(d) }
			} else {
				od = wl.delta
			}
			innerS := os_
			os_ = func(s string) {
				wl.status(s)
				if innerS != nil {
					innerS(s)
				}
			}
		}
		// The supervisor shadow (brain_supervise.go): the decision leg is
		// asked, on a slow interval, whether this worker is stuck, off track,
		// or has reached something only the user can settle. Recorded beside
		// what the run turned out to be; acted on by nothing. Nil - and free -
		// when no decision leg is configured.
		sup := b.superviseStart(ws, l, prompt)
		os_ = sup.wrap(os_)
		ran, res, err := runOneRaw(l, od, os_)
		sup.close(res, err)
		if wl != nil {
			res.Log = wl.close(res, err)
		}
		turnSpend.add(ws.Steer, ran, res)     // the TUI's Context panel (brain_usage.go)
		if !ws.Steer.Interrupted().IsZero() { // the handoff (or the partial) goes to memory (brain_btw.go)
			noteHandoff(ws, ran, prompt, res, err)
		}
		if err != nil && !titleRun && (errors.Is(err, captaincode.ErrWorkerStalled) || errors.Is(err, captaincode.ErrWorkerTimeout)) {
			noteFailure(ws, fmt.Sprintf("%s run on %q ended by captain (%s) after %s - if the task legitimately takes this long, split it or run its long step to completion inside one worker",
				ran, promptPeek(lastUserTurn(prompt)), captaincode.ShortErr(err), (time.Duration(res.DurationMs)*time.Millisecond).Round(time.Second)))
		}
		journalWorkerRun(ws, ran, prompt, res, err, "worker") // Euclid journal, when a write brain exists
		b.maybeAutoDistill(ws)                                // …and consolidate it once enough has accumulated
		return ran, res, err
	}
	_ = runOne
	runOneRaw = func(l captaincode.Leg, od, os_ func(string)) (captaincode.Leg, captaincode.Result, error) {
		if captaincode.IsFrontier(l) {
			// The frontier pseudo-leg inside a team/reroute: claude at max
			// effort, recorded under claude - exactly as /frontier and a
			// workflow stage do (2026-09-09: a /team /frontier plan used to
			// hand "frontier" to the opencode dispatcher).
			run := b.frontierFn
			if run == nil {
				run = ws.RunClaudeFrontierStream
			}
			res, err := run(prompt, od, os_)
			// A limit that names the frontier tier ("your Fable limit") is
			// the frontier's to wear, not claude's: the CLI's other models
			// still answer, so claude stays open and is the first fallback.
			var rl *captaincode.RateLimitError
			if errors.As(err, &rl) && rl.Tier != "" {
				return captaincode.LegFrontier, res, err
			}
			return captaincode.LegClaude, res, err
		}
		if b.runWorkerFn != nil { // test seam
			return b.runWorkerFn(l, prompt, od, os_)
		}
		res, err := ws.RunWorkerStreamHooks(l, prompt, opencodePort, od, os_)
		return l, res, err
	}
	cur := leg
	// The frontier tier's window is closed: do not knock again, run claude
	// at standard settings straight away. The same for the claude leg at max
	// effort, which is the frontier configuration under claude's name.
	frontierClosed := func() (time.Time, bool) {
		b.mu.Lock()
		until, cooling := b.ledger.Cooldowns[captaincode.LegFrontier]
		b.mu.Unlock()
		return until, cooling && time.Now().Before(until)
	}
	if leg == captaincode.LegFrontier || (leg == captaincode.LegClaude && ws.Effort == captaincode.EffortMax) {
		if until, closed := frontierClosed(); closed {
			fmt.Printf("captain brain: frontier tier window closed until %s - running claude at standard settings\n", until.Format("15:04"))
			if onStatus != nil {
				onStatus(fmt.Sprintf("frontier tier limited until %s → claude", until.Format("15:04")))
			}
			cur = captaincode.LegClaude
			ws.Effort = captaincode.EffortHigh
		}
	}
	tried := []captaincode.Leg{leg, cur}
	// Reserve the first call against the task's shared budget. If the budget
	// refuses, the turn stops here rather than dispatching past its cap
	// (ROADMAP M2.4). A task with no budget (no ledger, or MaxAttempts=0)
	// always reserves.
	b.reserveAttempt(taskID)
	if taskID != "" {
		b.mu.Lock()
		// M3.3: checkpoint at dispatch boundary so the recovery policy knows
		// the attempt was producing when the process died.
		for _, as := range b.ledger.AttemptStatesFor(taskID) {
			if as.State == captaincode.StateRunning && as.ProcessID == b.processID {
				b.ledger.Checkpoint(as.AttemptID, captaincode.PhaseDispatching, "")
				break
			}
		}
		b.mu.Unlock()
	}
	ranLeg, res, err := runOne(cur, onDelta, onStatus)
	b.reconcileAttempt(taskID, res.CostUSD)
	b.recordQuotaFromHeaders(ranLeg, res)
	// The claude leg at max effort refused by the frontier tier's own limit
	// (spend cap, "your Fable limit"): that is the tier's to wear, and claude
	// at standard settings is the first fallback - the same turn, one rung
	// down, not another leg.
	if ranLeg == captaincode.LegClaude && ws.Effort == captaincode.EffortMax && !titleRun && !captaincode.WorthKeeping(res, err) {
		var rl *captaincode.RateLimitError
		if errors.As(err, &rl) && rl.Tier == "frontier" && b.reserveAttempt(taskID) {
			b.onWorkerError(captaincode.LegFrontier, err)
			fmt.Printf("captain brain: frontier tier limited on claude at max effort → claude at standard settings\n")
			if onStatus != nil {
				onStatus("frontier tier limited → claude at standard settings")
			}
			ws.Effort = captaincode.EffortHigh
			ranLeg, res, err = runOne(captaincode.LegClaude, onDelta, onStatus)
			b.reconcileAttempt(taskID, res.CostUSD)
			b.recordQuotaFromHeaders(ranLeg, res)
		}
	}
	// Up to TWO reroute hops - but never a NEW hop once the chain has already
	// consumed a full worker budget: stacked 15m caps made one stage crawl 45
	// minutes (2026-08-07). Better a clear failure than a zombie chain.
	chainStart := time.Now()
	// A failed run that carries a partial deliverable (a rate limit after
	// real work) is NOT rerouted: a fresh leg would start over from zero and
	// the caller salvages the text instead (45 minutes of frontier work went
	// to cursor as a blank slate, 2026-09-10).
	for hop := 0; hop < 2 && !titleRun && err != nil && isReroutable(err) && !captaincode.WorthKeeping(res, err) &&
		time.Since(chainStart) < captaincode.WorkerTimeout()*time.Duration(captaincode.NoTimeoutMul(prompt)); hop++ {
		// Shared budget gate (ROADMAP M2.4): a reroute is another provider
		// call that draws from the same root task budget. If the cap is
		// exhausted, stop rather than dispatch past it.
		if !b.reserveAttempt(taskID) {
			b.stopBudget(taskID, captaincode.StopAttemptsExhausted)
			fmt.Printf("captain brain: budget exhausted - stopping reroute chain after %d hops\n", hop)
			break
		}
		b.onWorkerError(ranLeg, err) // cool the failed leg NOW so rerouteTarget skips it
		fb, ok := b.rerouteTarget(cur, prompt, tried...)
		if !ok {
			b.stopBudget(taskID, captaincode.StopAllLegsFailed)
			return ranLeg, res, err
		}
		fmt.Printf("captain brain: %s provider failed (%v) → rerouting task to %s\n", ranLeg, err, fb)
		b.pushActivity(activity{Dir: ws.Dir, Kind: "route", Leg: string(fb), Model: string(fb),
			Text: fmt.Sprintf("%s unavailable → rerouted to %s", ranLeg, fb)})
		// The reroute marker is emitted LAZILY, with the new leg's first
		// output: announcing eagerly writes the SSE header, and if every hop
		// then fails instantly (all legs rate-limited) the fork would get a
		// 200 with an inline error instead of the real 429/503.
		mark := fmt.Sprintf("[captain: %s unavailable → rerouted to %s]\n\n", ranLeg, fb)
		od, os_ := onDelta, onStatus
		var once sync.Once
		if od != nil {
			inner := od
			od = func(d string) { once.Do(func() { inner(mark) }); inner(d) }
		} else if os_ != nil {
			inner := os_
			os_ = func(d string) { once.Do(func() { inner(mark) }); inner(d) }
		}
		cur = fb
		tried = append(tried, fb)
		ranLeg, res, err = runOne(cur, od, os_)
		b.reconcileAttempt(taskID, res.CostUSD)
		b.recordQuotaFromHeaders(ranLeg, res)
		if err == nil && res.Text != "" && onDelta == nil {
			// Buffered mode: nobody streamed the marker - carry it in the text.
			res.Text = mark + res.Text
		}
	}
	return ranLeg, res, err
}

// deliverableContract is appended to every worker prompt: agentic models
// (observed live with cursor-agent 2026-07-25) sometimes end their turn after
// NARRATING intent - "Drafting the plan…" with no plan - and the user has to
// type "do it, you stopped". The contract forecloses it up front.
// workerContext states plainly what the worker is being asked to do. Captain's
// prompts carried a task and nothing else, so a worker met an instruction with
// no setting - which invites misreading ordinary engineering as something else
// (live 2026-09-09: a provider began declining routine backend work in the
// user's own repository). This is accurate description, not persuasion: it says
// where the work happens and what kind of work it is, and nothing more.
func workerContext(ws captaincode.Workspace) string {
	dir := ws.Dir
	return fmt.Sprintf("\n\n[captain] Working context: you are a software engineer working in the user's own"+
		" repository at %s. This is ordinary development work - reading, writing, reviewing,"+
		" testing and documenting code in that repository, on the user's behalf and at their"+
		" direction. File paths, commands and services named in the task refer to that project.\n",
		dir) + captaincode.OrientationWith(dir, ws.Brains) // Euclid memory, when the project has a brain (MM38); the named repos' too
}

const deliverableContract = "\n\n[captain] End-of-turn contract: your FINAL message must contain the complete deliverable itself - the answer, plan, code, or verdict in full. Never end your turn describing what you are about to do. Format the deliverable for scanning: markdown with short paragraphs (≤4 lines each), bullet or numbered lists for enumerations, ### section headers when the answer runs long, and fenced code blocks for code/commands - never one large paragraph."

// narrationOnly detects an intention-only output: short, opens with an
// intent phrase, and contains no structured content. Deliberately narrow -
// a short real verdict or completion report must not trip it.
var intentOpener = regexp.MustCompile(`(?i)^(i'll|i will|let me|i'm going to|drafting|gathering)\b`)

func narrationOnly(out string) bool {
	t := strings.TrimSpace(out)
	if len(t) >= 600 || t == "" {
		return false
	}
	if !intentOpener.MatchString(t) {
		return false
	}
	// structured content = it actually delivered something
	if strings.Contains(t, "#") || strings.Contains(t, "\n-") || strings.Contains(t, "```") {
		return false
	}
	return true
}

// nudgeNarration reruns a worker once when it returned intentions instead of
// a deliverable, with the contract stated as bluntly as possible.
func (b *brain) nudgeNarration(ws captaincode.Workspace, leg captaincode.Leg, prompt string, res captaincode.Result, onDelta, onStatus func(string)) captaincode.Result {
	if !narrationOnly(res.Text) {
		return res
	}
	fmt.Printf("captain brain: %s returned narration only (%d chars) - nudging for the deliverable\n", leg, len(res.Text))
	b.pushActivity(activity{Dir: ws.Dir, Kind: "run", Leg: string(leg), Model: captaincode.ModelID(leg), Text: "narration-only output - nudged to produce the deliverable"})
	if onDelta != nil {
		onDelta("\n\n[captain: that was narration without a deliverable - nudging " + string(leg) + " to produce it]\n\n")
	}
	nudge := prompt + "\n\n[captain] Your previous attempt ended with intentions (\"" + promptPeek(res.Text) + "\") and NO deliverable. Produce the complete deliverable NOW, in this turn."
	_, res2, err := b.runWorkerRerouted(ws, leg, nudge, onDelta, onStatus, "")
	if err != nil || strings.TrimSpace(res2.Text) == "" {
		return res // keep the narration rather than an error
	}
	// The nudge is a SECOND provider call. Merging only its text would bill
	// the turn for one call and lose the first attempt's tokens entirely -
	// the exact undercount M1.2 exists to close. Carry both, and let
	// recordRun split them back into two attempts.
	res2.Text = res.Text + "\n\n" + res2.Text
	res2.Tokens += res.Tokens
	res2.CostUSD += res.CostUSD
	res2.DurationMs += res.DurationMs
	res2.Nudged = true
	return res2
}

// recordRun is the brain's OWN learning loop: after every successful wrapper
// run it records the event and (for substantial runs) has the director score
// it. This replaced the /v1/assess-from-the-frontend design, which nothing
// ever called - the ledger sat frozen on smoke-test trivia that rated the
// free leg q8.8, above claude, and routing steered by garbage (2026-07-19).
// Gates:
//   - internal runs (title generator) are never recorded;
//   - micro-outputs are recorded (n, duration, tokens) but not scored - a
//     director call costs real quota and "PONG" tells nothing about quality.
//
// diffDir returns the directory where solo-turn diff files are saved (M3.5).
func (b *brain) diffDir() string { return filepath.Join(captainHome(), "diffs") }

// stocked, when the turn staged a shelf, is what the worker held: the
// assessment grades it in the same call that grades the work (M3.9).
func (b *brain) recordRun(leg captaincode.Leg, prompt string, res captaincode.Result, wsDir string, stocked ...captaincode.SkillRef) {
	if strings.Contains(prompt, "You are a title generator") || captaincode.IsDistillRequest(prompt) {
		return
	}
	task := lastUserTurn(prompt)
	ev := captaincode.Event{
		Task: truncate(task, 120), Class: captaincode.Classify(task),
		Leg: leg, Reason: "wrapper", Outcome: "ok",
		Tokens: res.Tokens, CostUSD: res.CostUSD, Duration: res.DurationMs,
	}
	// API legs report tokens but not $: estimate from the registry price so
	// spend is visible for every leg, not just claude (MM37). CallUsage keeps
	// WHICH of the two this was, so the M1 report can separate a bill from a
	// price list (ROADMAP M1.2).
	usage := captaincode.CallUsage(leg, res.Tokens, res.CostUSD, nil)
	ev.CostUSD, ev.CostStatus = usage.CostUSD, usage.CostStatus
	b.mu.Lock()
	if b.wasExplored(task) {
		ev.Reason = "explore"
	}
	b.mu.Unlock()
	// The task identity is minted BEFORE the director is asked to score the
	// run, so the scoring call - a real provider call against real quota -
	// is billed to the task that caused it instead of vanishing (M1.2).
	b.mu.Lock()
	ev.TaskID, ev.AttemptID = b.chargeTurn(leg, task, usage, res.DurationMs, res.Nudged)
	b.mu.Unlock()
	// M3.5: capture what the solo worker changed in the user's workspace so
	// the handoff brief carries the same artifact evidence a parallel workflow
	// does. Skipped for title/distill calls and when wsDir is empty (tests).
	if wsDir != "" && ev.AttemptID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		files, digest, diffPath, err := captaincode.CaptureSoloArtifact(ctx, wsDir, b.diffDir(), string(leg))
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "captain brain: solo artifact capture: %v\n", err)
		} else if len(files) > 0 || digest != "" {
			b.mu.Lock()
			b.ledger.RecordSoloArtifact(ev.AttemptID, files, digest, diffPath)
			b.mu.Unlock()
		}
	}
	var grades []captaincode.SkillGrade
	if len(res.Text) >= 200 && res.DurationMs >= 5000 && b.shouldAssess(leg) {
		if a, err := b.doAssess(ev.TaskID, task, res.Text, "none", stocked); err == nil {
			ev.Quality, ev.Verdict, grades = a.Quality, a.Verdict, a.Skills
			if wsDir != "" { // the grade and its reasoning go to memory too (brain_euclid.go)
				b.mu.Lock()
				reviewer := b.effectiveDirector()
				b.mu.Unlock()
				journalReview(captaincode.Workspace{Dir: wsDir}, leg, reviewer, task, a.Quality, a.Verdict, a.Notes)
			}
			// M5.1: record the assessment as a solo-turn check result so
			// the outcome evidence carries what the director observed,
			// not just a pending status. A "poor" verdict is a failed
			// check the user can see in `captain outcome <id>`.
			b.mu.Lock()
			b.ledger.RecordCheckResult(ev.TaskID, captaincode.CheckResult{
				Command:  "director:assess",
				ExitCode: assessmentExitCode(a.Verdict),
				Passed:   a.Verdict != "poor",
				Source:   "solo",
				At:       time.Now(),
			})
			b.mu.Unlock()
		}
	}
	// M3.9: one row per stocked skill, graded or not. A run the director
	// never scored still records the stocking, because "stocked often, used
	// never" is the finding selection has to be able to make about itself.
	if len(stocked) > 0 {
		names := make([]string, 0, len(stocked))
		for _, r := range stocked {
			names = append(names, r.Name)
		}
		b.recordShelf(ev.TaskID, leg, ev.Class, captaincode.TriageTask(task).Domain, names, grades)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ledger.Record(ev)
	b.completeAttemptLocked(ev.TaskID, ev.AttemptID, captaincode.StateSucceeded)
	// M5.1: record a pending outcome for this task so the user can add
	// review verdicts, correction time, and regressions later.
	if o := b.ledger.OutcomeFor(ev.TaskID); o == nil {
		b.ledger.RecordOutcome(captaincode.OutcomeEvidence{
			TaskID: ev.TaskID,
			Task:   truncate(task, 120),
			Leg:    leg,
			Status: captaincode.AcceptancePending,
		})
	}
	// M5.1: and settle the ones whose evidence has since decided them. A
	// pending column that only a human verdict ever emptied stayed 100%
	// pending, which is a constant, not a signal (settle.go).
	b.ledger.SettleOutcomes(time.Now())
	if err := b.ledger.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "captain brain: save run record: %v\n", err)
	}
}

// chargeTurn writes the task/attempt/call spine for one wrapper turn and

// assessmentExitCode maps a director verdict to an exit-code convention: 0
// for good/acceptable (the output met the bar), 1 for poor (it did not).
func assessmentExitCode(verdict string) int {
	if verdict == "poor" {
		return 1
	}
	return 0
}

// chargeTurn writes the task/attempt/call spine for one wrapper turn and
// returns the task and attempt identities to stamp on the decision row. A
// plain turn is one attempt with one call today; director, review and repair
// calls attach to the same task as further attempts as they are threaded
// through. Only the call row carries money - the task and attempt rows exist
// so a later charge can find its parent. Caller holds b.mu.
func (b *brain) chargeTurn(leg captaincode.Leg, task string, usage captaincode.Usage, durationMs int64, nudged bool) (string, string) {
	taskID := b.openTaskLocked(task)
	attemptID := captaincode.NewChargeID(captaincode.KindAttempt)
	b.ledger.RecordCharge(captaincode.Charge{ID: attemptID, Parent: taskID, TaskID: taskID, Kind: captaincode.KindAttempt, Leg: leg, Label: "worker"})
	b.ledger.RecordCharge(captaincode.Charge{Parent: attemptID, TaskID: taskID, Kind: captaincode.KindCall, Leg: leg, Label: "worker", DurationMs: durationMs, Usage: usage})
	// M3.3: persist the attempt's lifecycle state. The attempt started
	// running under this brain's ownership; recordRun completes it.
	now := time.Now()
	b.ledger.RecordAttemptState(captaincode.AttemptState{
		AttemptID: attemptID,
		TaskID:    taskID,
		State:     captaincode.StateRunning,
		Leg:       leg,
		ProcessID: b.processID,
		OwnerGen:  1,
		StartedAt: now,
		UpdatedAt: now,
	})
	b.markTaskRunning(taskID)
	if nudged {
		// A nudged turn merged two calls into one Result and the runtimes
		// report no per-call split, so the repair attempt is recorded with
		// UNKNOWN usage rather than half of a number nobody measured. The
		// turn's known total already sits on the worker call above; a second
		// copy here would bill it twice.
		repairID := captaincode.NewChargeID(captaincode.KindAttempt)
		b.ledger.RecordCharge(captaincode.Charge{ID: repairID, Parent: taskID, TaskID: taskID, Kind: captaincode.KindAttempt, Leg: leg, Label: "repair"})
		b.ledger.RecordCharge(captaincode.Charge{Parent: repairID, TaskID: taskID, Kind: captaincode.KindCall, Leg: leg, Label: "repair",
			Usage: captaincode.Usage{Status: captaincode.UsageUnknown, CostStatus: captaincode.UsageUnknown}})
	}
	return taskID, attemptID
}

// markTaskRunning transitions a task from admitted to running. Caller holds b.mu.
func (b *brain) markTaskRunning(taskID string) {
	if ts := b.ledger.TaskStateFor(taskID); ts != nil && ts.State == captaincode.StateAdmitted {
		ts.State = captaincode.StateRunning
		ts.UpdatedAt = time.Now()
	}
}

// completeAttemptLocked transitions an attempt to a terminal state and marks
// the task terminal when all its attempts are done. Caller holds b.mu.
func (b *brain) completeAttemptLocked(taskID, attemptID string, state captaincode.LifecycleState) {
	if as := b.ledger.AttemptStateFor(attemptID); as != nil {
		if as.State.IsTerminal() {
			return
		}
		as.State = state
		as.UpdatedAt = time.Now()
		as.TerminalAt = time.Now()
	}
	allTerminal := true
	for _, as := range b.ledger.AttemptStatesFor(taskID) {
		if !as.State.IsTerminal() {
			allTerminal = false
			break
		}
	}
	if allTerminal {
		if ts := b.ledger.TaskStateFor(taskID); ts != nil && !ts.State.IsTerminal() {
			ts.State = state
			ts.UpdatedAt = time.Now()
			ts.TerminalAt = time.Now()
		}
		// M3.4: the task has no in-flight work; drop it from the cancel tree.
		b.cancelTree.Cancel(taskID)
		// M3.5: build and store the handoff brief from the final ledger state.
		b.buildHandoffLocked(taskID)
	}
}

// buildHandoffLocked assembles and stores the M3.5 handoff brief for a task
// from the current ledger state. Caller holds b.mu.
func (b *brain) buildHandoffLocked(taskID string) {
	b.imu.Lock()
	ic := b.lastIntegrations[taskID]
	b.imu.Unlock()
	requirements := ""
	for _, ev := range b.ledger.Events {
		if ev.TaskID == taskID && ev.Task != "" {
			requirements = ev.Task
			break
		}
	}
	brief := captaincode.BuildHandoffBrief(b.ledger, taskID, requirements, &ic)
	b.ledger.RecordHandoff(brief)
}

// cancelTask is the M3.4 cancellation entry point. It fires every cancel
// registered under the task (cascading to children: workflow stages, team
// members, gates, review), then transitions the lifecycle to cancelled.
func (b *brain) cancelTask(taskID string) []string {
	return b.cancelTaskWithDeadline(taskID).Cancelled
}

// cancelTaskWithDeadline is the M3.4 deadline-based cancellation: fires the
// cancel tree with adapter-specific deadlines, waits for the ack window, kills
// any stragglers, then transitions the lifecycle to cancelled. Returns what
// was cancelled and what was still alive after the deadline (killed).
func (b *brain) cancelTaskWithDeadline(taskID string) captaincode.CancelResult {
	deadline := captaincode.DefaultCancelDeadline()
	result := b.cancelTree.CancelWithDeadline(taskID, deadline)
	b.mu.Lock()
	for _, as := range b.ledger.AttemptStatesFor(taskID) {
		if as.State.IsTerminal() {
			continue
		}
		if captaincode.CanTransition(as.State, captaincode.StateCancelRequested) {
			b.ledger.TransitionAttempt(as.AttemptID, captaincode.StateCancelRequested)
		}
		if captaincode.CanTransition(as.State, captaincode.StateCancelled) {
			b.ledger.TransitionAttempt(as.AttemptID, captaincode.StateCancelled)
		}
	}
	if ts := b.ledger.TaskStateFor(taskID); ts != nil && !ts.State.IsTerminal() {
		if captaincode.CanTransition(ts.State, captaincode.StateCancelRequested) {
			b.ledger.TransitionTask(taskID, captaincode.StateCancelRequested)
		}
		if captaincode.CanTransition(ts.State, captaincode.StateCancelled) {
			b.ledger.TransitionTask(taskID, captaincode.StateCancelled)
		}
	}
	b.buildHandoffLocked(taskID)
	b.ledger.Save()
	b.mu.Unlock()
	return result
}

// activity is one entry in the live feed the TUI sidebar shows so captain is
// never "quiet": each routing decision and each worker start/finish is recorded
// with a timestamp, the leg, and (on finish) how long it took + an output peek.
// binaryMtime is the unix mtime of the running executable (0 when unknown).
// binaryMtime is read ONCE, at process start: `install -m 755` replaces the
// file under the running brain, and a stat at request time then reports the
// new binary's time for the old process - the launcher's "brain predates
// the binary" warning went silent right after an install (2026-09-16).
var binaryMtimeAtStart = func() int64 {
	exe, err := os.Executable()
	if err != nil {
		return 0
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	st, err := os.Stat(exe)
	if err != nil {
		return 0
	}
	return st.ModTime().Unix()
}()

func binaryMtime() int64 { return binaryMtimeAtStart }

type activity struct {
	At     string `json:"at"`
	Kind   string `json:"kind"` // "route" | "run" | "done"
	Leg    string `json:"leg"`
	Model  string `json:"model"`
	Text   string `json:"text"` // rationale (route) or output preview (done)
	Ms     int64  `json:"ms"`
	Effort string `json:"effort,omitempty"` // how hard the worker thinks on this run (a run's routing decision, effort.go)
	Dir    string `json:"-"`                // the workspace it happened in ("" = machine-wide, shown to every TUI)
}

// pushActivity appends to the feed (capped) with its own lock so a worker run can
// record while a route still holds the main mu.
func (b *brain) pushActivity(a activity) {
	a.At = time.Now().Format("15:04:05")
	b.amu.Lock()
	b.acts = append(b.acts, a)
	if len(b.acts) > 60 {
		b.acts = b.acts[len(b.acts)-60:]
	}
	b.amu.Unlock()
}

// activityFeed serves the recent feed newest-first for the sidebar to render.
func (b *brain) activityFeed(w http.ResponseWriter, r *http.Request) {
	dir, mine := workspaceFilter(r)
	b.amu.Lock()
	out := make([]activity, 0, len(b.acts))
	for i := len(b.acts) - 1; i >= 0; i-- { // newest first
		a := b.acts[i]
		if mine && a.Dir != "" && a.Dir != dir {
			continue // another TUI's project
		}
		out = append(out, a)
	}
	b.amu.Unlock()
	writeJSON(w, 200, map[string]any{"activity": out})
}

// filterAllowed keeps only legs in the CAPTAIN_LEGS allowlist (all if empty).
func (b *brain) filterAllowed(order []captaincode.Leg) []captaincode.Leg {
	if len(b.allowed) == 0 {
		return order
	}
	out := order[:0:0]
	for _, l := range order {
		if b.allowed[l] {
			out = append(out, l)
		}
	}
	return out
}

// lastRoute is the most recent decision, surfaced in /v1/stats and the sidebar
// so it's obvious captain (not plain opencode) is answering.
type lastRoute struct {
	Task      string `json:"task"`
	Leg       string `json:"leg"`
	Model     string `json:"model"`
	Rationale string `json:"rationale"`
	At        string `json:"at"`
	Dir       string `json:"-"` // the workspace the route was for
}

type routeReq struct {
	Task   string `json:"task"`
	Prefer string `json:"prefer,omitempty"`
	Forced string `json:"forced,omitempty"` // force a specific leg (skips the director)
	ws     captaincode.Workspace
}

type routeResp struct {
	Class     string `json:"class"`
	Leg       string `json:"leg"`
	Provider  string `json:"providerID"` // empty for the claude leg (runs via claude -p)
	Model     string `json:"modelID"`
	ViaClaude bool   `json:"viaClaude"` // true → run through claude -p, not a provider
	Brief     string `json:"brief"`
	Rationale string `json:"rationale"`
	Effort    string `json:"effort,omitempty"` // low|medium|high|max - from the preference, else the class (effort.go)
}

// route: director assesses complexity + picks the worker/brief (or honor a
// forced leg / fast-route / single-leg skip). Heuristic Classify is fallback only.// routeFail is a routing decision that cannot be made: no eligible leg, an
// unknown leg. It carries the HTTP status the /v1/route endpoint reports.
type routeFail struct {
	code int
	msg  string
}

// route is the HTTP face of decideRoute, used by the terminal to ask "which
// leg should answer this?" before it sends the turn.
func (b *brain) route(w http.ResponseWriter, r *http.Request) {
	var req routeReq
	if !decode(w, r, &req) {
		return
	}
	req.ws = workspaceOf(r)
	if req.Task == "" {
		writeErr(w, 400, "task required")
		return
	}
	resp, fail := b.decideRoute(req)
	if fail != nil {
		writeErr(w, fail.code, fail.msg)
		return
	}
	writeJSON(w, 200, resp)
}

// decideRoute picks the leg (or the team/frontier/workflow pseudo-leg) for a
// task. It is called from two places: the /v1/route endpoint, and the "auto"
// model inside chatCompletions - the latter so a caller never has to decide a
// leg before the user's message renders.
func (b *brain) decideRoute(req routeReq) (routeResp, *routeFail) {
	resp, fail := b.decideLeg(req)
	if fail != nil {
		return resp, fail
	}
	// The effort rides with the decision: the preference the user stated
	// (leading prefix or mid-prompt) outranks the class the router settled on.
	prefer := req.Prefer
	if prefer == "" {
		prefer = captaincode.MidPromptPrefer(req.Task)
	}
	if resp.Model == "frontier" || captaincode.MidPromptFrontier(req.Task) {
		prefer = "frontier"
	}
	resp.Effort = string(captaincode.EffortFor(prefer, captaincode.Class(resp.Class)))
	return resp, nil
}

// decideLeg is decideRoute without the effort: the leg, the brief, the class.
func (b *brain) decideLeg(req routeReq) (routeResp, *routeFail) {
	// Registered first, so it runs last - after the mu defer below has let go.
	defer b.rememberLast(req.ws.Dir, b.lastRoute())
	// The user named the workers AND an order ("grok, codex and THEN claude"):
	// that is a pipeline, and /team cannot express one - it runs a single
	// parallel stage, which is why three "sequenced cheap→deep" reviewers all
	// started at once (live 2026-07-30). Nothing is left for the director to
	// decide, so build the workflow deterministically, skip the plan call, and
	// let the wrapper execute it (model stays "team": it is already in the
	// fork's provider list, and the wrapper prefers a cached workflow).
	if workflowEnabled() && teamEnabled() && req.Forced == "" {
		if stages := captaincode.NamedStages(req.Task); len(stages) > 1 {
			wf, ok := b.workflowFromNamedStages(stages, req.Task)
			if !ok {
				fmt.Printf("captain brain: named sequence %v exceeds the workflow limits (or a leg is outside CAPTAIN_LEGS) → falling back to the director\n", stages)
			}
			if ok {
				b.storeWorkflowForTask(req.Task, wf)
				rationale := fmt.Sprintf("user named legs in sequence (%s) - running as a workflow, one reviewed output", wf.Key())
				b.mu.Lock()
				b.last = &lastRoute{Task: truncate(req.Task, 72), Leg: "workflow", Model: wf.Key(), Rationale: rationale, At: time.Now().Format("15:04:05")}
				b.mu.Unlock()
				fmt.Printf("captain brain: routed %q → workflow %s (named sequence, director skipped)\n", truncate(req.Task, 72), wf.Key())
				b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: "workflow", Model: wf.Key(), Text: rationale})
				return routeResp{Class: string(captaincode.Classify(req.Task)), Leg: "team", Provider: "captain",
					Model: "team", Brief: req.Task, Rationale: rationale}, nil
			}
		}
	}

	// Workflow control words ("/wf <english>", "/run wf_x") are handled by the
	// wrapper, not by routing: short-circuit so they never pay for a director
	// plan call (17-27s with claude directing) just to be intercepted later.
	if workflowEnabled() {
		if _, ok := workflowIntent(req.Task); ok {
			return routeResp{Class: string(captaincode.ClassTrivial), Leg: "claude", Provider: "captain",
				Model: "claude", Brief: req.Task, Rationale: "workflow compile (skill)"}, nil
		}
		if id, ok := workflowRunID(req.Task); ok {
			return routeResp{Class: string(captaincode.ClassTrivial), Leg: "claude", Provider: "captain",
				Model: "claude", Brief: req.Task, Rationale: "workflow run " + id}, nil
		}
	}

	// A preference stated ANYWHERE in the turn counts: the fork only parses
	// prefixes at position 0, so "…called OPSIS. /quality review…" was silently
	// fast-pathed against the user's explicit wish (live 2026-08-01). An actual
	// leading prefix (req.Prefer set by the fork) always wins.
	if req.Prefer == "" {
		if p := captaincode.MidPromptPrefer(req.Task); p != "" {
			req.Prefer = p
			fmt.Printf("captain brain: mid-prompt /%s honored for %q\n", p, truncate(req.Task, 60))
		}
	}

	// /oss and /deterministic narrow the legs a turn may run on (pool.go);
	// a forced leg is the user's word and is not second-guessed.
	pool := req.ws.Pool
	if pool.Empty() {
		pool = captaincode.MidPromptPool(req.Task) // a direct /v1/route caller
	}
	if req.Forced != "" && !pool.Empty() && !captaincode.PoolAllows(pool, captaincode.Leg(req.Forced)) {
		fmt.Printf("captain brain: /%s named with pool %s - the named leg wins (%s)\n", req.Forced, pool, captaincode.ADIOneLine(captaincode.Leg(req.Forced)))
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: req.Forced, Model: "pool",
			Text: fmt.Sprintf("/%s is outside the %s pool - the named leg wins", req.Forced, pool)})
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	t0 := time.Now()
	classHint := captaincode.Classify(req.Task)
	class := classHint
	leg := captaincode.Leg(req.Forced)
	brief, rationale := req.Task, "forced leg"
	if req.Forced != "" && captaincode.KnownLeg(leg) && !captaincode.ServesTasks(leg) {
		return routeResp{}, &routeFail{400, fmt.Sprintf("/%s is a %v - `captain jev classify <task>` asks it a question", leg, captaincode.ErrDecisionLeg)}
	}
	directorMs := int64(0) // time spent in the director LLM call (the hot-path cost)
	// Which mechanism actually settles the leg, for the decision record: a
	// user's forced leg and a director's plan are both "routing", and a report
	// that does not distinguish them cannot say whether the policy was
	// exercised at all (ROADMAP M2.1).
	decPath, decMenu := captaincode.PathForced, []captaincode.Leg(nil)
	// What the decision leg answered at the same points, when it was asked,
	// and who actually settled the class (brain_shadow.go).
	var shadow *captaincode.Shadow
	// openCh: the open sidecar answering the same questions beside captain's
	// own choice, on a row of its own (brain_shadow.go). Started where triage
	// is settled, joined at whichever exit the route takes.
	var openCh <-chan shadowReply
	classBy := byHeuristic

	// ---- Triage gate (usage analysis I1/I3, 2026-08-01) -------------------
	// The LLM director costs a median 17.5s per plan and shares claude's quota;
	// 78.5% of tokens were landing on claude, mostly for prose work that never
	// needed it. Tier 0 (deterministic triage) routes confident trivial/medium
	// tasks instantly on a domain ladder that NEVER contains claude; tier 1 (a
	// free-leg classify, ~5s, never the director) refines low-confidence calls;
	// tier 2 (the director) is reserved for high complexity - plus everything
	// that already has its own semantics: /prefer, named legs, vision, forced.
	if triageEnabled() && req.Forced == "" && req.Prefer == "" &&
		len(captaincode.NamedAssignees(req.Task)) == 0 && !captaincode.TaskNeedsVision(req.Task) {
		tr := captaincode.TriageTask(req.Task)
		// The heuristic's own confidence, before any tier-1 answer replaces
		// it: what decides whether this turn was in the band at all, and so
		// whether it is a turn worth asking a shadow backend about.
		heuristicConf := tr.Confidence
		if !tr.NeedsDirector() {
			switch {
			case tr.Confidence < triageConfidence():
				// Tier 1 proper: jev when configured, else (or on a miss) the
				// free-leg classify. Tier 1 may only ever ADD signal: on an
				// error the heuristic stands. jev's shadow answers stay on the
				// record whether its triage answer was taken or not.
				r, by, sh, err := b.classifyTier1(req.Task, tr.Domain, b.chargeRoute(req.Task))
				shadow = sh
				if err == nil {
					tr, classBy = refineTriage(tr, r), by
				}
			case b.jevDecidesTriage(req.Task) && tr.Confidence < jevConsultBelow():
				// The band: sure enough to keep the ~5s free-leg classify out,
				// not so sure a ~300ms calibrated answer is not worth asking
				// for. A miss keeps the heuristic; nothing else is called.
				r, sh, err := b.classifyJev(req.Task, tr.Domain, b.chargeRoute(req.Task))
				shadow = sh
				if err == nil {
					tr, classBy = refineTriage(tr, r), captaincode.DecidedByJev
				} else {
					fmt.Printf("captain brain: %v - heuristic stands\n", err)
				}
			}
			if heuristicConf < jevConsultBelow() {
				// The band, read off the heuristic rather than off whatever
				// replaced it: a turn captain was going to think about anyway
				// is a turn worth asking a shadow backend about.
				openCh = b.openBeside(req.Task, tr.Domain, nil)
			}
		}
		if !tr.NeedsDirector() {
			rows := b.valueLadder(tr.Class, tr.Domain, false)
			ladder := b.ladderFrom(tr.Class, tr.Domain, false, rows)
			// /oss, /deterministic: only the pool's legs; none open → the
			// director decides below with the pool menu (and its fallback note).
			ladder = captaincode.FilterPool(pool, ladder)
			if len(ladder) > 0 {
				leg := ladder[0]
				totalMs := time.Since(t0).Milliseconds()
				rationale := fmt.Sprintf("triage fast-path: %s/%s (conf %.2f) - director reserved for high complexity", tr.Class, tr.Domain, tr.Confidence)
				if !pool.Empty() {
					rationale += " · pool " + pool.String() + ": " + legListShort(ladder, 4)
				}
				if valueRoutingEnabled() {
					rationale += " · value-ranked " + legListShort(ladder, 4)
				}
				if len(ladder) > 1 && b.explore(tr.Class) {
					leg = ladder[1]
					b.markExplored(req.Task)
					rationale += fmt.Sprintf(" · EXPLORE: trying runner-up %s to earn it a score", leg)
				}
				dec := b.valueDecision(tr, leg, rows, totalMs, rationale)
				stampShadow(&dec, shadow, classBy)
				b.recordOpenShadow(req.Task, dec, openCh, classBy)
				b.recordDecision(req.Task, dec)
				b.last = &lastRoute{Task: truncate(req.Task, 72), Leg: string(leg), Model: captaincode.ModelID(leg), Rationale: rationale, At: time.Now().Format("15:04:05")}
				fmt.Printf("captain brain: routed %q → class=%s leg=%s in %dms [triage %s]\n", b.last.Task, tr.Class, leg, totalMs, tr.Why)
				b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: string(leg), Model: captaincode.ModelID(leg), Text: rationale, Ms: totalMs})
				return routeResp{Class: string(tr.Class), Leg: string(leg), Provider: "captain",
					Model: string(leg), Brief: req.Task, Rationale: rationale}, nil
			}
			// nothing on the fast ladder is open → the director path below still
			// has the full menu (including cooled legs one rung up)
		}
	}
	// -----------------------------------------------------------------------

	if req.Forced == "" {
		order := captaincode.Pick(captaincode.StartRung(classHint, req.Prefer), b.ledger.Cooldowns, time.Now())
		managerOrder := captaincode.Pick(len(captaincode.Rungs)-1, b.ledger.Cooldowns, time.Now())
		order = b.filterAllowed(order) // honor CAPTAIN_LEGS
		managerOrder = b.filterAllowed(managerOrder)
		// A task that references an image must never land on a blind leg
		// (text-only models can only answer "I can't view the image"). Constrain
		// both the ladder and the director's menu to vision-capable legs; keep
		// the unfiltered ladder only if NO vision leg is currently open.
		needVision := captaincode.TaskNeedsVision(req.Task)
		if needVision {
			if vo := captaincode.FilterVision(order); len(vo) > 0 {
				order = vo
			}
			if vm := captaincode.FilterVision(managerOrder); len(vm) > 0 {
				managerOrder = vm
			}
		}
		// /quality: the user asked for the best - the menu contains only the
		// top legs by blended quality, INCLUDING the director's own model as a
		// worker ("never assign itself" yields to an explicit user request;
		// claude executes via claude -p exactly like a forced leg would).
		// (route already holds mu: read the ledger directly, NO re-lock.)
		// The user named the workers: put every named leg in the director's menu,
		// INCLUDING the director's own leg. Without this the menu simply does not
		// contain claude when claude directs (Rungs excludes the director), so the
		// director cannot obey even when told to - it substituted cursor for the
		// "claude seat" (live 2026-07-30). Cooldowns do not filter a named leg:
		// an explicit request outranks a bench, and the wrapper still reroutes if
		// the provider is genuinely down.
		order, managerOrder = b.widenForNamed(req.Task, order), b.widenForNamed(req.Task, managerOrder)
		if req.Prefer == "quality" {
			st := b.ledger.Stats()
			cand := order
			dir := captaincode.Director
			cooling := time.Now().Before(b.ledger.Cooldowns[dir])
			allowed := len(b.allowed) == 0 || b.allowed[dir]
			if captaincode.KnownLeg(dir) && allowed && !cooling {
				cand = append([]captaincode.Leg{dir}, order...)
			}
			if tq := captaincode.TopQuality(cand, st, 2); len(tq) > 0 {
				order = tq
			}
			if tq := captaincode.TopQuality(cand, st, 2); len(tq) > 0 {
				managerOrder = tq
			}
		}
		if !pool.Empty() {
			if po := captaincode.FilterPool(pool, order); len(po) > 0 {
				order = po
			} else {
				b.poolFallbackNote(req.ws.Dir, pool)
			}
			if pm := captaincode.FilterPool(pool, managerOrder); len(pm) > 0 {
				managerOrder = pm
			}
		}
		if len(order) == 0 {
			return routeResp{}, &routeFail{429, "no eligible legs (all cooling down or excluded by CAPTAIN_LEGS)"}
		}
		decMenu = order
		if len(order) == 1 {
			// Only one runnable model → skip the director's LLM call.
			// (that call is the main per-prompt latency).
			leg = order[0]
			rationale = "only runnable option"
			decPath = captaincode.PathLadder
		} else if os.Getenv("CAPTAIN_FAST_ROUTE") == "1" {
			// Fast route: skip the per-prompt director LLM call entirely and use the
			// deterministic ladder (instant). Trades the director's live routing
			// intelligence for a prompt that renders with ~0ms routing overhead.
			leg = order[0]
			rationale = "fast route (ladder, director skipped)"
			decPath = captaincode.PathLadder
		} else {
			decPath = captaincode.PathDirector
			td := time.Now()
			// Pass the USER's preference (quality|speed|save) only - never the
			// ladder's first leg: a leg name in "Preference hint" anchors the
			// director ("matches the preference hint" → grok on every medium
			// task, live 2026-07-19). The menu order already encodes cost.
			// Empty class → director owns complexity; classHint only on failure.
			// Fan-out is allowed (unless CAPTAIN_TEAM=0): a multi-worker plan
			// routes to the captain/team pseudo-model, which executes the whole
			// tree brain-side - "share tasks with your team of agents" is a
			// real capability now, not a sentence a lone worker stalls on.
			// Not under a stated preference: /quality, /speed and /save ask
			// for ONE worker of a kind (the best, the fastest, the cheapest);
			// a team is asked for as /team (live 2026-09-16: a /quality turn
			// ran as a team of two). Named legs are the user's, and stay.
			fanOut := teamEnabled() && (req.Prefer == "" || len(captaincode.NamedAssignees(req.Task)) > 0)
			b.planHints = b.valueHints(classHint, captaincode.TriageTask(req.Task).Domain, managerOrder)
			// The decision leg answers the same questions while the director
			// plans (brain_shadow.go): the comparison costs no latency.
			shadowCh := b.shadowBeside(req.Task, managerOrder, shadow)
			// And the sidecar, over the same menu. These are the rows that
			// say whether it could rank legs: the director is about to answer
			// the same question in prose, over options it was really given.
			if openCh == nil {
				openCh = b.openBeside(req.Task, captaincode.TriageTask(req.Task).Domain, managerOrder)
			}
			p, err := b.plan(req.ws, req.Task, "", req.Prefer, managerOrder, b.ledger.Stats(), b.ledger.TeamStats(), fanOut)
			b.planHints = nil
			directorMs = time.Since(td).Milliseconds()
			if shadowCh != nil {
				shadow = b.shadowJoin(shadowCh, b.chargeRoute(req.Task))
			}
			rr := captaincode.ResolveRoute(req.Task, classHint, order, p, err)
			class = rr.Class
			if rr.Managed {
				classBy = captaincode.PathDirector
			}
			if rr.Managed && rr.FanOut && len(rr.Plan.Workers) > 1 && teamEnabled() {
				b.storeTeamPlan(req.Task, rr.Plan)
				totalMs := time.Since(t0).Milliseconds()
				rationale = rr.Plan.Rationale
				// A team is a decision too: its shape and its legs go on the
				// record, so `captain why` and the shadow can read them.
				dec := b.menuDecision(class, captaincode.TriageTask(req.Task).Domain, decPath, "", managerOrder, totalMs, rationale)
				dec.Shape, dec.Workers = captaincode.ShapeTeam, planLegs(rr.Plan)
				stampShadow(&dec, shadow, classBy)
				b.recordOpenShadow(req.Task, dec, openCh, classBy)
				b.recordDecision(req.Task, dec)
				b.last = &lastRoute{Task: truncate(req.Task, 72), Leg: "team", Model: "team",
					Rationale: rationale, At: time.Now().Format("15:04:05")}
				fmt.Printf("captain brain: routed %q → class=%s leg=team (%d workers) in %dms [director %dms] - %s\n",
					b.last.Task, class, len(rr.Plan.Workers), totalMs, directorMs, rationale)
				b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: "team", Model: "team", Text: rationale, Ms: totalMs})
				return routeResp{Class: string(class), Leg: "team", Provider: "captain",
					Model: "team", Brief: rr.Brief, Rationale: rationale}, nil
			}
			if len(rr.Order) > 0 {
				leg = rr.Order[0]
			} else {
				leg = order[0]
			}
			brief = rr.Brief
			// API returns the director's raw rationale when managed; fallback reason otherwise.
			if rr.Managed && rr.Plan.Rationale != "" {
				rationale = rr.Plan.Rationale
			} else {
				rationale = rr.Reason
			}
		}
		// Hard backstop: even if the director ignored the vision-only menu and
		// picked a blind leg, override to the best open vision leg.
		if needVision && !captaincode.LegSupportsVision(leg) && len(captaincode.FilterVision(order)) > 0 {
			leg = captaincode.FilterVision(order)[0]
			rationale = rationale + " → overridden to " + string(leg) + ": task references an image, needs a vision-capable model"
		}
	}
	if req.Forced == "frontier" {
		b.last = &lastRoute{Task: truncate(req.Task, 72), Leg: "frontier", Model: "frontier", Rationale: "forced frontier", At: time.Now().Format("15:04:05")}
		fmt.Printf("captain brain: routed %q -> forced frontier\n", b.last.Task)
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: "frontier", Model: "frontier", Text: "forced frontier: best model, best version, max effort"})
		return routeResp{Class: string(class), Leg: "frontier", Provider: "captain", Model: "frontier", Brief: req.Task, Rationale: "forced frontier"}, nil
	}
	if req.Forced == "team" {
		// Explicit ensemble request: the wrapper plans (fan-out) and executes.
		b.last = &lastRoute{Task: truncate(req.Task, 72), Leg: "team", Model: "team", Rationale: "forced team", At: time.Now().Format("15:04:05")}
		fmt.Printf("captain brain: routed %q → forced team\n", b.last.Task)
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: "team", Model: "team", Text: "forced team"})
		return routeResp{Class: string(class), Leg: "team", Provider: "captain", Model: "team", Brief: req.Task, Rationale: "forced team"}, nil
	}
	if !captaincode.KnownLeg(leg) {
		return routeResp{}, &routeFail{400, "unknown leg " + string(leg)}
	}
	// EVERY leg runs through captain's own wrapper, exposed on this brain's
	// OpenAI-compatible endpoint as the "captain/<leg>" model - never the fork's
	// native opencode registry. claude→claude -p, cursor→cursor-agent, the rest
	// via captain's opencode dispatch. So all models are reached in wrapped form.
	provider, model, ok := "captain", string(leg), true
	b.last = &lastRoute{
		Task: truncate(req.Task, 72), Leg: string(leg), Model: model,
		Rationale: rationale, At: time.Now().Format("15:04:05"),
	}
	// Visible confirmation + timing: total route time and how much was the
	// director LLM call (the hot-path cost that blocks the prompt from rendering).
	totalMs := time.Since(t0).Milliseconds()
	// The director path ranks nothing itself - it hands a menu to a model. The
	// record therefore carries the menu it was given, scored, so `captain why`
	// can show what the director chose AGAINST, plus the legs that never
	// reached the menu and why.
	dec := b.menuDecision(class, captaincode.TriageTask(req.Task).Domain, decPath, leg, decMenu, totalMs, rationale)
	stampShadow(&dec, shadow, classBy)
	b.recordOpenShadow(req.Task, dec, openCh, classBy)
	b.recordDecision(req.Task, dec)
	fmt.Printf("captain brain: routed %q → class=%s leg=%s (%s) in %dms [director %dms] - %s\n",
		b.last.Task, class, leg, model, totalMs, directorMs, rationale)
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: string(leg), Model: model, Text: rationale, Ms: totalMs})
	return routeResp{
		Class: string(class), Leg: string(leg), Provider: provider, Model: model,
		ViaClaude: !ok, Brief: brief, Rationale: rationale,
	}, nil
}

type assessReq struct {
	Task       string  `json:"task"`
	Output     string  `json:"output"`
	Leg        string  `json:"leg"`
	Objective  string  `json:"objective,omitempty"`
	Tokens     int     `json:"tokens,omitempty"`
	CostUSD    float64 `json:"costUSD,omitempty"`
	DurationMs int64   `json:"durationMs,omitempty"`
}

type assessResp struct {
	Quality float64 `json:"quality"`
	Verdict string  `json:"verdict"`
	Notes   string  `json:"notes"`
}

// assess: director scores the worker output and the brain records the event so
// the scorecards learn. Recording happens even if scoring fails (outcome ok).
func (b *brain) assess(w http.ResponseWriter, r *http.Request) {
	var req assessReq
	if !decode(w, r, &req) {
		return
	}
	if req.Task == "" || req.Leg == "" {
		writeErr(w, 400, "task and leg required")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	obj := req.Objective
	if obj == "" {
		obj = "none"
	}
	ev := captaincode.Event{
		Task: truncate(req.Task, 120), Class: captaincode.Classify(req.Task),
		Leg: captaincode.Leg(req.Leg), Reason: "frontend", Outcome: "ok",
		Tokens: req.Tokens, CostUSD: req.CostUSD, Duration: req.DurationMs,
	}
	var out assessResp
	if a, err := b.mgr.Assess(req.Task, req.Output, obj); err == nil {
		ev.Quality, ev.Verdict = a.Quality, a.Verdict
		out = assessResp{Quality: a.Quality, Verdict: a.Verdict, Notes: a.Notes}
	}
	b.ledger.Record(ev)
	if err := b.ledger.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "captain brain: save ledger: %v\n", err)
	}
	writeJSON(w, 200, out)
}

// stats: live per-leg scorecards (what the director routes on).
func (b *brain) stats(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	last := b.last
	if dir, ok := workspaceFilter(r); ok {
		last = b.lastBy[dir] // the sidebar shows its own project's last route, not another TUI's
	}
	writeJSON(w, 200, map[string]any{"director": string(captaincode.Director), "legs": b.ledger.Stats(),
		"last": last, "skills": b.ledger.SkillStats()})
}

// lastRoute reads the most recent route under the lock (the value
// rememberLast compares against, so a failed route re-tags nothing).
func (b *brain) lastRoute() *lastRoute {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

// rememberLast files the route decideRoute just made under its workspace.
func (b *brain) rememberLast(dir string, prev *lastRoute) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last == nil || b.last == prev {
		return
	}
	b.last.Dir = dir
	if b.lastBy == nil {
		b.lastBy = map[string]*lastRoute{}
	}
	b.lastBy[dir] = b.last
}

// ── small http helpers ──

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST only")
		return false
	}
	// 16MB: the fork replays the ENTIRE conversation every turn and the
	// windowing/compaction happens brain-side AFTER parsing - a long session
	// (hundreds of turns with pasted content) blew the old 1MB cap and every
	// request 400'd "request body too large" (live 2026-08-27).
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(v); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// widenForNamed puts every leg the user NAMED into a director menu, including
// the director's own leg (Rungs excludes it, so without this the director cannot
// obey "have claude review this" when claude directs). Cooldowns do not filter a
// named leg - an explicit request outranks a bench, and the wrapper still
// reroutes a genuinely dead provider. CAPTAIN_LEGS stays a hard boundary.
func (b *brain) widenForNamed(task string, list []captaincode.Leg) []captaincode.Leg {
	for _, l := range captaincode.NamedAssignees(task) {
		if len(b.allowed) > 0 && !b.allowed[l] {
			continue
		}
		if !captaincode.KnownLeg(l) || legInList(l, list) {
			continue
		}
		list = append([]captaincode.Leg{l}, list...)
	}
	return list
}

// legInList reports whether l is already in list (the package-private helper in
// pkg/captaincode is not exported).
func legInList(l captaincode.Leg, list []captaincode.Leg) bool {
	for _, x := range list {
		if x == l {
			return true
		}
	}
	return false
}

// triageEnabled: CAPTAIN_TRIAGE=0 restores the always-director behaviour.
func triageEnabled() bool { return os.Getenv("CAPTAIN_TRIAGE") != "0" }

// triageConfidence is the tier-0 confidence below which tier 1 (free-leg
// classify) is consulted. CAPTAIN_TRIAGE_CONF overrides; default 0.6.
func triageConfidence() float64 {
	if v := os.Getenv("CAPTAIN_TRIAGE_CONF"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			return f
		}
	}
	return 0.6
}

// jevTriageEnabled: CAPTAIN_TRIAGE_JEV=0 keeps the decision leg out of triage
// even when its key is set (`captain jev` still works).
func jevTriageEnabled() bool { return os.Getenv("CAPTAIN_TRIAGE_JEV") != "0" }

// jevConfidence is the calibrated confidence a jev answer must reach to be
// taken; below it the free-leg classify decides as before.
// CAPTAIN_TRIAGE_JEV_CONF overrides; default the tier-0 bar (triageConfidence).
func jevConfidence() float64 {
	if v := os.Getenv("CAPTAIN_TRIAGE_JEV_CONF"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			return f
		}
	}
	return triageConfidence()
}

// jevConsultBelow is the tier-0 confidence below which a configured jev is
// consulted on its own. The free-leg classify costs ~5s, so its bar
// (triageConfidence, 0.6) is low; jev answers in ~300ms for ~$0.00002, so it
// is worth asking whenever the heuristic is not certain. Between the two bars
// a jev miss keeps the heuristic and nothing else is called.
// CAPTAIN_TRIAGE_JEV_BELOW overrides; default 0.9; 0 closes the band.
func jevConsultBelow() float64 {
	if v := os.Getenv("CAPTAIN_TRIAGE_JEV_BELOW"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			return f
		}
	}
	return 0.9
}

// jevTimeout bounds one classification: the vendor answers in 70-500ms, and a
// stall must not hold the route when the free-leg classify can decide instead.
const jevTimeout = 8 * time.Second

// errJevUnsure is a jev answer below its bar: charged, logged, not taken.
var errJevUnsure = errors.New("jev unsure")

// classifyJev asks the decision leg the triage questions - and, in the
// shadow, the routing ones (brain_shadow.go) - and returns its triage answer
// when the weaker of class and domain clears jevConfidence; errJevUnsure
// (with its rationale) below it, the call's error on a failure. The shadow
// comes back in every case, so an unsure or failed call is on the record.
// Charged either way. Caller holds b.mu (the charge hook expects it).
func (b *brain) classifyJev(task string, d captaincode.Domain, onCall captaincode.CallHook) (captaincode.TriageResult, *captaincode.Shadow, error) {
	// Which backend may decide this one, and at what bar. Normally the
	// primary at its tuned bar; a promoted sidecar at the bar IT earned, on
	// the states it can hold whole. The bar travels with the backend because
	// it was never a property of the question.
	c, bar, why := b.triageDecider(task)
	if c == nil {
		return captaincode.TriageResult{}, nil, fmt.Errorf("%w (no backend may decide triage)", errJevUnsure)
	}
	if bar == 0 {
		bar = jevConfidence()
	}
	ctx, cancel := context.WithTimeout(context.Background(), jevAskTimeout(c))
	tr, sh, res, err := captaincode.TriageWithJev(ctx, c, task, b.jevOptions(d, nil))
	cancel()
	if onCall != nil && !b.jevFree(c) {
		onCall(captaincode.LegJev, "classify", res, err)
	}
	if err != nil {
		return captaincode.TriageResult{}, sh, fmt.Errorf("jev classify failed on %s (%w)", c.Backend(), err)
	}
	if tr.Confidence < bar {
		return tr, sh, fmt.Errorf("%w (%s, bar %.2f%s)", errJevUnsure, tr.Why, bar, whySuffix(why))
	}
	return tr, sh, nil
}

// whySuffix appends a decider's rationale to a message when it had one.
func whySuffix(why string) string {
	if why == "" {
		return ""
	}
	return " - " + why
}

// jevAskTimeout is how long one call may take. The primary crosses the
// internet and gets jevTimeout; a sidecar on loopback answers in about 13ms
// and carries its own, because two seconds there is not a budget, it is the
// point past which the thing is wedged.
func jevAskTimeout(c *captaincode.SystemOneClient) time.Duration {
	if c != nil && c.HTTP != nil && c.HTTP.Timeout > 0 {
		return c.HTTP.Timeout
	}
	return jevTimeout
}

// jevFree says a call cost nothing, so no charge row is written for it. The
// sidecar is a local process answering from weights already on the disk;
// pricing it at the registry's per-token rate would put money on the ledger
// that nobody was billed, which is the one thing the ledger is for.
func (b *brain) jevFree(c *captaincode.SystemOneClient) bool {
	return c != nil && c == b.jevBackends.Open
}

// refineTriage folds a tier-1 answer into the heuristic result: class and
// domain are replaced, the confidence too when the answer is calibrated (the
// free-leg classify carries none), and the rationale keeps both steps.
func refineTriage(tr, r captaincode.TriageResult) captaincode.TriageResult {
	tr.Class, tr.Domain = r.Class, r.Domain
	if r.Confidence > 0 {
		tr.Confidence = r.Confidence
	}
	tr.Why += " → " + r.Why
	return tr
}

// classifyTier1 is triage tier 1: the decision leg when it is configured and
// sure enough, else the free-leg classify. jev answers in a few hundred
// milliseconds with a calibrated confidence; below the bar its answer is
// dropped rather than trusted, and the ~5s free-leg call decides as before.
// Every jev call is charged, taken or not. Caller holds b.mu (the charge hook
// expects it).
// It also says who answered (jev, or the free-leg classify) and returns
// jev's shadow when it was asked, whichever answer was taken.
func (b *brain) classifyTier1(task string, d captaincode.Domain, onCall captaincode.CallHook) (captaincode.TriageResult, string, *captaincode.Shadow, error) {
	var sh *captaincode.Shadow
	if b.jev != nil {
		tr, jsh, err := b.classifyJev(task, d, onCall)
		sh = jsh
		if err == nil {
			return tr, captaincode.DecidedByJev, sh, nil
		}
		fmt.Printf("captain brain: %v - free-leg classify instead\n", err)
	}
	c, dom, err := b.classifyLLM(task, onCall)
	if err != nil {
		return captaincode.TriageResult{}, "", sh, err
	}
	return captaincode.TriageResult{Class: c, Domain: dom, Why: "LLM-refined"}, byClassify, sh, nil
}

// classifyLLM is triage tier 1's fallback: the FREE leg, never the director.
// CAPTAIN_TRIAGE_LLM=0 disables it (heuristics stand alone).
func (b *brain) classifyLLM(task string, onCall captaincode.CallHook) (captaincode.Class, captaincode.Domain, error) {
	if b.classifyLLMFn != nil {
		return b.classifyLLMFn(task)
	}
	if os.Getenv("CAPTAIN_TRIAGE_LLM") == "0" {
		return "", "", fmt.Errorf("tier-1 classify disabled")
	}
	return captaincode.ClassifyWithLLM(task, opencodePort, onCall)
}

// openOnly filters a preference list to legs not cooling down right now.
// Caller holds b.mu.
func (b *brain) openOnly(legs []captaincode.Leg) []captaincode.Leg {
	now := time.Now()
	out := legs[:0:0]
	for _, l := range legs {
		if now.Before(b.ledger.Cooldowns[l]) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// legListShort renders the first n legs of a ladder for a rationale.
func legListShort(legs []captaincode.Leg, n int) string {
	if len(legs) > n {
		legs = legs[:n]
	}
	parts := make([]string, 0, len(legs))
	for _, l := range legs {
		parts = append(parts, string(l))
	}
	return strings.Join(parts, " > ")
}
