// captain - Captain Code: an agent scaffolding where the strongest model
// (Claude Fable, via the user's Max subscription) is the router MANAGER. It
// bootstraps each task into a worker brief, picks the worker model on live
// 3-axis scorecards (performance / quality / cost), assesses the result
// fairly, and maintains those stats locally.
// The deterministic ladder is the guardrail: trivial tasks skip the manager,
// and any manager failure falls back to heuristic routing.
//
// Captain reuses OpenCode's own scaffolding rather than reimplementing a chat
// UI: each leg gets a real, persistent OpenCode session per project directory
// (pkg/captaincode.ThreadRef) that survives across invocations - genuine
// cross-turn memory, not a fresh throwaway session every dispatch. Deep
// inspection (typing directly into a worker, scrolling history, switching
// sessions) is OpenCode's real TUI via `captain ui` (opencode attach) - never
// a custom reimplementation of it.
//
//	captain "fix the flaky auth test until tests pass"
//	captain --prefer save "add pagination to the runs list"
//	captain with grok "summarize what pkg/captaincode does"
//	captain                      # interactive REPL - type, get routed+assessed answers, type again
//	captain why | captain quota | captain stats | captain ui
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const (
	opencodePort    = 14096
	defaultCooldown = 30 * time.Minute
)

func main() {
	// The provider keys live in ~/.config/captain/env. A process that was not
	// given them already loads the file itself (envfile.go).
	captaincode.LoadCaptainEnv()
	if _, err := captaincode.LoadRegistry(""); err != nil {
		fmt.Fprintf(os.Stderr, "captain: legs.json: %v\n", err)
	}
	if _, err := captaincode.LoadPriorOverrides(); err != nil { // priors.json layers on the registry
		fmt.Fprintf(os.Stderr, "captain: priors.json: %v\n", err)
	}
	prefer := flag.String("prefer", "", "quality | speed | save")
	until := flag.String("until", "", "shell command; loop until it exits 0")
	maxIters := flag.Int("max-iters", 5, "max loop iterations")
	noManager := flag.Bool("no-manager", false, "skip the Fable router manager; pure heuristic ladder")
	flag.Parse()
	args := flag.Args()

	ledger, err := captaincode.LoadLedger()
	if err != nil {
		fatal(err)
	}

	// Director is configurable (default grok). CAPTAIN_DIRECTOR takes a comma
	// preference ladder ("claude,codex,grok"): the FIRST entry directs (and is
	// excluded from the worker ladder); later entries are who the brain demotes
	// to when the active director keeps failing (see directorLadderFromEnv).
	if d := os.Getenv("CAPTAIN_DIRECTOR"); d != "" {
		first := captaincode.Leg(strings.TrimSpace(strings.Split(d, ",")[0]))
		if _, isMode := captaincode.ParseDirectorMode(string(first)); isMode {
			// A policy word (frontier|quality|auto): the brain resolves it
			// against the ranking at start (restoreDirectorMode); the CLI's
			// own director stays the default until then.
		} else if !captaincode.KnownLeg(first) {
			fatal(fmt.Errorf("CAPTAIN_DIRECTOR=%q: first entry %q is neither a known leg (%s) nor a mode (frontier|quality|auto)", d, first, strings.Join(captaincode.LegIDs(), "|")))
		} else {
			captaincode.SetDirector(first)
		}
	} else {
		// No env override: start with a leg whose bin is present so direct
		// `captain "task"` and pre-brain manager calls do not name grok when
		// the user has no xAI/opencode setup.
		if d := captaincode.DefaultDirectorForMachine(); d != captaincode.Director {
			captaincode.SetDirector(d)
		}
	}

	if len(args) > 0 {
		switch args[0] {
		case "why":
			cmdWhy(ledger)
			return
		case "quota":
			cmdQuota(ledger)
			return
		case "budget":
			cmdBudget(ledger)
			return
		case "stats":
			cmdStats(ledger)
			return
		case "calibrate":
			cmdCalibrate(ledger, args[1:])
			return
		case "ui":
			cmdUI()
			return
		case "brain":
			cmdBrain(args[1:])
			return
		case "priors":
			cmdPriors(args[1:])
			return
		case "status":
			cmdStatus()
			return
		case "runs", "history":
			cmdRuns(args[1:])
			return
		case "show":
			cmdShow(args[1:])
			return
		case "refine":
			cmdRefine(args[1:], ledger)
			return
		case "upgrade":
			cmdUpgrade(args[1:])
			return
		case "kill":
			cmdKill(args[1:])
			return
		case "stop":
			cmdStop(args[1:])
			return
		case "redact":
			cmdRedact(args[1:])
			return
		case "gate": // the action gate at the tool boundary (gate_cmd.go)
			cmdGate(args[1:])
			return
		case "proxy": // standalone egress proxy (the brain runs one itself)
			startProxy()
			select {}
		case "init":
			cmdInit(args[1:])
			return
		case "doctor":
			cmdDoctor(args[1:])
			return
		case "eval":
			cmdEval(args[1:])
			return
		case "legs":
			cmdLegs(args[1:])
			return
		case "send":
			cmdSend(os.Args[2:])
			return
		case "adi":
			cmdADI(os.Args[2:])
			return
		case "jev":
			cmdJev(args[1:])
			return
		case "euclid":
			cmdEuclid(args[1:])
			return
		case "watch":
			cmdWatch()
			return
		case "state":
			cmdState(args[1:])
			return
		case "cancel":
			cmdCancel(args[1:])
			return
		case "lifecycle":
			cmdLifecycle(args[1:])
			return
		case "resume":
			cmdResume(args[1:])
			return
		case "handoff":
			cmdHandoff(args[1:])
			return
		case "policy":
			cmdPolicy(ledger, args[1:])
			return
		case "roles":
			cmdRoles(ledger)
			return
		case "task":
			cmdTask(args[1:])
			return
		case "director":
			cmdDirector(args[1:])
			return
		case "skills": // the vetted shelf (skills_cmd.go, M3.9)
			cmdSkills(args[1:])
			return
		case "outcomes":
			cmdOutcomes(args[1:])
			return
		case "outcome":
			cmdOutcome(args[1:])
			return
		case "host":
			cmdHost(args[1:])
			return
		case "release":
			cmdRelease(args[1:])
			return
		}
	}

	var forced captaincode.Leg
	if len(args) >= 3 && args[0] == "with" {
		forced = captaincode.Leg(args[1])
		args = args[2:]
	}

	if len(args) == 0 {
		if !stdoutIsTerminal() {
			fmt.Println("usage: captain [--prefer quality|speed|save] [--until <cmd>] [--no-manager] \"task\" | captain with <leg> \"task\" | captain why | captain quota | captain stats | captain ui")
			os.Exit(2)
		}
		repl(ledger, *prefer, *until, *maxIters, *noManager)
		return
	}

	task := strings.Join(args, " ")
	run(ledger, task, forced, *prefer, *until, *maxIters, *noManager)
}

// stdoutIsTerminal reports whether stdout is an interactive terminal (a
// character device) rather than a pipe/file - the stdlib idiom, more reliable
// across terminals than an isatty shim.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

// repl is captain's interactive chat loop: type a task, get a routed and
// assessed answer, type the next one - the same fundamental shape as
// OpenCode/Claude Code/Codex. Memory comes from real OpenCode sessions
// persisted per (leg, project directory) in the ledger, not from anything
// reimplemented here; `captain ui` opens OpenCode's own TUI on the same
// sessions for deep inspection.
func repl(ledger *captaincode.Ledger, prefer, until string, maxIters int, noManager bool) {
	fmt.Println("captain - interactive. Type a task and press Enter.")
	fmt.Println("  with <leg> <task>   force a leg (free|grok|codex|claude)")
	fmt.Println("  /new                forget this project's conversation threads")
	fmt.Println("  /why /quota /stats /calibrate  inspect routing state")
	fmt.Println("  /ui                 open the real OpenCode TUI on these sessions")
	fmt.Println("  Ctrl+D or /quit     exit")
	fmt.Println()

	cwd, _ := os.Getwd()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for {
		fmt.Print("captain> ")
		if !sc.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch line {
		case "/quit", "/exit":
			return
		case "/new", "/reset":
			ledger.ResetThreads(cwd)
			saveLedger(ledger)
			fmt.Println("captain: cleared this project's conversation threads - next dispatch starts fresh")
			continue
		case "/why":
			cmdWhy(ledger)
			continue
		case "/quota":
			cmdQuota(ledger)
			continue
		case "/stats":
			cmdStats(ledger)
			continue
		case "/calibrate":
			cmdCalibrate(ledger, nil)
			continue
		case "/roles":
			cmdRoles(ledger)
			continue
		case "/ui":
			cmdUI()
			continue
		}

		var forced captaincode.Leg
		task := line
		if fields := strings.SplitN(line, " ", 3); len(fields) == 3 && fields[0] == "with" {
			forced = captaincode.Leg(fields[1])
			task = fields[2]
		}
		run(ledger, task, forced, prefer, until, maxIters, noManager)
		fmt.Println()
	}
}

// captainNote mirrors captain's decision log into the "captain" opencode
// session so it shows as its own tab in `captain ui`. Best-effort.
func captainNote(ledger *captaincode.Ledger, format string, a ...any) {
	d := captaincode.NewDispatcher(opencodePort)
	d.Title = "captain"
	d.SessionID = ledger.Sessions["__captain__"]
	if err := d.Note(fmt.Sprintf(format, a...)); err == nil {
		ledger.Sessions["__captain__"] = d.SessionID
	}
}

func cmdUI() {
	d := captaincode.NewDispatcher(opencodePort)
	if err := d.EnsureServer(); err != nil {
		fatal(err)
	}
	cmd := exec.Command("opencode", "attach", d.BaseURL)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(fmt.Errorf("opencode attach: %w", err))
	}
}

func run(ledger *captaincode.Ledger, task string, forced captaincode.Leg, prefer, until string, maxIters int, noManager bool) {
	classHint := captaincode.Classify(task)
	class := classHint
	plan := captaincode.CompileLoop(task, until, maxIters)
	disp := captaincode.NewDispatcher(opencodePort)
	mgr := captaincode.Manager{Director: captaincode.Director, Port: opencodePort}
	cwd, _ := os.Getwd()
	// The terminal's folder is the workspace. Since the brain went
	// per-request (2026-09-12) an unset Dir means "no workspace", and an
	// opencode worker then ran in the umbrella folder (~/.captaincode/
	// workers) - `captain with glm "edit x"` edited nothing the user could
	// see, and the eval harness's economical arm rejected every run in 4s
	// (live 2026-09-15). claude -p only worked by inheriting the process cwd.
	disp.Dir = cwd

	iters := 1
	if plan.Until != "" {
		iters = plan.MaxIters
		fmt.Printf("captain: loop compiled - until `%s` (max %d iterations)\n", plan.Until, plan.MaxIters)
	}

	workerTask := plan.Task
	var lastLeg captaincode.Leg // tracks which leg disp's session belongs to; only re-resolve on a leg change

	for iter := 1; iter <= iters; iter++ {
		rung := captaincode.StartRung(class, prefer)
		order := captaincode.Pick(rung, ledger.Cooldowns, time.Now())
		managerOrder := captaincode.Pick(len(captaincode.Rungs)-1, ledger.Cooldowns, time.Now())
		if len(order) == 0 {
			fatal(errors.New("all legs cooling down - see `captain quota`"))
		}
		reason := fmt.Sprintf("class=%s prefer=%s rung=%d iter=%d ladder", class, orDash(prefer), rung, iter)

		// Manager routing: director assesses complexity + picks the worker.
		// Forced legs and --no-manager skip it; failures fall back to the
		// heuristic ladder (classHint). Director class is authoritative when
		// the plan succeeds (see ResolveRoute).
		if forced == "" && !noManager {
			allowFanOut := plan.Until == "" // loops stay single-worker
			fmt.Println("captain: manager planning…")
			// USER preference only - a leg name here anchors the director on the
			// ladder's first open leg (see brain.go route, 2026-07-19).
			// Empty class → director owns complexity assessment (hint is fallback only).
			p, err := mgr.Plan(workerTask, "", prefer, managerOrder, ledger.Stats(), ledger.TeamStats(), allowFanOut)
			rr := captaincode.ResolveRoute(workerTask, classHint, order, p, err)
			if rr.FanOut {
				runFanOut(ledger, mgr, task, rr.Class, rr.Plan)
				return
			}
			if rr.Managed {
				class, order, workerTask = rr.Class, rr.Order, rr.Brief
				reason = fmt.Sprintf("class=%s iter=%d %s", class, iter, rr.Reason)
			} else {
				warn("manager unavailable (%v) - heuristic ladder", err)
				class = classHint
			}
		}
		if forced != "" {
			order = []captaincode.Leg{forced}
		}

		var res captaincode.Result
		var leg captaincode.Leg
		var runErr error
		var ev captaincode.Event
		var failures []legFailure // every leg that refused this task, for one summary at the end
		disp.Live = true
		for _, leg = range order {
			if leg != lastLeg {
				resolveThread(ledger, disp, leg, cwd)
				lastLeg = leg
			}
			fmt.Printf("captain: → %s [%s] (%s)\n", leg, disp.Title, reason)
			captainNote(ledger, "**→ %s** `%s`\n\n%s", leg, disp.Title, reason)
			res, runErr = disp.Run(leg, workerTask)
			if errors.Is(runErr, captaincode.ErrSessionNotFound) {
				// The persisted thread points at a session the server no
				// longer has - self-heal: drop it and retry THIS leg once
				// with a brand-new session, instead of leaving it broken
				// until a manual /new or silently skipping to another leg.
				warn("%s's saved thread is gone server-side - starting a fresh one", leg)
				delete(ledger.Threads, captaincode.ThreadKey(leg, cwd))
				disp.SessionID = ""
				disp.Title = fmt.Sprintf("w-%s-%d", captaincode.ModelID(leg), ledger.NextWorkerSeq())
				res, runErr = disp.Run(leg, workerTask)
			}
			persistThread(ledger, disp, leg, cwd)
			ev = captaincode.Event{Task: truncate(task, 120), Class: class, Leg: leg, Reason: reason, Iter: iter,
				Tokens: res.Tokens, CostUSD: res.CostUSD, Duration: res.DurationMs}
			switch {
			case errors.Is(runErr, captaincode.ErrRateLimited):
				ev.Outcome = "rate_limited"
				ev.Error = runErr.Error()
				ledger.Record(ev)
				ledger.Cooldown(leg, defaultCooldown)
				failures = append(failures, legFailure{leg, runErr})
				fmt.Printf("captain: %s rate-limited - cooling down %s, trying next leg\n", leg, defaultCooldown)
				continue
			case runErr != nil:
				ev.Outcome = "fail"
				ev.Error = runErr.Error()
				ledger.Record(ev)
				failures = append(failures, legFailure{leg, runErr})
				// Bench it on the same policy the brain uses: a missing
				// credential or a dead login is not a fault that heals
				// between two turns, and Pick() skips a cooling leg.
				if d, why := benchPolicy(leg, runErr); d > 0 {
					ledger.Cooldown(leg, d)
					fmt.Printf("captain: %s benched %s (%s)\n", leg, d.Round(time.Second), why)
				}
				// One line per leg: a provider error blob is hundreds of
				// characters of JSON, and thirteen of them buried the one
				// sentence that mattered (2026-09-22).
				fmt.Printf("captain: %s failed (%s), trying next leg\n", leg, legReason(runErr))
				continue
			default:
				ev.Outcome = "ok"
			}
			break
		}
		if runErr != nil {
			saveLedger(ledger)
			fatal(ladderExhausted(failures, runErr))
		}

		if !res.Streamed {
			fmt.Println(res.Text)
		}

		objective := "none"
		done := plan.Until == ""
		if plan.Until != "" {
			if exec.Command("sh", "-c", plan.Until).Run() == nil {
				objective, done = "PASS: "+plan.Until, true
			} else {
				objective = "FAIL: " + plan.Until
			}
		}

		// Fair assessment: the manager scores the worker's output, anchored
		// by the objective check (objective evidence outranks impression).
		if !noManager && forced == "" {
			fmt.Println("captain: manager assessing…")
			if a, err := mgr.Assess(task, res.Text, objective); err == nil {
				ev.Quality, ev.Verdict = a.Quality, a.Verdict
				fmt.Printf("captain: assessed %s → %.1f/10 (%s) %s\n", leg, a.Quality, a.Verdict, a.Notes)
				captainNote(ledger, "**assessed %s** `%s` → %.1f/10 (%s) %s - objective: %s", leg, disp.Title, a.Quality, a.Verdict, a.Notes, objective)
			} else {
				warn("assessment failed (%v) - %s left unscored", err, leg)
			}
		}
		ledger.Record(ev)
		saveLedger(ledger)

		if done {
			if plan.Until != "" {
				fmt.Printf("captain: ✓ `%s` green after %d iteration(s)\n", plan.Until, iter)
			}
			return
		}
		if iter >= 2 && rung < len(captaincode.Rungs)-1 {
			rung++
			fmt.Printf("captain: not converging - escalating to %s\n", captaincode.Rungs[rung])
		}
		workerTask = task + "\n\nPrevious attempt did not satisfy: `" + plan.Until + "`. The check still fails - fix the remaining failures."
	}
	fatal(fmt.Errorf("loop hit max iterations (%d) without `%s` passing", maxIters, until))
}

// resolveThread loads a leg's persistent OpenCode session for cwd (if any)
// into disp, so the dispatch continues that leg's real conversation history
// instead of starting from zero - genuine cross-turn memory reused straight
// from OpenCode's own session storage. Falls back to a fresh, freshly
// numbered tab when no thread exists yet for this (leg, cwd) pair.
func resolveThread(ledger *captaincode.Ledger, disp *captaincode.OpencodeDispatcher, leg captaincode.Leg, cwd string) {
	if t, ok := ledger.Threads[captaincode.ThreadKey(leg, cwd)]; ok {
		disp.SessionID = t.SessionID
		disp.Title = t.Title
		return
	}
	disp.SessionID = ""
	disp.Title = fmt.Sprintf("w-%s-%d", captaincode.ModelID(leg), ledger.NextWorkerSeq())
}

// persistThread saves disp's current session as leg's thread for cwd, so the
// next dispatch to this leg (even in a later `captain` invocation) resumes it.
func persistThread(ledger *captaincode.Ledger, disp *captaincode.OpencodeDispatcher, leg captaincode.Leg, cwd string) {
	if disp.SessionID == "" {
		return
	}
	ledger.Threads[captaincode.ThreadKey(leg, cwd)] = captaincode.ThreadRef{SessionID: disp.SessionID, Title: disp.Title}
}

// runFanOut dispatches the manager's parallel workers concurrently - each on
// its own fresh opencode session (fan-out workers are one-shot subtasks, not
// persisted threads) - then has the manager score every worker and
// synthesize one answer in a single call.
func runFanOut(ledger *captaincode.Ledger, mgr captaincode.Manager, task string, class captaincode.Class, p captaincode.Plan) {
	fmt.Printf("captain: fan-out to %d workers (manager: %s)\n", len(p.Workers), p.Rationale)
	captainNote(ledger, "**fan-out** to %d workers - %s", len(p.Workers), p.Rationale)
	type outcome struct {
		title string
		w     captaincode.Worker
		res   captaincode.Result
		err   error
	}
	results := make([]outcome, len(p.Workers))
	var wg sync.WaitGroup
	for i, w := range p.Workers {
		title := fmt.Sprintf("w-%s-%d", captaincode.ModelID(w.Leg), ledger.NextWorkerSeq())
		fmt.Printf("captain: → %s [%s]\n", w.Leg, title)
		captainNote(ledger, "**→ %s** `%s` (parallel)", w.Leg, title)
		wg.Add(1)
		go func(i int, w captaincode.Worker, title string) {
			defer wg.Done()
			d := captaincode.NewDispatcher(opencodePort) // fresh session per worker
			d.Title = title
			res, err := d.Run(w.Leg, w.Brief)
			results[i] = outcome{title: title, w: w, res: res, err: err}
		}(i, w, title)
	}
	wg.Wait()

	// Keyed by title, NOT leg: the manager may assign the same leg to more
	// than one parallel worker (e.g. two independent "free" lookups), and a
	// leg-keyed map silently drops one worker's result when that happens.
	// Ensemble identity: which COMBINATION of models handled this task. Worker
	// events carry it, and one aggregate event (Team set, no Leg) becomes the
	// team's own scorecard entry - so routing can learn that a mix of models
	// beats any solo pick on some classes of task.
	teamLegs := make([]captaincode.Leg, 0, len(p.Workers))
	for _, w := range p.Workers {
		teamLegs = append(teamLegs, w.Leg)
	}
	teamKey := captaincode.TeamKey(teamLegs)

	outputs := map[string]captaincode.WorkerOutput{}
	events := map[string]*captaincode.Event{}
	for _, o := range results {
		ev := captaincode.Event{Task: truncate(task, 120), Class: class, Leg: o.w.Leg, Team: teamKey,
			Reason: "fan-out: " + p.Rationale, Tokens: o.res.Tokens, CostUSD: o.res.CostUSD, Duration: o.res.DurationMs}
		if o.err != nil {
			ev.Outcome = "fail"
			ev.Error = o.err.Error()
			if errors.Is(o.err, captaincode.ErrRateLimited) {
				ev.Outcome = "rate_limited"
				ledger.Cooldown(o.w.Leg, defaultCooldown)
			}
			fmt.Printf("captain: worker %s [%s] failed: %v\n", o.w.Leg, o.title, o.err)
			ledger.Record(ev)
			continue
		}
		ev.Outcome = "ok"
		outputs[o.title] = captaincode.WorkerOutput{Leg: o.w.Leg, Text: o.res.Text}
		events[o.title] = &ev
	}
	if len(outputs) == 0 {
		saveLedger(ledger)
		fatal(errors.New("all fan-out workers failed"))
	}

	if ma, err := mgr.AssessMulti(task, outputs, "none"); err == nil {
		// Ensemble-level aggregate: the team's grade is the mean of its scored
		// workers - one event with Team set and no Leg feeds TeamStats.
		if teamKey != "" && len(ma.Scores) > 0 {
			sum, n := 0.0, 0
			for _, sc := range ma.Scores {
				if sc.Quality > 0 {
					sum += sc.Quality
					n++
				}
			}
			if n > 0 {
				ledger.Record(captaincode.Event{Task: truncate(task, 120), Class: class, Team: teamKey,
					Reason: "team: " + p.Rationale, Outcome: "ok", Quality: sum / float64(n), Verdict: "team-mean"})
			}
		}
		fmt.Println(ma.Synthesis)
		for _, s := range ma.Scores {
			if ev, ok := events[s.Worker]; ok {
				ev.Quality, ev.Verdict = s.Quality, s.Verdict
			}
			fmt.Printf("captain: assessed %s → %.1f/10 (%s) %s\n", s.Worker, s.Quality, s.Verdict, s.Notes)
		}
	} else {
		warn("synthesis unavailable (%v) - raw outputs follow", err)
		for title, out := range outputs {
			fmt.Printf("--- %s (%s) ---\n%s\n", title, out.Leg, out.Text)
		}
	}
	for _, ev := range events {
		ledger.Record(*ev)
	}
	saveLedger(ledger)
}

func cmdWhy(l *captaincode.Ledger) {
	if len(l.Events) == 0 {
		fmt.Println("captain: no decisions recorded yet")
		return
	}
	e := l.Events[len(l.Events)-1]
	fmt.Printf("last: %q\n  class=%s leg=%s outcome=%s (%s)\n  quality=%.1f verdict=%s tokens=%d cost=$%.4f duration=%dms at=%s\n",
		e.Task, orDash(string(e.Class)), e.Leg, e.Outcome, e.Reason, e.Quality, orDash(e.Verdict), e.Tokens, e.CostUSD, e.Duration, e.At.Format(time.RFC3339))
	if e.Error != "" {
		fmt.Printf("  error: %s\n", e.Error)
	}
	printDecision(l, e.TaskID)
	printCharges(l, e.TaskID)
	printBudget(l, e.TaskID)
}

// printBudget shows the task's resource budget status when one exists.
func printBudget(l *captaincode.Ledger, taskID string) {
	b := l.BudgetFor(taskID)
	if b == nil {
		return
	}
	fmt.Printf("  budget: %s\n", b.Summary())
}

// printDecision shows the evidence behind the routing choice, not just its
// conclusion: the policy that ranked, the field with each candidate's terms
// and how much evidence stands behind its quality, and the legs that were
// excluded with the reason each was ruled out (ROADMAP M2.1).
func printDecision(l *captaincode.Ledger, taskID string) {
	d, ok := l.DecisionFor(taskID)
	if !ok {
		return
	}
	who := string(d.Chosen)
	if d.Shape == captaincode.ShapeTeam {
		who = "team " + joinLegs(d.Workers, "+")
	}
	fmt.Printf("  decision: %s → %s (%s/%s", d.Path, who, d.Class, orDash(string(d.Domain)))
	if d.NeedVision {
		fmt.Print(", needs vision")
	}
	fmt.Printf(") in %dms\n", d.DecidedMs)
	fmt.Printf("    policy: %s\n", d.Policy.Version)
	if d.Shadow != nil {
		fmt.Printf("    shadow: %s\n", captaincode.FormatShadow(*d.Shadow))
	}
	if d.Explored {
		fmt.Printf("    EXPLORE: passed over the top-ranked %s to earn %s a score\n", d.PassedOver, d.Chosen)
	}
	for i, c := range d.Considered() {
		mark := " "
		if c.Leg == d.Chosen {
			mark = "→"
		}
		fmt.Printf("    %s #%d %-8s value=%+.3f  q=%.1f %s  $%.4f  %s  %s\n",
			mark, i+1, c.Leg, c.Value, c.Quality, calibOf(c), c.CostUSD, latencyOf(c), freshnessOf(c))
	}
	for _, c := range d.Excluded() {
		fmt.Printf("      ✗ %-8s %s\n", c.Leg, c.Excluded)
	}
}

// evidenceOf says how much local evidence stands behind a quality estimate.
// A blended 8.2 from two scored runs and a blended 8.2 from forty are not the
// same claim, and printing only the number hides which one this is.
func evidenceOf(c captaincode.Scored) string {
	if c.ScoredRuns == 0 {
		return fmt.Sprintf("(prior %.1f, no local scores)", c.Prior)
	}
	return fmt.Sprintf("(prior %.1f + %d scored of %d runs)", c.Prior, c.ScoredRuns, c.Samples)
}

// calibOf renders the M5.2 calibrated estimate when the decision record
// carries one, falling back to the raw evidence string otherwise.
func calibOf(c captaincode.Scored) string {
	if c.Calibration.SampleSize > 0 || c.Calibration.Prior > 0 {
		return c.Calibration.Format()
	}
	return evidenceOf(c)
}

func latencyOf(c captaincode.Scored) string {
	if c.LatencyMs <= 0 {
		return "no observed duration"
	}
	return fmt.Sprintf("%.1fs observed", float64(c.LatencyMs)/1000)
}

// freshnessOf dates the evidence. A ranking built on a leg's numbers from
// three weeks ago is a different claim from one built on yesterday's.
func freshnessOf(c captaincode.Scored) string {
	out := ""
	if c.Pressure > 0 {
		out = fmt.Sprintf("pressure %.0f%% ", c.Pressure*100)
	}
	if c.Unreliable {
		out += "UNRELIABLE "
	}
	if c.LastRun.IsZero() {
		return out + "never run here"
	}
	return out + "last " + humanAgo(time.Since(c.LastRun))
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// printCharges shows what the whole turn cost, not just its last worker call:
// the task tree with one line per provider call, then the leaves-only roll-up
// with its coverage counts. An incomplete total is never presented as a bill
// (ROADMAP M1.2).
func printCharges(l *captaincode.Ledger, taskID string) {
	rows := captaincode.ChargeTree(l.Charges, taskID)
	if len(rows) == 0 {
		return
	}
	fmt.Println("  accounting:")
	for _, c := range rows {
		// A fan-out's stage row carries no money, but without it a team turn
		// reads as an unexplained list of workers.
		if c.Kind == captaincode.KindStage {
			fmt.Printf("    [%s]\n", c.Label)
			continue
		}
		if c.Kind != captaincode.KindCall {
			continue
		}
		fmt.Printf("    %-9s %-7s %6d tok  $%.4f %-9s %s\n",
			orDash(c.Label), orDash(string(c.Leg)), c.Usage.Total,
			c.Usage.CostUSD, c.Usage.CostStatus, orDash(c.Usage.PriceSource))
	}
	t := l.TaskTotals(taskID)
	fmt.Printf("    %s: $%.4f over %d calls, %d tok (measured=%d estimated=%d unknown=%d)\n",
		totalLabel(t), t.CostUSD, t.Calls, t.Tokens, t.Measured, t.Estimated, t.Unknown)
}

// totalLabel refuses to call a partial sum a bill.
func totalLabel(t captaincode.Totals) string {
	if t.Complete() {
		return "billed"
	}
	return "partial total"
}

func cmdBudget(l *captaincode.Ledger) {
	budgets := l.RecentBudgets()
	if len(budgets) == 0 {
		fmt.Println("captain: no task budgets recorded yet")
		fmt.Println("  set CAPTAIN_MAX_ATTEMPTS to activate the attempt cap (0 = unlimited, tracking only)")
		return
	}
	total, stopped, cost := l.BudgetCoverage()
	fmt.Printf("%d task budgets (%d stopped, $%.4f total settled cost)\n", total, stopped, cost)
	for _, b := range budgets {
		fmt.Print(captaincode.FormatBudget(b))
	}
}

func cmdQuota(l *captaincode.Ledger) {
	now := time.Now()
	today := 0
	perLeg := map[captaincode.Leg]int{}
	for _, e := range l.Events {
		if e.At.After(now.Add(-24 * time.Hour)) {
			today++
			perLeg[e.Leg] += e.Tokens
		}
	}
	fmt.Printf("last 24h: %d dispatches\n", today)
	fmt.Println(captaincode.QuotaSummary(l, now))
	for _, line := range captaincode.SortedQuotaLines(l, now) {
		fmt.Println(line)
	}
	fmt.Println()
	for _, leg := range captaincode.AllLegs {
		state := "open"
		if leg == captaincode.Director {
			state = "director (not in worker ladder unless forced)"
		}
		if until, ok := l.Cooldowns[leg]; ok && now.Before(until) {
			state = "cooling down until " + until.Format("15:04")
		}
		fmt.Printf("  %-7s %s, ~%d tokens (24h)\n", leg, state, perLeg[leg])
	}
}

func cmdStats(l *captaincode.Ledger) {
	stats := l.Stats()
	fmt.Printf("scorecards (ok-runs; quality blends the benchmark prior with manager-assessed 0-10, objective-anchored; director=%s):\n", captaincode.Director)
	for _, leg := range captaincode.AllLegs {
		s := stats[leg] // zero value when no runs yet - blended quality is then the pure prior
		local := "n/a"
		if s.Scored > 0 {
			local = fmt.Sprintf("%.1f", s.AvgQuality)
		}
		cost := ""
		if s.TotalCostUSD > 0 {
			cost = fmt.Sprintf(" cost=$%.4f", s.TotalCostUSD)
		}
		fmt.Printf("  %-7s quality=%.1f (prior=%.1f local=%-4s) n=%-3d scored=%-3d avg=%5dms avg_tokens=%d%s\n",
			leg, captaincode.BlendedQuality(leg, s), captaincode.QualityPrior(leg), local, s.N, s.Scored, s.AvgDurationMs, s.AvgTokens, cost)
	}
	// Accounting coverage across every recorded provider call - worker,
	// review and repair alike. M1 reports this BESIDE the total, because a
	// sum built from unknowns is not a measured cost (ROADMAP M1.2).
	if t := l.LedgerTotals(); t.Calls > 0 {
		fmt.Printf("accounting coverage: %d calls (measured=%d estimated=%d unknown=%d), %s $%.4f, %d tok\n",
			t.Calls, t.Measured, t.Estimated, t.Unknown, totalLabel(t), t.CostUSD, t.Tokens)
	}
	if bt, bs, bc := l.BudgetCoverage(); bt > 0 {
		fmt.Printf("budget coverage: %d tasks (%d stopped, $%.4f settled) - `captain budget` for detail\n", bt, bs, bc)
	}
}

func cmdRoles(l *captaincode.Ledger) {
	summaries := captaincode.RoleEconomics(l)
	fmt.Print(captaincode.FormatRoleEconomics(summaries))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return captaincode.CutHead(s, n) + "…"
}

// legFailure is one leg's refusal, kept so the ladder can report every cause
// at the end instead of only the last one.
type legFailure struct {
	leg captaincode.Leg
	err error
}

// legReason renders an error as one short line: provider errors arrive as
// multi-line JSON blobs, and printing them whole per leg is what turned a
// thirteen-leg ladder into a screen of noise (2026-09-22).
func legReason(err error) string {
	return truncate(strings.Join(strings.Fields(err.Error()), " "), 160)
}

// ladderExhausted reports a ladder that ran out of legs. The old message named
// the LAST error, but the ladder ends at the free tier, so the reason the user
// read was reliably the least informative one of the run - the credential and
// login faults that actually explained the failure had scrolled past. Name
// every leg, once, with its own cause.
func ladderExhausted(failures []legFailure, last error) error {
	if len(failures) <= 1 {
		return fmt.Errorf("all legs failed; last error: %w", last)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "all %d legs failed:", len(failures))
	for _, f := range failures {
		fmt.Fprintf(&b, "\n  %-10s %s", f.leg, legReason(f.err))
	}
	b.WriteString("\n\n`captain doctor` says which of these are missing a credential or a login.")
	return errors.New(b.String())
}

func saveLedger(l *captaincode.Ledger) {
	if err := l.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "captain: warn: save ledger: %v\n", err)
	}
}

func warn(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "captain: "+format+"\n", a...)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "captain: %v\n", err)
	os.Exit(1)
}
