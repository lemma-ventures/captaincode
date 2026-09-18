package captaincode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrRateLimited signals the leg hit its subscription window; the policy
// layer cools the leg down and re-dispatches one lane over.
var ErrRateLimited = errors.New("rate limited")

// Workspace is the project one request works in: the directory captain's
// worker wrappers (claude -p, cursor-agent, codex, opencode sessions) run in,
// so they read the USER's repo. One brain serves every captain-code TUI on
// the machine at once, so the workspace travels with the request (the TUI
// sends it as the X-Captain-Cwd header) instead of living in the brain's
// environment - a brain pinned to one folder made every other TUI's workers
// explore the wrong repo (live 2026-07-19 and again 2026-09-12).
type Workspace struct {
	Dir    string
	Effort Effort   // how hard the worker thinks on this request ("" = the transport's default); see effort.go
	Brains []string // other repositories the task names, whose brains are read alongside Dir's (reporefs.go)
	Steer  *Steer   // the turn's /btw handle: notes sent while a worker runs reach it here (steer.go); nil = none
	Pool   Pool     // the turn's /oss and /deterministic constraints (pool.go)
}

// WithEffort is this workspace with the request's effort set.
func (ws Workspace) WithEffort(e Effort) Workspace { ws.Effort = e; return ws }

// At is this workspace moved to another folder (a team worker's worktree),
// keeping everything that belongs to the request - effort, brains, steering.
func (ws Workspace) At(dir string) Workspace { ws.Dir = dir; return ws }

// DefaultWorkspace is the launcher's CAPTAIN_CWD (empty = the brain's own
// cwd): the CLI paths and the fallback for a caller that sent no workspace.
func DefaultWorkspace() Workspace { return Workspace{Dir: os.Getenv("CAPTAIN_CWD")} }

// WorkerTimeout exposes the per-worker hard cap (reroute chains bound on it).
func WorkerTimeout() time.Duration { return workerTimeout() }

// workerTimeout bounds every worker dispatch so a leg can NEVER hang the UI
// indefinitely. This is the hard backstop for the whole class of "stuck" bugs -
// most importantly a headless opencode-serve worker that calls the interactive
// `question` tool (which blocks forever waiting for a TUI answer nobody can give).
// Tune with CAPTAIN_WORKER_TIMEOUT (e.g. "25m"); default 15m - a research-paper
// audit with repo reading routinely runs past 8m, and the stall watchdog (4m of
// NO activity) is what catches genuinely wedged runs, so the hard cap only needs
// to bound work that is actually progressing.
func workerTimeout() time.Duration {
	if v := os.Getenv("CAPTAIN_WORKER_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 15 * time.Minute
}

// ErrWorkerStalled signals the worker's session showed NO event-bus activity
// for StallTimeout while its /message call was still pending - the turn is
// wedged server-side (observed live 2026-07-18: a tool part stuck "running"
// forever after the model emitted a mangled tool call; zero part updates for
// 11+ minutes). Distinct from the hard Timeout so callers can retry: a stalled
// turn did nothing recently, so re-running it is safe and is exactly the
// manual recovery that fixed the incident.
var ErrWorkerStalled = errors.New("worker stalled")

// ErrContextOverflow signals the task/conversation exceeds the worker model's
// context window (opencode: ContextOverflowError, "too large to compact").
// Non-retryable - the same input will overflow again; the caller must shrink
// the prompt, start a new session, or pick a bigger-context leg. Observed live
// 2026-07-18: a 200k-token TUI conversation replayed through the wrapper
// overflowed codex on every attempt and the fork's SDK blind-retried a 502.
var ErrContextOverflow = errors.New("context overflow")

// firstEventTimeout is how long a worker may produce NOTHING before it counts
// as stalled. A provider that accepts the message and never streams anything is
// dead on arrival; the full stall window (meant for quiet tool runs) just delays
// the reroute. Tune with CAPTAIN_WORKER_FIRST_EVENT_TIMEOUT; default 90s.
func firstEventTimeout() time.Duration {
	if v := os.Getenv("CAPTAIN_WORKER_FIRST_EVENT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 90 * time.Second
}

// blindTimeout caps a run whose event stream never connected: with no activity
// sensor the only safe assumption is that silence means trouble. Tune with
// CAPTAIN_WORKER_BLIND_TIMEOUT; default 3m.
func blindTimeout() time.Duration {
	if v := os.Getenv("CAPTAIN_WORKER_BLIND_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 3 * time.Minute
}

// idleTimeout is how long a worker that HAS produced output may stay silent
// while no tool is running. A model streams or it is dead; the long stall
// window exists for tool runs (a build, a test suite), not for a dropped
// stream. Tune with CAPTAIN_WORKER_IDLE_TIMEOUT; default 90s.
func idleTimeout() time.Duration {
	if v := os.Getenv("CAPTAIN_WORKER_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 90 * time.Second
}

// workerStallTimeout is how long a worker session may stay silent (no events
// at all on the opencode bus) before the watchdog aborts it. Distinct from
// workerTimeout: that caps TOTAL turn time; this catches a turn that stopped
// making progress. Only meaningful when the event stream is connected. Tune
// with CAPTAIN_WORKER_STALL_TIMEOUT (e.g. "2m"); default 4m - long enough for
// a quiet tool run (a several-minute bash emits nothing between start and
// finish), short enough that a wedge self-heals well before the hard cap.
func workerStallTimeout() time.Duration {
	if v := os.Getenv("CAPTAIN_WORKER_STALL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 4 * time.Minute
}

// ErrProviderDown signals the upstream provider is temporarily unavailable
// (503/502/"service temporarily unavailable" - live 2026-07-19: xAI grok-4.5
// bursts). Transient: the policy layer cools the leg briefly (minutes, not
// the 30m rate-limit window) so routing steers around the outage.
var ErrProviderDown = errors.New("provider temporarily unavailable")

// ErrProviderAuth is a provider rejecting the leg's credentials (401/403,
// "Authorization failed"). It wraps ErrProviderDown so the task reroutes like
// any provider fault - kimi's NIM key was rejected and the turn simply ended
// with the error instead of trying the next leg (live 2026-09-12) - but the
// policy layer benches the leg for longer: a bad key does not heal in ten
// minutes, and harnessFault keeps it off the model's reliability stats.
var ErrProviderAuth = fmt.Errorf("%w: credentials rejected", ErrProviderDown)

// providerAuthError recognizes a credential rejection in a provider error.
func providerAuthError(msg string) bool {
	m := strings.ToLower(msg)
	for _, p := range []string{
		"authorization failed", "unauthorized", "forbidden", "invalid api key",
		"invalid_api_key", "incorrect api key", "authentication", "statuscode\":401",
		"statuscode\":403", "status\":401", "status\":403",
	} {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

// ErrSessionNotFound signals a persisted thread's opencode session no longer
// exists server-side (deleted, or the server lost state) - a dangling
// ThreadRef. The caller must clear it and retry with a fresh session rather
// than treat this like an ordinary failure, or the leg stays permanently
// broken until a manual `/new`.
var ErrSessionNotFound = errors.New("opencode session not found")

type Result struct {
	Text       string
	Tokens     int
	CostUSD    float64 // real $ cost when the leg reports it (claude -p); 0 for subscription legs with no per-call price
	DurationMs int64
	Streamed   bool // output was already live-printed; don't reprint
	// Partial marks output salvaged from a run that hit its time cap. Eight
	// minutes of a paper audit must not evaporate because the ninth was not
	// allowed (live 2026-07-30: claude and codex both hit the cap on a
	// research-paper review and the turn delivered nothing at all).
	Partial bool
	// Nudged marks output that took a second worker call to produce (a
	// narration-only first try, re-asked for the deliverable). Tokens,
	// CostUSD and DurationMs then cover BOTH calls; the brain charges the
	// second as its own "repair" attempt so the turn's bill is the whole
	// turn and not just its last call (ROADMAP M1.2).
	Nudged bool
	// Log is the on-disk worker log the brain streamed this run into (empty
	// when logging is off) - what the agent said and did survives a dead
	// turn, a discarded result or a killed brain.
	Log string
	// Headers carries HTTP response headers the adapter exposed (M2.3). The
	// opencode error JSON embeds upstream x-ratelimit-* headers; extracting
	// them here lets the brain record quota telemetry proactively — a
	// response carrying x-ratelimit-remaining-requests:5 tells the routing
	// gate the leg is about to hit its limit BEFORE the window closes.
	Headers http.Header
}

// ErrEmptyOutput signals the worker finished "successfully" and produced no
// text. That is never a usable answer - the user sees a turn that ran for
// minutes and said nothing (live 2026-07-31: "grok wrapper done in 1m30s (0
// chars)"). Treated as a failure so the caller reroutes and the scorecard
// records it.
// ErrRefused: the worker declined the task. Distinct from a provider fault -
// the leg is healthy, its POLICY does not fit this work - so it reroutes but
// must not be treated as unreliability.
var ErrRefused = errors.New("worker refused the task")

var ErrEmptyOutput = errors.New("worker returned an empty answer")

// ErrWorkerTimeout signals the worker exceeded CAPTAIN_WORKER_TIMEOUT. When the
// Result carries text, that text is real work: callers should prefer delivering
// it (marked partial) over discarding it.
var ErrWorkerTimeout = errors.New("worker timed out")

type Dispatcher interface {
	Run(leg Leg, task string) (Result, error)
}

// legModels maps opencode-transport legs to provider/model pairs. Derived
// from the registry (applyRegistry) then adjusted by env overrides.
var legModels = map[Leg]struct{ Provider, Model string }{}

// LegModelPins returns every API-backed leg with its pinned provider/model -
// the legs that have no local binary to upgrade (`captain upgrade` shows them
// so the inventory is complete: grok and codex are MODEL PINS, not CLIs).
func LegModelPins() []struct{ Leg, Provider, Model string } {
	out := make([]struct{ Leg, Provider, Model string }, 0, len(legModels))
	for _, l := range []Leg{LegFree, LegGrok, LegGrokMax, LegCodex, LegGLM, LegMiniMax, LegQwen, LegDeepSeek, LegGemini, LegKimi} {
		if mm, ok := legModels[l]; ok {
			out = append(out, struct{ Leg, Provider, Model string }{string(l), mm.Provider, mm.Model})
		}
	}
	return out
}

// applyLegModelEnv lets ops pin leg models without recompiling:
// <PREFIX>_PROVIDER / <PREFIX>_MODEL per registry leg (CAPTAIN_FREE_MODEL,
// CAPTAIN_GLM_PROVIDER, …; the zen free catalog rotates!).
func applyLegModelEnv() {
	for leg, mm := range legModels {
		sp := specs[leg]
		prefix := sp.EnvPrefix()
		if p := strings.TrimSpace(os.Getenv(prefix + "_PROVIDER")); p != "" {
			mm.Provider = p
		}
		if m := strings.TrimSpace(os.Getenv(prefix + "_MODEL")); m != "" {
			mm.Model = m
		}
		legModels[leg] = mm
	}
}

// directorModels overrides legModels when a leg acts as DIRECTOR (planner/
// judge) rather than worker. Two reasons a worker model can't judge:
//   - agentic tuning: coding-agent models keep narrating tool calls as prose
//     ("invoke Glob with pattern...") instead of returning plain JSON even
//     with tools disabled (observed live with xAI's grok-build-0.1) -
//     grok-4.6 is xAI's plain reasoning model and complies reliably. It's
//     also the default director on merit: 4th on the AA Intelligence Index,
//     Terminal-Bench within a point of GPT-5.5/Fable, at a fraction of the
//     per-task cost (July 2026; 4.6 replaced 4.5 on 2026-08-12, same price).
//   - capability: gpt-5.3-codex-spark is the latency-optimized variant
//     (~56% SWE-Pro vs standard Codex's ~72%) - too weak to plan/assess, so
//     a codex director runs the standard model instead.
//
// Legs absent here use legModels. The claude leg needs no entry: as director
// it runs Claude Fable via claude -p - the strongest judge available, at the
// price of ~2 Max-quota calls per managed task.
var directorModels = map[Leg]struct{ Provider, Model string }{
	LegGrok: {"xai", "grok-4.6"},
	// gpt-5.5, the full-effort twin of the worker's gpt-5.5-fast: the ChatGPT
	// route dropped gpt-5.3-codex on 2026-09-15 ("Model not found").
	LegCodex: {"openai", "gpt-5.5"},
}

// ModelSpec returns the opencode provider/model a worker leg runs, so a
// frontend (the opencode fork) can execute the leg the brain selected. Claude
// has no opencode mapping (it runs via claude -p) and returns ok=false.
func ModelSpec(leg Leg) (provider, model string, ok bool) {
	mm, found := legModels[leg]
	return mm.Provider, mm.Model, found
}

func modelFor(leg Leg, asDirector bool) (struct{ Provider, Model string }, bool) {
	if asDirector {
		if mm, ok := directorModels[leg]; ok {
			return mm, true
		}
	}
	mm, ok := legModels[leg]
	return mm, ok
}

// OpencodeDispatcher drives a local `opencode serve` (spawning it on demand
// in the current working directory) and claude -p for the Claude leg.
// Title names the worker's session so it shows up as its own tab in
// `opencode attach` (w-<model>-<#>); Claude-leg output is mirrored into a
// titled session via noReply posts so it gets a tab too.
type OpencodeDispatcher struct {
	BaseURL    string
	Dir        string // the workspace the worker session is pinned to ("" = DefaultWorkspace)
	Effort     Effort // the request's effort, sent as the message's variant when the model offers one
	Steer      *Steer // the turn's /btw handle: a note sent mid-run is POSTed to the busy session, which merges it into the running turn (steer.go)
	SessionID  string
	Title      string
	Client     *http.Client
	Spawn      bool          // spawn opencode serve if the port is closed
	Live       bool          // stream worker output to the terminal via the SSE event bus
	OnDelta    func(string)  // if set, text deltas go here (brain SSE) instead of stdout - live token streaming for the opencode legs
	OnStatus   func(string)  // if set, tool activity goes here (brain progress feed) instead of stderr
	NoTools    bool          // disable all tools: plain chat completion (director calls)
	AsDirector bool          // use directorModels instead of legModels for this leg, if an override exists
	Timeout    time.Duration // per-message deadline; on expiry the session is aborted server-side
	Ceiling    time.Duration // when > Timeout: a PROGRESSING run extends past Timeout, dying only here (progress-aware cap)

	// StallTimeout aborts a turn whose session shows no event-bus activity for
	// this long (0 = disabled). Needs the event stream (Live/OnDelta), which is
	// what observes activity; without it only the hard Timeout applies.
	StallTimeout time.Duration
	lastActivity atomic.Int64 // unix nanos of the last event seen for this session

	// Everything the worker streamed, so a run that hits the hard cap can still
	// hand back what it produced instead of nothing (live 2026-07-30).
	amu             sync.Mutex
	acc             strings.Builder
	sawOutput       atomic.Bool  // the model actually started producing (text/reasoning/tool), not just a session bookkeeping event
	lastFingerprint string       // REST cross-check state (watchdog goroutine only)
	activeTools     atomic.Int32 // tool parts currently "running": the legitimate reason a worker goes quiet
}

func NewDispatcher(port int) *OpencodeDispatcher {
	return &OpencodeDispatcher{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		Client:  &http.Client{Timeout: 30 * time.Minute},
		// CAPTAIN_OPENCODE_SPAWN=0: never start a serve (the test suites set
		// it). A test that reached the live port while the real serve was
		// down spawned one with the test's throwaway HOME - no auth, no
		// providers - and the brain adopted it: every opencode leg then died
		// with "create opencode session" / "User not found" (2026-09-18).
		Spawn: os.Getenv("CAPTAIN_OPENCODE_SPAWN") != "0",
	}
}

// ModelID returns the model identifier a leg runs, for worker tab names.
func ModelID(l Leg) string {
	if l == LegFrontier {
		return "claude-fable-frontier"
	}
	if mm, ok := legModels[l]; ok {
		return mm.Model
	}
	if s, ok := specs[l]; ok {
		return s.Model
	}
	return string(l)
}

func (d *OpencodeDispatcher) Run(leg Leg, task string) (Result, error) {
	if d.Dir == "" {
		d.Dir = DefaultWorkspace().Dir
	}
	if leg == LegClaude {
		// Mirror the claude -p exchange into a titled opencode session so the
		// Claude worker gets a tab like everyone else (best-effort).
		if err := d.EnsureServer(); err == nil && d.ensureSession() == nil {
			if err := d.Note("## brief\n\n" + task); err != nil {
				fmt.Fprintf(os.Stderr, "captain: unable to post claude brief: %v\n", err)
			}
		}
		res, err := runClaude(d.Dir, task)
		if err == nil {
			if err := d.Note("## result\n\n" + res.Text); err != nil {
				fmt.Fprintf(os.Stderr, "captain: unable to post claude result: %v\n", err)
			}
		} else {
			if err := d.Note("## error\n\n" + err.Error()); err != nil {
				fmt.Fprintf(os.Stderr, "captain: unable to post claude error: %v\n", err)
			}
		}
		return res, err
	}
	if leg == LegCursor {
		text, err := runCursor(d.Dir, task)
		return Result{Text: text}, err
	}
	mm, ok := modelFor(leg, d.AsDirector)
	if !ok {
		return Result{}, fmt.Errorf("unknown leg %q", leg)
	}
	if err := d.EnsureServer(); err != nil {
		return Result{}, err
	}
	if err := d.ensureSession(); err != nil {
		return Result{}, err
	}
	start := time.Now()
	var streamed bool
	// The event bus is not just an output channel - it is the ONLY activity
	// sensor (stall watchdog), the source of tool-activity statuses, and what
	// fills the partial-output buffer. Starting it only when someone wanted the
	// TEXT meant the workflow and team paths (which pass onDelta=nil so worker
	// text never reaches the answer) ran with NO stall detection at all: a dead
	// xAI session sat 9 minutes untouched, bounded only by the 15m hard cap
	// (live 2026-07-31).
	if d.Live || d.OnDelta != nil || d.OnStatus != nil || d.StallTimeout > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		conn := make(chan bool, 1)
		go d.streamEvents(ctx, conn)
		select {
		case streamed = <-conn:
		case <-time.After(2 * time.Second):
		}
	}
	payload := map[string]any{
		"model": map[string]string{"providerID": mm.Provider, "modelID": mm.Model},
		"parts": []map[string]string{{"type": "text", "text": task}},
	}
	// The request's effort, as the reasoning variant this model offers
	// (glm: low/high/max, grok-4.6: low…xhigh; none for grok-build, kimi).
	if d.Effort != "" {
		if v := d.Effort.Variant(opencodeVariants(d.BaseURL, mm.Provider, mm.Model)); v != "" {
			payload["variant"] = v
		}
	}
	if d.NoTools {
		payload["tools"] = map[string]bool{"*": false}
	} else {
		// Headless worker: the interactive `question` tool blocks the session
		// forever waiting for a TUI answer that never comes (nobody is attached to
		// this opencode serve). Disable it so the worker proceeds on its own
		// judgment instead of hanging. This is the #1 cause of captain "stuck".
		payload["tools"] = map[string]bool{"question": false}
	}
	body, _ := json.Marshal(payload)
	// /btw: a second message POSTed to a busy session is merged into the
	// running turn (the pending POST below returns the merged answer, this
	// one's is a duplicate and dropped). Attached for as long as the run
	// lasts; a note landing in the instant between the turn ending and the
	// detach becomes a stray extra turn nobody reads - the queued TUI copy
	// still runs it, so nothing is lost.
	if d.Steer != nil {
		inject := func(note string) error {
			np := map[string]any{
				"model": payload["model"],
				"tools": payload["tools"],
				"parts": []map[string]string{{"type": "text", "text": SteerLine(note)}},
			}
			if v, ok := payload["variant"]; ok {
				np["variant"] = v
			}
			nb, _ := json.Marshal(np)
			go func() {
				resp, err := d.Client.Post(d.BaseURL+"/session/"+d.SessionID+"/message", "application/json", bytes.NewReader(nb))
				if err == nil {
					io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
					resp.Body.Close()
				}
			}()
			if d.OnStatus != nil {
				d.OnStatus("btw from the user taken: " + truncateStr(strings.TrimSpace(note), 120))
			}
			return nil
		}
		defer d.Steer.Attach(leg, inject)()
	}
	// Without telemetry we cannot tell "working" from "dead", so a blind run gets
	// a much shorter leash than a watched one - the retry-on-fresh-session path
	// recovers most wedges, and 15 minutes of silence helps nobody.
	effTimeout := d.Timeout
	if !streamed && d.StallTimeout > 0 {
		if bt := blindTimeout(); effTimeout == 0 || bt < effTimeout {
			effTimeout = bt
		}
		fmt.Fprintf(os.Stderr, "captain: %s worker has no event stream - running blind with a %s cap (no stall detection)\n", leg, effTimeout)
	}
	ctx := context.Background()
	var capped atomic.Bool
	if effTimeout > 0 {
		var cancel context.CancelFunc
		if streamed {
			// Progress-aware cap (2026-08-24): a worker the event stream shows
			// MOVING is not killed at the base cap - a healthy long team/wf
			// worker must outlive 15m. Quiet past the base cap dies (the
			// stall watchdog usually got there first); a tool in flight is
			// not quiet (toolRunTimeout). Ceiling, when set, is an absolute
			// stop - none by default (2026-09-12: a productive 31-minute run
			// was cut mid-task).
			ctx, cancel = context.WithCancel(ctx)
			defer cancel()
			d.lastActivity.Store(time.Now().UnixNano())
			go func(kctx context.Context, kcancel context.CancelFunc, start time.Time) {
				poll := effTimeout / 4
				if poll > 10*time.Second {
					poll = 10 * time.Second
				}
				if poll < 50*time.Millisecond {
					poll = 50 * time.Millisecond
				}
				t := time.NewTicker(poll)
				defer t.Stop()
				for {
					select {
					case <-kctx.Done():
						return
					case <-t.C:
						el := time.Since(start)
						quiet := time.Since(time.Unix(0, d.lastActivity.Load()))
						window := idleTimeout()
						if d.activeTools.Load() > 0 {
							window = toolRunTimeout()
						}
						if (d.Ceiling > 0 && el >= d.Ceiling) || (el >= effTimeout && quiet >= window) {
							capped.Store(true)
							kcancel()
							return
						}
					}
				}
			}(ctx, cancel, time.Now())
		} else {
			ctx, cancel = context.WithTimeout(ctx, effTimeout)
			defer cancel()
		}
	}
	// Stall watchdog: the hard Timeout above caps total turn time, but the
	// incident class it misses is a turn that WEDGES - the server-side
	// generation stops (a tool part stuck "running", a dead provider stream)
	// and /message just never returns. The event stream observes the session's
	// real activity, so: no events for StallTimeout → abort the generation
	// server-side. The abort is what unblocks everything - opencode finalizes
	// the turn and the pending /message POST returns (MessageAbortedError);
	// cancelling only our request would leave the server-side turn wedged.
	// Cancel is kept as a fallback for a server too dead to answer the abort.
	// Only armed when the event stream connected (it's the activity sensor).
	var stalled atomic.Bool
	var stalledWindow atomic.Int64 // the window that actually fired, for the error text
	if d.StallTimeout > 0 && streamed {
		// A worker that has produced NOTHING is diagnosed faster than one that
		// went quiet mid-work: waiting the full stall window twice cost 8 minutes
		// before a dead xAI session was rerouted, on a "condense this to 200
		// words" task (live 2026-07-30). Once any event arrives, the normal
		// window applies - a several-minute silent tool run is legitimate.
		var stallCancel context.CancelFunc
		ctx, stallCancel = context.WithCancel(ctx)
		defer stallCancel()
		watchCtx := ctx
		d.lastActivity.Store(time.Now().UnixNano())
		go func() {
			poll := d.StallTimeout / 4
			if poll > time.Second {
				poll = time.Second
			}
			// Baseline the REST fingerprint up front: on window expiry only a
			// CHANGE proves progress. (Baselining lazily at first expiry would
			// count the baseline itself as progress and double every window.)
			d.lastFingerprint, _ = d.sessionFingerprint()
			t := time.NewTicker(poll)
			defer t.Stop()
			for {
				select {
				case <-watchCtx.Done():
					return
				case <-t.C:
					// Three windows, by what the worker is actually doing:
					//   never started generating → firstEventTimeout (90s)
					//   quiet with NO tool running → idleTimeout (90s), at most StallTimeout
					//   quiet WITH a tool running → toolRunTimeout (12m): a build
					//     or a test suite is silent for minutes, and opencode
					//     itself bounds a tool at 10m - past that it is wedged.
					//     (`cargo test --release` used to be aborted at 4m, live
					//     2026-09-12.)
					window := d.StallTimeout
					switch {
					case !d.sawOutput.Load():
						window = firstEventTimeout()
					case d.activeTools.Load() <= 0:
						window = idleTimeout()
					}
					if window > d.StallTimeout {
						window = d.StallTimeout
					}
					if d.sawOutput.Load() && d.activeTools.Load() > 0 && toolRunTimeout() > window {
						window = toolRunTimeout()
					}
					if time.Since(time.Unix(0, d.lastActivity.Load())) <= window {
						continue
					}
					// The bus says silence - verify before killing: a dead
					// /event bus made every long run look stalled for a week
					// (2026-08-07). If REST shows the session advancing, the
					// bus is lying: reset the clock and keep waiting.
					// Before the first token, the only REST change is our own
					// user message being saved: that is not the model working.
					// Counting it stretched kimi's 90s first-event window to
					// the full 4m stall, twice (a fresh-session retry), so a
					// simple prompt waited 8 minutes for a dead NIM (2026-09-18).
					if fp, ok := d.sessionFingerprint(); ok && fp != d.lastFingerprint && (d.sawOutput.Load() || d.sessionHasAssistantOutput()) {
						fmt.Fprintf(os.Stderr, "captain: %s session advancing over REST while the event bus is silent - bus degraded, not a stall\n", d.SessionID)
						d.lastFingerprint = fp
						d.lastActivity.Store(time.Now().UnixNano())
						d.sawOutput.Store(true) // REST progress counts as output
						continue
					}
					stalled.Store(true)
					stalledWindow.Store(int64(window))
					d.abort() // server-side finalize → the pending POST returns
					grace := d.StallTimeout
					if grace > 10*time.Second {
						grace = 10 * time.Second
					}
					select {
					case <-watchCtx.Done(): // POST returned; Run is unwinding
					case <-time.After(grace):
						stallCancel() // server didn't even honor the abort - force our side open
					}
					return
				}
			}
		}()
	}
	// /interrupt with no channel taken (or the grace over): end the run and
	// keep what streamed (steer.go). The server-side generation is aborted too.
	ctx, stopped, detachStop := interruptible(ctx, d.Steer, leg)
	defer detachStop()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+"/session/"+d.SessionID+"/message", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Client.Do(req)
	if err != nil {
		if stopped.Load() {
			d.abort()
			return Result{Text: d.streamed(), Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: streamed},
				fmt.Errorf("%s/%s stopped after %s: %w", mm.Provider, mm.Model, time.Since(start).Round(time.Second), ErrInterrupted)
		}
		if stalled.Load() {
			window := time.Duration(stalledWindow.Load())
			what := "no session activity"
			if window != d.StallTimeout {
				what = "the model never started generating" // first-event window
			}
			return Result{}, fmt.Errorf("%s/%s: %w - %s for %s", mm.Provider, mm.Model, ErrWorkerStalled, what, window)
		}
		if errors.Is(err, context.DeadlineExceeded) || capped.Load() {
			d.abort() // stop the server-side generation too
			if !streamed && d.StallTimeout > 0 {
				// Blind cap: treat as a stall so the caller retries on a fresh
				// session and can reroute, rather than surfacing a dead end.
				return Result{}, fmt.Errorf("%s/%s: %w - no telemetry and nothing returned within %s",
					mm.Provider, mm.Model, ErrWorkerStalled, effTimeout)
			}
			if partial := d.streamed(); partial != "" {
				return Result{Text: partial, Partial: true},
					fmt.Errorf("%s/%s did not finish within %s: %w", mm.Provider, mm.Model, d.Timeout, ErrWorkerTimeout)
			}
			return Result{}, fmt.Errorf("%s/%s did not finish within %s: %w", mm.Provider, mm.Model, d.Timeout, ErrWorkerTimeout)
		}
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return Result{}, ErrRateLimited
	}
	if resp.StatusCode == http.StatusNotFound {
		// A persisted ThreadRef pointing at a session the server no longer
		// has (deleted, or state lost) - distinct from an ordinary error so
		// the caller can self-heal instead of leaving the leg permanently
		// broken. Body shape differs from the normal message response
		// (`{"name":"NotFoundError",...}`), so this must be checked before
		// decoding into the expected shape below.
		return Result{}, ErrSessionNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		if stalled.Load() {
			// The watchdog's own abort finalized the turn as an error
			// status: report the stall, not the abort artifact.
			window := time.Duration(stalledWindow.Load())
			what := "no session activity"
			if window != d.StallTimeout {
				what = "the model never started generating"
			}
			return Result{}, fmt.Errorf("%s/%s: %w - %s for %s", mm.Provider, mm.Model, ErrWorkerStalled, what, window)
		}
		return Result{}, fmt.Errorf("opencode HTTP %d: %s", resp.StatusCode, string(body))
	}
	var msg struct {
		Info struct {
			Error  json.RawMessage `json:"error"`
			Tokens struct {
				Total int `json:"total"`
			} `json:"tokens"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return Result{}, fmt.Errorf("decode opencode response: %w", err)
	}
	if len(msg.Info.Error) > 0 && string(msg.Info.Error) != "null" {
		e := opencodeErrorText(msg.Info.Error)
		hdrs := extractResponseHeaders(msg.Info.Error)
		// Provider-down BEFORE rate-limit: xAI's capacity errors ("currently at
		// capacity due to high demand") ship with HTTP 429, and matching "429"
		// first would bench the leg for the 30m quota window instead of the
		// short outage cooldown.
		if strings.Contains(e, "temporarily unavailable") || strings.Contains(e, "service unavailable") || strings.Contains(e, " 503") || strings.Contains(e, " 502") || strings.Contains(e, "bad gateway") || strings.Contains(e, "overloaded") || strings.Contains(e, "at capacity") || strings.Contains(e, "high demand") {
			return Result{Headers: hdrs}, fmt.Errorf("%s/%s: %w: %s", mm.Provider, mm.Model, ErrProviderDown, e)
		}
		if strings.Contains(e, "rate") || strings.Contains(e, "429") || strings.Contains(e, "quota") || strings.Contains(e, "usage limit") {
			return Result{Headers: hdrs}, ErrRateLimited
		}
		if providerAuthError(string(msg.Info.Error)) {
			return Result{Headers: hdrs}, fmt.Errorf("%s/%s: %w (check the provider key): %s", mm.Provider, mm.Model, ErrProviderAuth, e)
		}
		if strings.Contains(e, "contextoverflow") || strings.Contains(e, "too large to compact") || strings.Contains(e, "context exceeds") {
			return Result{Headers: hdrs}, fmt.Errorf("%s/%s: %w: %s", mm.Provider, mm.Model, ErrContextOverflow, string(msg.Info.Error))
		}
		if stalled.Load() {
			// The error is the watchdog's own abort finalizing the wedged turn
			// (MessageAbortedError) - report the stall, not the abort artifact.
			window := time.Duration(stalledWindow.Load())
			what := "no session activity"
			if window != d.StallTimeout {
				what = "the model never started generating"
			}
			return Result{Headers: hdrs}, fmt.Errorf("%s/%s: %w - %s for %s", mm.Provider, mm.Model, ErrWorkerStalled, what, window)
		}
		return Result{Headers: hdrs}, fmt.Errorf("opencode error: %s", string(msg.Info.Error))
	}
	var out strings.Builder
	for _, p := range msg.Parts {
		if p.Type == "text" {
			out.WriteString(p.Text)
		}
	}
	return Result{Text: out.String(), Tokens: msg.Info.Tokens.Total, DurationMs: time.Since(start).Milliseconds(), Streamed: streamed}, nil
}

// busEvent is one line of the opencode event bus: flat on /event, wrapped in
// a payload on /global/event.
type busEvent struct {
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	Properties struct {
		SessionID string          `json:"sessionID"`
		PartID    string          `json:"partID"`
		Field     string          `json:"field"`
		Delta     string          `json:"delta"`
		Part      json.RawMessage `json:"part"`
	} `json:"properties"`
}

// openEvents connects to one SSE endpoint of the serve; the body lives for
// the whole run.
func (d *OpencodeDispatcher) openEvents(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{}).Do(req) // no timeout: lives for the whole run
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	return resp, nil
}

// streamEvents tails the opencode SSE bus and live-prints this session's
// generation: text deltas to stdout, tool activity to stderr - the same feed
// the opencode TUI renders. Reports initial connect success on conn.
func (d *OpencodeDispatcher) streamEvents(ctx context.Context, conn chan<- bool) {
	// /global/event carries every directory's events, each wrapped in
	// {directory, project, payload}. /event is scoped to the serve's OWN
	// instance - the folder it was started in - so a worker session pinned to
	// any other folder was invisible on it: no deltas, no tool statuses, and
	// the stall watchdog blind ("bus degraded" on every arc run while the
	// serve sat in DLM; on every run once the serve moved to captain's own
	// dir, 2026-09-12). /event stays as the fallback for a serve without the
	// global bus.
	resp, err := d.openEvents(ctx, "/global/event")
	if err != nil {
		resp, err = d.openEvents(ctx, "/event")
	}
	if err != nil {
		conn <- false
		return
	}
	conn <- true
	defer resp.Body.Close()

	partTypes := map[string]string{}
	toolSeen := map[string]bool{}
	toolRunning := map[string]bool{}
	toolDone := map[string]bool{}
	narrated := map[string]bool{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var e busEvent
		raw := []byte(strings.TrimPrefix(line, "data:"))
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		if len(e.Payload) > 0 { // the global bus envelope: the event is inside
			if json.Unmarshal(e.Payload, &e) != nil {
				continue
			}
		}
		if e.Properties.SessionID != d.SessionID {
			continue
		}
		// Any event for this session is proof of life for the stall watchdog.
		d.lastActivity.Store(time.Now().UnixNano())
		switch e.Type {
		case "message.part.updated":
			var p struct {
				ID    string `json:"id"`
				Type  string `json:"type"`
				Tool  string `json:"tool"`
				Text  string `json:"text"` // text / reasoning parts carry their content so far
				State struct {
					Status string `json:"status"`
					Title  string `json:"title"`
					Output string `json:"output"`
					Error  string `json:"error"`
				} `json:"state"`
			}
			if json.Unmarshal(e.Properties.Part, &p) != nil {
				continue
			}
			// The worker's own words between tools, when nobody streams the
			// text itself; a command's result under its start line.
			if p.Type == "text" && d.OnDelta == nil && d.OnStatus != nil && !narrated[p.ID] && len(p.Text) >= 40 {
				if s := Narration(p.Text); s != "" {
					narrated[p.ID] = true
					d.OnStatus(s)
				}
			}
			if p.Type == "tool" && d.OnStatus != nil && toolSeen[p.ID] && !toolDone[p.ID] && (p.State.Status == "completed" || p.State.Status == "error") {
				toolDone[p.ID] = true
				if p.Tool == "bash" || p.State.Status == "error" {
					out := p.State.Output
					if p.State.Status == "error" && p.State.Error != "" {
						out = p.State.Error
					}
					if s := Outcome(out, 0, p.State.Status == "error"); s != "" {
						d.OnStatus(s)
					}
				}
			}
			partTypes[p.ID] = p.Type
			// Generation actually started (as opposed to session bookkeeping):
			// this is what distinguishes "working quietly" from "dead on arrival".
			switch p.Type {
			case "text", "reasoning", "tool":
				d.sawOutput.Store(true)
			}
			// Track in-flight tools: silence WITH a tool running is a build or a
			// test suite; silence with none is a dead stream (live 2026-07-30: an
			// xAI session emitted a little, then nothing, and got the full 4m).
			if p.Type == "tool" {
				switch p.State.Status {
				case "running":
					if !toolRunning[p.ID] {
						toolRunning[p.ID] = true
						d.activeTools.Add(1)
					}
				case "completed", "error", "aborted":
					if toolRunning[p.ID] {
						delete(toolRunning, p.ID)
						d.activeTools.Add(-1)
					}
				}
			}
			if p.Type == "tool" && p.State.Status == "running" && !toolSeen[p.ID] {
				toolSeen[p.ID] = true
				if d.OnStatus != nil {
					d.OnStatus(workerStatus(p.Tool, p.State.Title)) // brain progress feed → the TUI
				} else {
					fmt.Fprintf(os.Stderr, "\n  ⚙ %s %s\n", p.Tool, p.State.Title)
				}
			}
		case "message.part.delta":
			d.sawOutput.Store(true)
			if e.Properties.Field != "text" {
				continue
			}
			if t, known := partTypes[e.Properties.PartID]; known && t != "text" {
				continue // reasoning/tool deltas stay off the answer stream
			}
			d.amu.Lock()
			d.acc.WriteString(e.Properties.Delta)
			d.amu.Unlock()
			if d.OnDelta != nil {
				d.OnDelta(e.Properties.Delta) // stream into the brain's SSE response
			} else {
				fmt.Print(e.Properties.Delta) // terminal live-print (CLI)
			}
		}
	}
}

// sessionFingerprint polls the session's messages over REST and returns a
// cheap digest of their current state. The /event bus is the fast activity
// sensor, but it can die silently (2026-08-07: server.connected then nothing,
// for a week) - REST is the ground truth the watchdog verifies against before
// killing a run.
func (d *OpencodeDispatcher) sessionFingerprint() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.BaseURL+"/session/"+d.SessionID+"/message", nil)
	if err != nil {
		return "", false
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	// Hash CONTENT only. The raw JSON carries ticking timestamps and token
	// counters, which made the fingerprint "progress" on every poll - the
	// watchdog never fired again and a failing stage crawled 45 minutes to the
	// hard cap (2026-08-07). Part text and tool state are what progress means.
	var msgs []struct {
		Parts []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Tool  string `json:"tool"`
			State struct {
				Status string `json:"status"`
			} `json:"state"`
		} `json:"parts"`
	}
	h := sha256.New()
	if json.Unmarshal(body, &msgs) == nil {
		for _, m := range msgs {
			for _, p := range m.Parts {
				fmt.Fprintf(h, "%s|%s|%s|%s\n", p.Type, p.Text, p.Tool, p.State.Status)
			}
		}
	} else {
		h.Write(body) // unknown shape: raw hash is better than nothing
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:12]), true
}

// sessionHasAssistantOutput reports whether the session holds any assistant
// part at all - text, reasoning or a tool call - as opposed to only the user
// turns captain posted. The first-event REST cross-check needs this
// distinction (see the watchdog).
func (d *OpencodeDispatcher) sessionHasAssistantOutput() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.BaseURL+"/session/"+d.SessionID+"/message", nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return false
	}
	defer resp.Body.Close()
	var msgs []struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&msgs) != nil {
		return false
	}
	for _, m := range msgs {
		if m.Info.Role != "assistant" {
			continue
		}
		for _, p := range m.Parts {
			switch p.Type {
			case "text", "reasoning":
				if strings.TrimSpace(p.Text) != "" {
					return true
				}
			case "tool", "tool_use", "tool-invocation":
				return true
			}
		}
	}
	return false
}

// streamed returns everything this session has emitted so far.
func (d *OpencodeDispatcher) streamed() string {
	d.amu.Lock()
	defer d.amu.Unlock()
	return strings.TrimSpace(d.acc.String())
}

// opencodeErrorText extracts the parts of an opencode error JSON that are safe
// to CLASSIFY on: the error name, its message, and the upstream status code.
// The raw blob also embeds upstream response HEADERS (x-ratelimit-*) and other
// metadata - matching "rate" against the whole blob once turned an NVIDIA 404
// (model not found) into a bogus 30-minute rate-limit cooldown (2026-07-18).
// Falls back to the raw string only when the shape doesn't parse.
func opencodeErrorText(raw json.RawMessage) string {
	var e struct {
		Name string `json:"name"`
		Data struct {
			Message    string `json:"message"`
			StatusCode int    `json:"statusCode"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &e) == nil && (e.Name != "" || e.Data.Message != "") {
		return strings.ToLower(fmt.Sprintf("%s %s %d", e.Name, e.Data.Message, e.Data.StatusCode))
	}
	return strings.ToLower(string(raw))
}

// extractResponseHeaders pulls the upstream responseHeaders map from an
// opencode error JSON blob. The blob embeds x-ratelimit-* headers the
// provider returned alongside the error — the same headers QuotaFromHeaders
// parses for proactive quota telemetry (M2.3). Returns nil when the blob
// does not carry headers or does not parse, so a caller can treat nil as
// "no quota signal" without distinguishing "not present" from "malformed".
func extractResponseHeaders(raw json.RawMessage) http.Header {
	var e struct {
		Data struct {
			ResponseHeaders map[string]string `json:"responseHeaders"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	if len(e.Data.ResponseHeaders) == 0 {
		return nil
	}
	h := make(http.Header, len(e.Data.ResponseHeaders))
	for k, v := range e.Data.ResponseHeaders {
		h.Set(k, v)
	}
	return h
}

// abort best-effort cancels the session's in-flight generation server-side,
// so a timed-out call doesn't keep burning quota in the background.
func (d *OpencodeDispatcher) abort() {
	if d.SessionID == "" {
		return
	}
	req, err := http.NewRequest(http.MethodPost, d.BaseURL+"/session/"+d.SessionID+"/abort", nil)
	if err != nil {
		return
	}
	if resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req); err == nil {
		resp.Body.Close()
	}
}

// Note posts a message into the dispatcher's session without triggering
// inference (noReply) - the channel behind the captain tab and Claude-leg
// mirroring. Best-effort: errors are returned but callers may ignore them.
func (d *OpencodeDispatcher) Note(text string) error {
	if d.SessionID == "" {
		if err := d.ensureSession(); err != nil {
			return err
		}
	}
	body, _ := json.Marshal(map[string]any{
		"noReply": true,
		"parts":   []map[string]string{{"type": "text", "text": text}},
	})
	resp, err := d.Client.Post(d.BaseURL+"/session/"+d.SessionID+"/message", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (d *OpencodeDispatcher) EnsureServer() error {
	if d.portOpen() {
		if d.serveHasProviders() {
			return nil
		}
		// A serve with no providers is not ours: started by a test with a
		// throwaway HOME, or before the keys were in place. Every session it
		// would host fails, so recycle it - the launcher does the same on
		// restart - and spawn a real one (2026-09-18).
		if !d.Spawn {
			return fmt.Errorf("opencode serve at %s has no providers configured (started with the wrong HOME?) - restart captain-code.sh", d.BaseURL)
		}
		port := d.BaseURL[strings.LastIndex(d.BaseURL, ":")+1:]
		fmt.Fprintf(os.Stderr, "captain: opencode serve on %s has no providers - recycling it\n", d.BaseURL)
		_ = exec.Command("pkill", "-f", "opencode serve --port "+port).Run()
		for i := 0; i < 25 && d.portOpen(); i++ {
			time.Sleep(200 * time.Millisecond)
		}
		if d.portOpen() {
			return fmt.Errorf("opencode serve at %s has no providers and would not stop - restart captain-code.sh", d.BaseURL)
		}
	}
	if !d.Spawn {
		return fmt.Errorf("opencode serve not running at %s", d.BaseURL)
	}
	port := d.BaseURL[strings.LastIndex(d.BaseURL, ":")+1:]
	cmd := exec.Command("opencode", "serve", "--port", port, "--hostname", "127.0.0.1")
	// The serve is shared by every workspace and every session it hosts is
	// pinned with ?directory=, so it runs in captain's own dir, not a project.
	_ = os.MkdirAll(umbrellaDir(), 0o755)
	cmd.Dir = umbrellaDir()
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn opencode serve: %w", err)
	}
	_ = cmd.Process.Release()
	for i := 0; i < 50; i++ {
		if d.portOpen() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("opencode serve did not become ready on %s", d.BaseURL)
}

// serveHasProviders: the serve answers /config/providers with at least one
// provider. A serve started under a HOME with no opencode.jsonc and no
// auth.json has none, and cannot host a session (see EnsureServer).
// Checked once a minute per base URL; a serve that cannot answer at all is
// left to the session path to report.
func (d *OpencodeDispatcher) serveHasProviders() bool {
	serveCheckMu.Lock()
	if at, ok := serveChecked[d.BaseURL]; ok && time.Since(at) < time.Minute {
		serveCheckMu.Unlock()
		return true
	}
	serveCheckMu.Unlock()
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(d.BaseURL + "/config/providers")
	if err != nil {
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return true
	}
	var out struct {
		Providers *[]json.RawMessage `json:"providers"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out) != nil || out.Providers == nil {
		return true // not a providers answer (a fake, an older serve): no verdict
	}
	if len(*out.Providers) == 0 {
		return false
	}
	serveCheckMu.Lock()
	serveChecked[d.BaseURL] = time.Now()
	serveCheckMu.Unlock()
	return true
}

var (
	serveCheckMu sync.Mutex
	serveChecked = map[string]time.Time{}
)

func (d *OpencodeDispatcher) portOpen() bool {
	addr := strings.TrimPrefix(d.BaseURL, "http://")
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (d *OpencodeDispatcher) ensureSession() error {
	if d.SessionID != "" {
		return nil
	}
	title := d.Title
	if title == "" {
		title = "captain-worker"
	}
	// Pin the session to the CALLER's project (?directory=), never the serve's
	// own cwd: the serve is long-lived and keeps the cwd it was born with, so
	// after a project switch new sessions would silently explore the PREVIOUS
	// repo (live 2026-07-19: a Compliance question answered from Relay).
	dir := d.Dir
	if dir == "" {
		dir = DefaultWorkspace().Dir
	}
	// Workers are CHILD sessions of one umbrella per project. Every opencode
	// instance shares the session database, and the TUI's `-c` resumes the
	// most recently updated top-level session in the directory - which, after
	// any worker ran, was the worker (live 2026-09-11: the user's TUI opened a
	// captain-free session and showed him a title-generator prompt). The TUI
	// filters children out (`parentID === undefined`), the way it hides its
	// own subagent sessions. The umbrella itself lives OUTSIDE the project
	// (see umbrellaSession), or it is the top-level session `-c` resumes.
	parent, err := d.umbrellaSession(dir)
	if err != nil {
		return err
	}
	id, err := d.createSession(dir, title, parent)
	if err != nil {
		return err
	}
	d.SessionID = id
	return nil
}

// umbrellas holds the per-directory umbrella session id for each serve.
var (
	umbrellaMu sync.Mutex
	umbrellas  = map[string]string{} // baseURL + "\x00" + dir → session id
)

// resetUmbrellas forgets every umbrella (tests, and a serve recycle).
func resetUmbrellas() {
	umbrellaMu.Lock()
	defer umbrellaMu.Unlock()
	umbrellas = map[string]string{}
}

// umbrellaDir is the directory every umbrella session is pinned to: captain's
// own state dir, never the user's project. A top-level session in the project
// directory is exactly what the TUI's `-c` resumes, so the first umbrella
// version (a "captain workers" session in the project, one per brain start)
// reproduced the bug it was meant to fix: after any worker ran, `-c` opened
// an EMPTY umbrella and the user's transcript was nowhere (live 2026-09-11,
// twice - once typing ten turns into the umbrella itself). Children keep
// their project directory; opencode never checks that a child and its parent
// share one.
func umbrellaDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "captaincode", "workers")
	}
	return filepath.Join(home, ".captaincode", "workers")
}

// umbrellaTitle names the project's umbrella so a restarted brain finds the
// one it made last time instead of leaving a new empty session per start.
func umbrellaTitle(dir string) string { return "captain workers · " + dir }

// umbrellaSession returns the project's umbrella session: the one cached for
// this brain, else the one a previous brain left in umbrellaDir, else a new one.
func (d *OpencodeDispatcher) umbrellaSession(dir string) (string, error) {
	key := d.BaseURL + "\x00" + dir
	umbrellaMu.Lock()
	id, ok := umbrellas[key]
	umbrellaMu.Unlock()
	if ok {
		return id, nil
	}
	home := umbrellaDir()
	_ = os.MkdirAll(home, 0o755)
	id = d.findSession(home, umbrellaTitle(dir))
	if id == "" {
		var err error
		id, err = d.createSession(home, umbrellaTitle(dir), "")
		if err != nil {
			return "", err
		}
	}
	umbrellaMu.Lock()
	umbrellas[key] = id
	umbrellaMu.Unlock()
	return id, nil
}

// findSession returns the id of the newest top-level session in dir with
// exactly this title, or "" (not found, or the serve did not answer).
func (d *OpencodeDispatcher) findSession(dir, title string) string {
	resp, err := d.Client.Get(d.BaseURL + "/session?roots=true&directory=" + url.QueryEscape(dir))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var list []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		ParentID string `json:"parentID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return ""
	}
	for _, s := range list {
		if s.Title == title && s.ParentID == "" {
			return s.ID
		}
	}
	return ""
}

// createSession creates one opencode session pinned to dir (the ?directory=
// query param is the only form 1.17.20 honours; the body's directory field is
// ignored), optionally as a child of parent.
func (d *OpencodeDispatcher) createSession(dir, title, parent string) (string, error) {
	createURL := d.BaseURL + "/session"
	if dir != "" {
		createURL += "?directory=" + url.QueryEscape(dir)
	}
	in := map[string]string{"title": title}
	if parent != "" {
		in["parentID"] = parent
	}
	body, _ := json.Marshal(in)
	resp, err := d.Client.Post(createURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var s struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil || s.ID == "" {
		return "", fmt.Errorf("create opencode session: %v", err)
	}
	return s.ID, nil
}

// cursorPermissionArgs: cursor-agent -p is headless too. --trust clears the
// workspace-trust prompt; --force auto-allows commands unless a rule
// explicitly denies them, the same stance as claude's
// --dangerously-skip-permissions and codex's --dangerously-bypass-approvals
// (the repo is the blast radius; a headless approval prompt is a hang).
// CAPTAIN_CURSOR_PERMISSIONS: "default" keeps --trust only, any other value
// is passed through verbatim.
func cursorPermissionArgs() []string {
	switch v := os.Getenv("CAPTAIN_CURSOR_PERMISSIONS"); v {
	case "", "skip", "force":
		return []string{"--trust", "--force"}
	case "default":
		return []string{"--trust"}
	default:
		return append([]string{"--trust"}, strings.Fields(v)...)
	}
}

// claudePermissionArgs: claude -p is headless - permission prompts can't be
// answered, so tools like WebFetch get auto-denied and the worker is silently
// weaker than every other leg (live 2026-07-26). Default: full trust on the
// user's own machine, same stance as cursor-agent --trust and the opencode
// workers. CAPTAIN_CLAUDE_PERMISSIONS: "default" restores stock denials, any
// other value is passed as --permission-mode.
func claudePermissionArgs() []string {
	switch v := os.Getenv("CAPTAIN_CLAUDE_PERMISSIONS"); v {
	case "", "skip":
		return []string{"--dangerously-skip-permissions"}
	case "default":
		return nil
	default:
		return []string{"--permission-mode", v}
	}
}

func runClaude(dir, task string) (Result, error) {
	return runClaudeTimeout(dir, task, 0)
}

// RunClaudeText runs a prompt through captain's own Claude wrapper (claude -p on
// the Max subscription) and returns the reply. This is how the captain-code
// fork executes the claude leg - via captain's wrapper, NOT opencode's anthropic
// provider/registry (no API credits, no model-id matching).
func RunClaudeText(prompt string) (string, error) {
	res, err := runClaude(DefaultWorkspace().Dir, prompt)
	return res.Text, err
}

// RunWorker executes any leg through captain's own wrapper and returns the reply:
// claude via claude -p; every other leg via captain's opencode dispatch (using
// captain's known provider/model ids on port). This is the single entry the
// captain-code fork calls so ALL models are reached in their wrapped form, not
// through the fork's own opencode model registry.
func RunWorker(leg Leg, task string, port int) (string, error) {
	res, err := RunWorkerStream(leg, task, port, nil)
	return res.Text, err
}

// RunWorker is RunWorker in this workspace.
func (ws Workspace) RunWorker(leg Leg, task string, port int) (string, error) {
	res, err := ws.RunWorkerStreamHooks(leg, task, port, nil, nil)
	return res.Text, err
}

// RunWorkerStream is RunWorker with live output: onDelta (if non-nil) is called
// with each chunk of text as the worker produces it, so the brain can stream
// tokens into its SSE response instead of sitting silent until completion. The
// Every leg streams live: claude via `claude -p --output-format stream-json`,
// cursor via `cursor-agent --output-format stream-json`, and free/grok/codex via
// the opencode event bus (the dispatcher's OnDelta hook). Returns the full Result
// (authoritative text, cost, tokens) and propagates ErrRateLimited so the brain
// can cool the leg down.
func RunWorkerStream(leg Leg, task string, port int, onDelta func(string)) (Result, error) {
	return RunWorkerStreamHooks(leg, task, port, onDelta, nil)
}

// RunWorkerStream is RunWorkerStream in this workspace.
func (ws Workspace) RunWorkerStream(leg Leg, task string, port int, onDelta func(string)) (Result, error) {
	return ws.RunWorkerStreamHooks(leg, task, port, onDelta, nil)
}

// RunWorkerStreamHooks is RunWorkerStream plus the progress channel: onStatus
// (if non-nil) receives one line per tool the worker starts, so the caller can
// show what the run is doing while it produces no answer text. Statuses are
// out-of-band - they never enter the answer, the ledger, or the scorecard.
func RunWorkerStreamHooks(leg Leg, task string, port int, onDelta, onStatus func(string)) (Result, error) {
	return DefaultWorkspace().RunWorkerStreamHooks(leg, task, port, onDelta, onStatus)
}

// RunWorkerStreamHooks is RunWorkerStreamHooks with the worker running in
// this workspace - the entry every brain request goes through.
func (ws Workspace) RunWorkerStreamHooks(leg Leg, task string, port int, onDelta, onStatus func(string)) (Result, error) {
	// --notimeout in the current turn ×10s the bounds; stripped so the worker
	// never sees it (parsed here - the one choke point every path crosses).
	// Frontier-class legs (codex-cli) carry 2× on top - deep reasoning is slow by
	// design, the same allowance /frontier gets.
	mul := time.Duration(NoTimeoutMul(task)) * legBudgetMultiplier(leg)
	task = StripNoTimeout(task)
	base := workerTimeout() * mul
	ceil := workerCeiling() * mul
	// Naming a session is housekeeping: bound it hard so a stalled title cannot
	// hold a worker for minutes (live 2026-09-11: a title stalled the free leg
	// for 4m and was then rerouted to a second leg).
	if IsTitlePrompt(task) {
		base, ceil = titleBudget()
	}
	switch specs[leg].Transport {
	case TransportSystemOne:
		// Every dispatch path is guarded before this point (Rungs, the
		// TUI's model list, forced prefixes, workflow parsing); this is
		// the backstop for the one that is not.
		return Result{}, fmt.Errorf("/%s is a %w - `captain jev classify <task>` asks it a question", leg, ErrDecisionLeg)
	case TransportClaudeCLI:
		res, err := runClaudeStreamOpts(ws.Dir, task, base, ceil, onDelta, onStatus, false, ws.Effort, ws.Steer)
		return emptyIsFailure(leg, res, err)
	case TransportCursorCLI: // cursor-agent has no effort knob
		res, err := runCursorStream(ws.Dir, task, base, ceil, onDelta, onStatus, ws.Steer)
		return emptyIsFailure(leg, res, err)
	case TransportCodexCLI:
		res, err := runCodexCLIStream(ws.Dir, task, base, ceil, onDelta, onStatus, ws.Effort, ws.Steer)
		return emptyIsFailure(leg, res, err)
	}
	// free/grok/codex: the dispatcher tails opencode's event bus; OnDelta forwards
	// each token to the brain's SSE response as it's generated. Timeout is the hard
	// backstop against a worker that blocks (e.g. on the interactive question tool);
	// StallTimeout catches a turn that wedges mid-run (no events at all).
	d := NewDispatcher(port)
	d.Dir = ws.Dir
	d.Effort = ws.Effort
	d.Steer = ws.Steer
	d.Title = "captain-" + string(leg)
	d.OnDelta = onDelta
	d.OnStatus = onStatus
	d.Timeout = base
	d.Ceiling = ceil
	d.StallTimeout = workerStallTimeout()
	res, err := d.Run(leg, task)
	if errors.Is(err, ErrWorkerStalled) && !strings.Contains(err.Error(), "never started generating") {
		// Self-heal: the stalled attempt was aborted having made no recent
		// progress, so re-running is safe. Fresh dispatcher = fresh session -
		// the wedged session's context is poison. One retry only; a second
		// stall surfaces as a real error. A leg that never produced a first
		// token is the PROVIDER not answering: the retry only doubles the
		// wait (2026-09-18) - the reroute net takes it from here.
		fmt.Fprintf(os.Stderr, "captain: %s worker stalled (%v) - retrying once on a fresh session\n", leg, err)
		d = NewDispatcher(port)
		d.Dir = ws.Dir
		d.Effort = ws.Effort
		d.Steer = ws.Steer
		d.Title = "captain-" + string(leg)
		d.OnDelta = onDelta
		d.OnStatus = onStatus
		d.Timeout = workerTimeout()
		d.StallTimeout = workerStallTimeout()
		res, err = d.Run(leg, task)
	}
	// Fallback: if the event stream never connected, no deltas were forwarded -
	// emit the whole reply once so the client still gets the answer.
	if err == nil && onDelta != nil && !res.Streamed && res.Text != "" {
		onDelta(res.Text)
	}
	if err == nil && strings.TrimSpace(res.Text) == "" {
		err = fmt.Errorf("%s: %w", leg, ErrEmptyOutput)
	}
	return res, err
}

// frontierCmdConfig returns the extra claude -p args and env for /frontier:
// the strongest model version, explicitly pinned (CAPTAIN_FRONTIER_MODEL,
// default claude-fable-5), with a maxed extended-thinking budget
// (CAPTAIN_FRONTIER_THINKING tokens, default 31999 - Claude Code reads
// MAX_THINKING_TOKENS). Best model, best version, highest effort.
func frontierCmdConfig(effort Effort) (args []string, env []string) {
	model := os.Getenv("CAPTAIN_FRONTIER_MODEL")
	if model == "" {
		// The ALIAS, not a pinned version: /frontier means "the strongest thing
		// available", and an alias resolves to the newest release of that tier
		// without a code change every time one ships (2026-09-09). Pin
		// CAPTAIN_FRONTIER_MODEL to a full name to freeze it.
		model = "fable"
	}
	// Effort, not a raw token budget: claude -p exposes low|medium|high|xhigh|max.
	// /frontier is the request's effort (max - the user asked for the ceiling,
	// 2026-09-13; xhigh had been the deliberate second-to-best until then);
	// CAPTAIN_FRONTIER_EFFORT pins it.
	flag := os.Getenv("CAPTAIN_FRONTIER_EFFORT")
	if flag == "" {
		if effort == "" {
			effort = EffortMax
		}
		flag = effort.ClaudeFlag()
	}
	args = []string{"--model", model, "--effort", flag}
	// Legacy escape hatch: an explicit thinking budget still wins if set.
	if think := os.Getenv("CAPTAIN_FRONTIER_THINKING"); think != "" {
		env = []string{"MAX_THINKING_TOKENS=" + think}
	}
	return args, env
}

// transientProviderError spots terminal CLI failures that are the provider's
// CONNECTION, not the task: cursor-agent's "RetriableError: Connection stalled
// repeatedly" surfaced as a generic exit status 1 and bypassed the whole
// reroute/bench machinery - 7m10s, then nothing (live 2026-08-06).
func transientProviderError(msg string) bool {
	m := strings.ToLower(msg)
	for _, p := range []string{
		"retriableerror", "connection stalled", "connection error", "connect error",
		"econnreset", "etimedout", "socket hang up", "stream disconnected",
		"connection reset", "network error",
	} {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

// emptyIsFailure turns a "successful" run with no text into an error: a silent
// turn is indistinguishable from a broken one, and rerouting costs less than
// leaving the user with nothing.
func emptyIsFailure(leg Leg, res Result, err error) (Result, error) {
	if err == nil && strings.TrimSpace(res.Text) == "" {
		return res, fmt.Errorf("%s: %w", leg, ErrEmptyOutput)
	}
	if err == nil && IsRefusal(res.Text) {
		return res, fmt.Errorf("%s: %w", leg, ErrRefused)
	}
	return res, err
}

// refusalRe matches a worker declining the task outright. A refusal currently
// counts as a SUCCESSFUL run: the leg returns polite prose, the scorecard
// records a win, and the user gets nothing (live 2026-09-09 - a provider began
// refusing ordinary crypto-backend engineering in the user's own repo). Treated
// as a leg-level failure it reroutes to a provider whose policy fits the work,
// which is what a router is for.
//
// Deliberately narrow and anchored to the START of the reply: a long answer
// that merely discusses refusals, or says "I can't reproduce this bug", must
// not trip it.
var refusalRe = regexp.MustCompile(`(?i)^\W{0,4}(i(?:'m| am) (?:sorry|afraid|unable)|i (?:can(?:'t|not)|won't) (?:help|assist|provide|continue|comply)|sorry,? (?:but )?i (?:can(?:'t|not)|won't)|i must decline|as an ai(?:[ ,]|$))`)

// IsRefusal reports whether a worker declined the task rather than doing it.
func IsRefusal(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 1200 { // a real refusal is short; a long answer is work
		return false
	}
	first := t
	if i := strings.IndexByte(t, '\n'); i > 0 {
		first = t[:i]
	}
	return refusalRe.MatchString(strings.TrimSpace(first))
}

// RunClaudeFrontierStream is the /frontier runner: claude -p at frontier
// settings, with double the normal worker time budget (deep thinking is slow
// by design).
func RunClaudeFrontierStream(task string, onDelta, onStatus func(string)) (Result, error) {
	return DefaultWorkspace().RunClaudeFrontierStream(task, onDelta, onStatus)
}

// RunClaudeFrontierStream is RunClaudeFrontierStream in this workspace.
func (ws Workspace) RunClaudeFrontierStream(task string, onDelta, onStatus func(string)) (Result, error) {
	return runClaudeStreamOpts(ws.Dir, task, 2*workerTimeout(), 2*workerCeiling(), onDelta, onStatus, true, EffortMax, ws.Steer)
}

// claudeStreamLine is one newline-delimited JSON event from `claude -p
// --output-format stream-json`. We only need the text deltas (partial messages)
// and the terminal "result" event (authoritative text + cost + error status).
type claudeStreamLine struct {
	Type  string `json:"type"`
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	// "assistant" events carry the turn's content blocks - tool_use blocks are
	// the only visible sign of life during a long tool chain.
	Message struct {
		Content []struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			Text      string          `json:"text"`
			Input     json.RawMessage `json:"input"`
			ID        string          `json:"id"`          // tool_use id
			ToolUseID string          `json:"tool_use_id"` // tool_result: which call
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"` // tool_result body: a string or blocks
		} `json:"content"`
	} `json:"message"`
	// terminal "result" event fields
	IsError        bool    `json:"is_error"`
	APIErrorStatus int     `json:"api_error_status"`
	Result         string  `json:"result"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	Usage          struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// waitBounded waits for a finished/killed command, but not forever: Stderr is a
// strings.Builder, so exec.Wait blocks on its copy goroutine until EVERY holder
// of the pipe closes it - and an orphaned grandchild (a shell's `sleep`, a
// spawned helper) inherits it. Without the bound, a run that hit its time cap
// still blocked for as long as the grandchild lived, so the cap was not really
// a cap (live 2026-07-30: a capped worker's partial output arrived minutes late,
// or never).
func waitBounded(cmd *exec.Cmd, grace time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(grace):
		return fmt.Errorf("worker exited but did not release its output pipes within %s", grace)
	}
}

// claudeStdin is claude -p's stream-json input: one user message per line,
// the task first, then any /btw note. Writes after close (the turn ended)
// fail rather than block.
type claudeStdin struct {
	mu     sync.Mutex
	w      io.WriteCloser
	closed bool
}

func (c *claudeStdin) send(text string) error {
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("the worker's turn has ended")
	}
	_, err = c.w.Write(append(line, '\n'))
	return err
}

func (c *claudeStdin) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.w.Close()
	}
}

// runClaudeStream runs claude -p in streaming mode, forwarding each text_delta to
// onDelta as it arrives, and returns the final Result from the terminal event.
func runClaudeStream(dir, task string, timeout time.Duration, onDelta, onStatus func(string)) (Result, error) {
	return runClaudeStreamOpts(dir, task, timeout, 0, onDelta, onStatus, false, "", nil)
}

// runClaudeStreamPA is the progress-aware variant: past base, the run dies
// only if quiet; ceil is the absolute stop.
func runClaudeStreamPA(dir, task string, base, ceil time.Duration, onDelta, onStatus func(string)) (Result, error) {
	return runClaudeStreamOpts(dir, task, base, ceil, onDelta, onStatus, false, "", nil)
}

// dir is the workspace every runner below executes in ("" = the brain's cwd).
func runClaudeStreamOpts(dir, task string, timeout, ceil time.Duration, onDelta, onStatus func(string), frontier bool, effort Effort, steer *Steer) (Result, error) {
	start := time.Now()
	ctx := context.Background()
	prog := &progress{}
	var capped *atomic.Bool
	if timeout > 0 {
		// Progress-aware always (see progressCtx): a run with a tool in
		// flight or output flowing is never cut for taking long.
		var cancel context.CancelFunc
		ctx, cancel, prog, capped = progressCtx(timeout, ceil)
		defer cancel()
	}
	// /interrupt can end the run and keep what it streamed (steer.go).
	ctx, stopped, detachStop := interruptible(ctx, steer, LegClaude)
	defer detachStop()
	// stream-json IN as well as out: the task goes down stdin as a user
	// message, and so does any /btw note sent while the run lasts - claude
	// folds it into the running turn at its next tool boundary (steer.go).
	args := append([]string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}, claudePermissionArgs()...)
	args = append(args, ClaudeMCPArgs(dir)...) // Euclid memory tools, same as the opencode workers get
	args = append(args, ClaudeDirArgs(dir)...) // sibling repos and scratch dirs are working directories too
	var fenv []string
	if frontier {
		var fargs []string
		fargs, fenv = frontierCmdConfig(effort)
		args = append(args, fargs...)
	} else if effort != "" {
		// The request's effort (effort.go): a typo fix does not think like a
		// migration. Unset → claude's own default.
		args = append(args, "--effort", effort.ClaudeFlag())
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	// Through the egress proxy while it listens (ANTHROPIC_BASE_URL): the
	// Max login is unaffected, the bearer rides through (verified live
	// 2026-09-13) and every request body is redacted before the socket.
	fenv = append(fenv, ClaudeProxyEnv()...)
	if len(fenv) > 0 {
		cmd.Env = append(os.Environ(), fenv...)
	}
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("claude -p pipe: %w", err)
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("claude -p stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("claude -p start: %w", err)
	}
	in := &claudeStdin{w: stdinPipe}
	defer in.close()
	// The task itself, off the scan loop: a pipe takes 64K before the reader
	// must drain it, and a prompt is often longer. A write that fails (the
	// process died before reading) surfaces through the run's own exit.
	go func() {
		if err := in.send(task); err != nil {
			fmt.Fprintf(os.Stderr, "captain: claude -p did not take its prompt: %v\n", err)
		}
	}()
	if steer != nil {
		defer steer.Attach(LegClaude, func(note string) error {
			if err := in.send(SteerLine(note)); err != nil {
				return err
			}
			if onStatus != nil {
				onStatus("btw from the user taken: " + truncateStr(strings.TrimSpace(note), 120))
			}
			return nil
		})()
	}

	// The hard cap must be authoritative: CommandContext kills the child, but a
	// grandchild holding stdout would keep the scan loop blocked past the
	// deadline. Closing the pipe on ctx.Done() unblocks it.
	scanDone := make(chan struct{})
	defer close(scanDone)
	go func() {
		select {
		case <-ctx.Done():
			in.close()
			stdout.Close()
		case <-scanDone:
		}
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // signature/thinking lines get large
	var final claudeStreamLine
	haveFinal := false
	var acc strings.Builder
	longest := ""                   // longest assistant text block seen (see the "assistant" case)
	blockOpen := false              // a text block is mid-stream: deltas continue the same paragraph
	shellCalls := map[string]bool{} // tool_use ids of Bash calls: their results are worth a peek
	for sc.Scan() {
		prog.touch() // progress stamp: any output line counts as activity
		var ev claudeStreamLine
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "user":
			// Tool results come back as user turns: each one closes a tool,
			// and a command's result is shown under its start line.
			for _, blk := range ev.Message.Content {
				if blk.Type != "tool_result" {
					continue
				}
				prog.toolEnd()
				if onStatus != nil && (shellCalls[blk.ToolUseID] || blk.IsError) {
					if s := Outcome(toolResultText(blk.Content), 0, blk.IsError); s != "" {
						onStatus(s)
					}
				}
			}
		case "stream_event":
			switch ev.Event.Type {
			case "content_block_stop", "message_stop":
				// The next text block is a new paragraph: the model's
				// narration between tool calls arrives as separate blocks,
				// and joined bare it read as one run-on line ("…paths.Shadow
				// work is already…", 2026-09-18).
				blockOpen = false
			case "content_block_delta":
				if ev.Event.Delta.Type == "text_delta" && ev.Event.Delta.Text != "" {
					if !blockOpen && acc.Len() > 0 && !strings.HasSuffix(acc.String(), "\n") {
						acc.WriteString("\n\n")
						if onDelta != nil {
							onDelta("\n\n")
						}
					}
					blockOpen = true
					acc.WriteString(ev.Event.Delta.Text)
					if onDelta != nil {
						onDelta(ev.Event.Delta.Text)
					}
				}
			}
		case "assistant":
			// Remember the longest assistant message: `result` is only the LAST
			// one, and a session that spawns subagents ends on a one-line
			// reaction to their completion notifications - an 11-minute Arc
			// cohort delivered 293 chars while its 5k-char report sat one
			// message earlier (2026-09-10).
			for _, blk := range ev.Message.Content {
				if blk.Type == "text" && len(strings.TrimSpace(blk.Text)) > len(longest) {
					longest = strings.TrimSpace(blk.Text)
				}
			}
			// Tool calls are the run's only visible activity between text
			// bursts - report each one as it is issued, and count it in
			// flight until its result comes back (a running tool is never
			// "quiet"). The prose in the same message is the worker's own
			// account of what it is doing: shown when nobody streams the
			// text itself (team, workflow, /repeat rounds).
			for _, blk := range ev.Message.Content {
				if blk.Type == "text" && onStatus != nil && onDelta == nil {
					if s := Narration(blk.Text); s != "" {
						onStatus(s)
					}
				}
				if blk.Type != "tool_use" {
					continue
				}
				prog.toolStart()
				if blk.Name == "Bash" {
					shellCalls[blk.ID] = true
				}
				if onStatus == nil {
					continue
				}
				if s := workerStatus(blk.Name, claudeToolDetail(blk.Input)); s != "" {
					onStatus(s)
				}
			}
		case "result":
			final, haveFinal = ev, true
			// In stream-json input mode claude waits for the next user
			// message after a turn; the run is one turn, so closing stdin
			// here is what lets it exit. A note arriving after this point
			// fails to send and the brain says so.
			in.close()
		}
	}
	in.close()
	waitErr := waitBounded(cmd, 2*time.Second)
	if stopped.Load() {
		return Result{Text: strings.TrimSpace(acc.String()), Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
			fmt.Errorf("claude -p stopped after %s: %w", time.Since(start).Round(time.Second), ErrInterrupted)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (capped != nil && capped.Load()) {
		if partial := strings.TrimSpace(acc.String()); partial != "" {
			return Result{Text: partial, Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
				fmt.Errorf("claude -p did not finish within %s: %w", time.Since(start).Round(time.Second), ErrWorkerTimeout)
		}
		return Result{}, fmt.Errorf("claude -p did not finish within %s: %w", time.Since(start).Round(time.Second), ErrWorkerTimeout)
	}
	if haveFinal {
		if final.APIErrorStatus == http.StatusTooManyRequests ||
			(final.IsError && (strings.Contains(strings.ToLower(final.Result), "usage limit") ||
				strings.Contains(strings.ToLower(final.Result), "session limit") ||
				strings.Contains(strings.ToLower(final.Result), "rate limit"))) {
			// Keep what was produced before the window closed (45 minutes of
			// frontier work were discarded here, 2026-09-10) and carry the
			// provider's words + reset time for the cooldown.
			if frontier {
				return partialResult(acc.String(), final.Result, start, onDelta), rateLimitedFrontier(final.Result)
			}
			return partialResult(acc.String(), final.Result, start, onDelta), rateLimited(LegClaude, final.Result)
		}
		if final.IsError {
			if transientProviderError(final.Result) {
				return Result{}, fmt.Errorf("claude: %w: %s", ErrProviderDown, truncateStr(final.Result, 160))
			}
			return Result{}, fmt.Errorf("claude error: %s", final.Result)
		}
		text := final.Result
		if text == "" {
			text = acc.String()
		}
		text = preferSubstantiveReport(text, longest)
		tokens := final.Usage.InputTokens + final.Usage.OutputTokens +
			final.Usage.CacheCreationInputTokens + final.Usage.CacheReadInputTokens
		return Result{Text: text, Tokens: tokens, CostUSD: final.TotalCostUSD,
			DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil}, nil
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			if transientProviderError(msg) {
				return Result{}, fmt.Errorf("claude -p: %w: %s", ErrProviderDown, truncateStr(msg, 160))
			}
			return Result{}, fmt.Errorf("claude -p: %w: %s", waitErr, truncateStr(msg, 300))
		}
		return Result{}, fmt.Errorf("claude -p: %w", waitErr)
	}
	return Result{Text: acc.String(), DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil}, nil
}

// runCursor runs a prompt through captain's Cursor wrapper (cursor-agent -p on
// the Cursor subscription). Text output; ~10-min cap for long jobs.
func runCursor(dir, task string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// --trust clears cursor-agent's workspace-trust prompt (headless -p can't
	// answer it), which otherwise fails with exit 1 in any untrusted dir. It
	// trusts the cwd without --force/--yolo's blanket command auto-run.
	cmd := exec.CommandContext(ctx, "cursor-agent", append([]string{"-p", "--output-format", "text"}, cursorPermissionArgs()...)...)
	cmd.Stdin = strings.NewReader(task)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("cursor-agent -p timed out")
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			if cursorUsageLimit(msg) {
				return "", rateLimited(LegCursor, msg)
			}
			return "", fmt.Errorf("cursor-agent -p: %w: %s", err, truncateStr(msg, 300))
		}
		return "", fmt.Errorf("cursor-agent -p: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CursorUsageLimit is cursorUsageLimit for callers outside the package.
func CursorUsageLimit(msg string) bool { return cursorUsageLimit(msg) }

// cursorUsageLimit recognizes an exhausted Cursor subscription on stderr.
func cursorUsageLimit(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "usage limit") || strings.Contains(m, "actionrequirederror") ||
		strings.Contains(m, "rate limit") || strings.Contains(m, "quota")
}

// runCursorStream runs cursor-agent in streaming mode, forwarding the assistant's
// answer text to onDelta as it arrives (thinking deltas are skipped - they stay
// off the answer stream). The terminal "result" event is authoritative and lets
// us map a rate-limited Cursor subscription to ErrRateLimited so the leg cools
// down instead of being retried forever.
func runCursorStream(dir, task string, base, ceil time.Duration, onDelta, onStatus func(string), steer *Steer) (Result, error) {
	start := time.Now() // DurationMs gates the grading loop - a zero duration silently exempted cursor from ALL scoring (13 unscored runs, 2026-07-25)
	ctx := context.Background()
	prog := &progress{}
	var capped *atomic.Bool
	if base > 0 {
		var cancel context.CancelFunc
		ctx, cancel, prog, capped = progressCtx(base, ceil)
		defer cancel()
	}
	ctx, stopped, detachStop := interruptible(ctx, steer, LegCursor) // /interrupt keeps what streamed
	defer detachStop()
	cmd := exec.CommandContext(ctx, "cursor-agent", append([]string{"-p", "--output-format", "stream-json"}, cursorPermissionArgs()...)...)
	cmd.Stdin = strings.NewReader(task)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("cursor-agent pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("cursor-agent start: %w", err)
	}
	scanDone := make(chan struct{})
	defer close(scanDone)
	go func() { // see runClaudeStreamOpts: keep the cap authoritative
		select {
		case <-ctx.Done():
			stdout.Close()
		case <-scanDone:
		}
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var acc, result strings.Builder
	streamedAny, isError := false, false
	for sc.Scan() {
		prog.touch() // progress stamp: any output line counts as activity
		var ev struct {
			Type     string          `json:"type"`
			Subtype  string          `json:"subtype"`
			Text     string          `json:"text"`
			IsError  bool            `json:"is_error"`
			Result   string          `json:"result"`
			ToolCall json.RawMessage `json:"tool_call"`
			Message  struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			if ev.Subtype == "delta" && ev.Text != "" { // incremental answer (long outputs)
				acc.WriteString(ev.Text)
				streamedAny = true
				if onDelta != nil {
					onDelta(ev.Text)
				}
			} else if !streamedAny { // full assistant message (short outputs coalesce)
				for _, c := range ev.Message.Content {
					if c.Type == "text" && c.Text != "" {
						acc.WriteString(c.Text)
						if onDelta != nil {
							onDelta(c.Text)
						}
					}
				}
			}
		case "tool_call":
			// A tool in flight is the run's reason to be silent.
			switch ev.Subtype {
			case "started":
				prog.toolStart()
			case "completed", "failed", "error":
				prog.toolEnd()
			}
			// Only the "started" edge is reported: completions would double every line.
			if ev.Subtype != "started" || onStatus == nil {
				continue
			}
			if s := cursorToolStatus(ev.ToolCall); s != "" {
				onStatus(s)
			}
		case "result":
			isError = ev.IsError
			result.WriteString(ev.Result)
		}
	}
	waitErr := waitBounded(cmd, 2*time.Second)
	if stopped.Load() {
		return Result{Text: strings.TrimSpace(acc.String()), Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
			fmt.Errorf("cursor-agent -p stopped after %s: %w", time.Since(start).Round(time.Second), ErrInterrupted)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (capped != nil && capped.Load()) {
		if partial := strings.TrimSpace(acc.String()); partial != "" {
			return Result{Text: partial, Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
				fmt.Errorf("cursor-agent -p timed out: %w", ErrWorkerTimeout)
		}
		return Result{}, fmt.Errorf("cursor-agent -p timed out: %w", ErrWorkerTimeout)
	}
	final := result.String()
	if final == "" {
		final = acc.String()
	}
	if isError {
		low := strings.ToLower(final)
		if strings.Contains(low, "rate") || strings.Contains(low, "quota") ||
			strings.Contains(low, "usage limit") || strings.Contains(low, "429") {
			return partialResult(acc.String(), final, start, onDelta), rateLimited(LegCursor, final)
		}
		if transientProviderError(final) {
			return Result{}, fmt.Errorf("cursor-agent: %w: %s", ErrProviderDown, truncateStr(final, 160))
		}
		return Result{}, fmt.Errorf("cursor-agent error: %s", truncateStr(final, 200))
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			if cursorUsageLimit(msg) {
				// The exhausted subscription arrives on STDERR with exit 1
				// ("ActionRequiredError: You've hit your usage limit"), not
				// as a result event - so it fell through to the generic
				// error and /cursor never rerouted (live 2026-09-13).
				return Result{}, rateLimited(LegCursor, msg)
			}
			if transientProviderError(msg) {
				return Result{}, fmt.Errorf("cursor-agent -p: %w: %s", ErrProviderDown, truncateStr(msg, 160))
			}
			if cursorAuthError(msg) {
				// A dead Cursor login: provider-down so the task reroutes and
				// the user still gets an answer; harnessFault keeps it off the
				// model's stats; the short cooldown lets `agent login` take
				// effect within minutes (live 2026-09-13).
				return Result{}, fmt.Errorf("cursor-agent -p: %w: not logged in - run `cursor-agent login`: %s", ErrProviderDown, truncateStr(msg, 160))
			}
			return Result{}, fmt.Errorf("cursor-agent -p: %w: %s", waitErr, truncateStr(msg, 300))
		}
		return Result{}, fmt.Errorf("cursor-agent -p: %w", waitErr)
	}
	return Result{Text: final, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil}, nil
}

// cursorAuthError recognizes a dead Cursor login on cursor-agent's stderr.
func cursorAuthError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "authentication required") || strings.Contains(m, "agent login") ||
		strings.Contains(m, "cursor_api_key") || strings.Contains(m, "not logged in")
}

// runClaudeTimeout bounds a claude -p call; timeout 0 means unbounded
// (workers doing long jobs). Director calls always pass a deadline so
// routing can never hang the CLI.
func runClaudeTimeout(dir, task string, timeout time.Duration) (Result, error) {
	start := time.Now()
	stop := make(chan struct{})
	go func() { // claude -p has no event bus; heartbeat so long runs aren't silent
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, "  … claude still working (%s)\n", time.Since(start).Round(time.Second))
			}
		}
	}()
	defer close(stop)
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	claudeArgs := append([]string{"-p", "--output-format", "json"}, claudePermissionArgs()...)
	if dir != "" { // a worker run gets the memory tools; the director (dir "") plans from the task
		claudeArgs = append(claudeArgs, ClaudeMCPArgs(dir)...)
		claudeArgs = append(claudeArgs, ClaudeDirArgs(dir)...)
	}
	claudeCmd := exec.CommandContext(ctx, "claude", claudeArgs...)
	claudeCmd.Stdin = strings.NewReader(task)
	claudeCmd.Dir = dir
	out, err := claudeCmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("claude -p did not finish within %s", timeout)
	}
	if err != nil && len(out) == 0 {
		return Result{}, fmt.Errorf("claude -p: %w", err)
	}
	var r struct {
		IsError        bool    `json:"is_error"`
		APIErrorStatus int     `json:"api_error_status"`
		Result         string  `json:"result"`
		TotalCostUSD   float64 `json:"total_cost_usd"`
		Usage          struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if jerr := json.Unmarshal(out, &r); jerr != nil {
		return Result{}, fmt.Errorf("parse claude output: %w", jerr)
	}
	if r.APIErrorStatus == http.StatusTooManyRequests ||
		(r.IsError && strings.Contains(strings.ToLower(r.Result), "usage limit")) {
		return Result{}, ErrRateLimited
	}
	if r.IsError {
		return Result{}, fmt.Errorf("claude error: %s", r.Result)
	}
	tokens := r.Usage.InputTokens + r.Usage.OutputTokens + r.Usage.CacheCreationInputTokens + r.Usage.CacheReadInputTokens
	return Result{Text: r.Result, Tokens: tokens, CostUSD: r.TotalCostUSD, DurationMs: time.Since(start).Milliseconds()}, nil
}

// partialResult packages the text a run produced before its provider cut it
// off (rate limit) as a partial deliverable. The provider's terminal message
// is stripped when the stream echoed it as text, so the answer does not end
// in "You've hit your session limit".
func partialResult(acc, terminal string, start time.Time, onDelta func(string)) Result {
	text := strings.TrimSpace(acc)
	if t := strings.TrimSpace(terminal); t != "" && strings.HasSuffix(text, t) {
		text = strings.TrimSpace(strings.TrimSuffix(text, t))
	}
	if text == "" {
		return Result{DurationMs: time.Since(start).Milliseconds()}
	}
	return Result{Text: text, Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil}
}

// preferSubstantiveReport returns the deliverable a claude -p session
// actually produced: when the final message is a short trailer (a reaction
// to a subagent notification, "done", …) and an earlier assistant message is
// substantially longer, that earlier message IS the report - return it with
// the trailer appended so nothing is lost.
func preferSubstantiveReport(final, longest string) string {
	f := strings.TrimSpace(final)
	if len(f) >= 800 || len(longest) < 2*len(f)+400 || longest == f {
		return final
	}
	if f == "" {
		return longest
	}
	return longest + "\n\n" + f
}
