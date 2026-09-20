// captain/team - hierarchical team execution inside the brain's wrapper.
//
// The director plans a worker tree at route time (fan-out allowed); the fork
// then calls the wrapper with model="team"; the brain executes every worker in
// PARALLEL - each brief standalone, each run protected by the stall watchdog
// and provider-down reroute - then one AssessMulti call grades all workers and
// synthesizes the final answer. Ledger gets per-worker events (Team set) plus
// the ensemble aggregate, so team scorecards accumulate from real TUI work.
//
// Built 2026-07-20 after "share tasks with your team of agents" reached a lone
// one-shot grok worker that could only stall: the sentence is now a feature.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// teamEnabled: CAPTAIN_TEAM=0 turns fan-out routing off (single workers only).
func teamEnabled() bool { return os.Getenv("CAPTAIN_TEAM") != "0" }

// storeTeamPlan caches a route-time fan-out plan for the wrapper, keyed by the
// exact task text, so the team turn doesn't pay a second director call.
func (b *brain) storeTeamPlan(task string, p captaincode.Plan) {
	b.tmu.Lock()
	if b.teamPlans == nil {
		b.teamPlans = map[string]captaincode.Plan{}
		b.teamPlanAt = map[string]time.Time{}
	}
	for k, at := range b.teamPlanAt { // opportunistic expiry
		if time.Since(at) > teamPlanTTL {
			delete(b.teamPlans, k)
			delete(b.teamPlanAt, k)
		}
	}
	b.teamPlans[task] = p
	b.teamPlanAt[task] = time.Now()
	b.tmu.Unlock()
}

// teamPlanTTL bounds how long a route-time plan stays executable. The wrapper
// call follows its route within seconds; anything older is a repeat of the same
// prompt text much later, and re-planning it is more honest than resurrecting a
// stale fan-out.
const teamPlanTTL = 10 * time.Minute

// hasTeamPlan reports whether a fan-out plan is waiting for this exact task,
// without consuming it.
func (b *brain) hasTeamPlan(task string) bool {
	b.tmu.Lock()
	defer b.tmu.Unlock()
	p, ok := b.teamPlans[task]
	return ok && len(p.Workers) > 1 && time.Since(b.teamPlanAt[task]) <= teamPlanTTL
}

// takeTeamPlan pops the cached plan for task (take-once).
func (b *brain) takeTeamPlan(task string) (captaincode.Plan, bool) {
	b.tmu.Lock()
	defer b.tmu.Unlock()
	p, ok := b.teamPlans[task]
	if ok {
		delete(b.teamPlans, task)
		fresh := time.Since(b.teamPlanAt[task]) <= teamPlanTTL
		delete(b.teamPlanAt, task)
		if !fresh {
			return captaincode.Plan{}, false
		}
	}
	return p, ok
}

// runWorker executes one team worker; stubbed in tests. The default carries
// the full resilience stack (stall watchdog, provider-down reroute).
func (b *brain) runWorker(ws captaincode.Workspace, leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
	return b.runWorkerRerouted(ws, leg, brief, onDelta, onStatus, "")
}

// teamWorkerPrompt gives a team worker what the solo path always gave it - the
// conversation - plus its scoped assignment. Briefs alone are not enough: the
// director writes them from the last user turn, so anything anchored in the
// conversation ("in my writing style", "the file we discussed", "fix that
// bug") was silently dropped and two workers cheerfully answered a question
// nobody asked (live 2026-07-29: "give me a simple sentence in my writing
// style" → generic sentences from models that had never seen the user write).
func (b *brain) teamWorkerPrompt(ws captaincode.Workspace, conversation, brief string, leg captaincode.Leg) string {
	// Compaction, not lossy windowing (2026-08-27): team workers now share the
	// solo path's summarize-the-overflow treatment.
	convo := b.fitPrompt(ws, leg, conversation, minInt(promptBudget(leg), 400_000))
	return convo + "\n\n[captain] You are ONE worker on a team answering the LAST user turn above." +
		"\nYour assignment: " + brief +
		"\nThe conversation above is authoritative: the user's own words, files, and style take precedence over any paraphrase in the assignment. Stay inside your assignment's scope; another worker covers the rest." +
		workerContext(ws) +
		deliverableContract
}

// skills, when the stage staged a shelf, adds M3.9's second question to the
// same call: of the procedures captain staged, which do these answers show
// being used, and were they worth their place.
func (b *brain) doAssessMulti(taskID, task string, outputs map[string]captaincode.WorkerOutput, objective string, skills ...captaincode.SkillRef) (captaincode.MultiAssessment, error) {
	if b.assessMultiFn != nil {
		return b.assessMultiFn(task, outputs, objective)
	}
	b.mu.Lock()
	mgr := captaincode.Manager{Director: b.effectiveDirector(), Port: b.mgr.Port, Skills: skills}
	b.mu.Unlock()
	// The synthesis is a real provider call on the team's behalf: bill it to
	// the team's task rather than letting coordination overhead vanish (M1.2).
	mgr.CallLabel, mgr.OnCall = "synthesis", b.chargeAux(taskID)
	return mgr.AssessMulti(task, outputs, objective)
}

// teamPlanFor returns the plan to execute: the route-time cache if present,
// otherwise a fresh director plan (fan-out allowed) - the wrapper can be
// called directly (user picks captain/team) or after a brain restart.
func (b *brain) teamPlanFor(ws captaincode.Workspace, task, prefer string, required []captaincode.Leg) (captaincode.Plan, error) {
	if p, ok := b.takeTeamPlan(task); ok && len(required) == 0 {
		return p, nil
	}
	b.mu.Lock()
	order := b.filterAllowed(captaincode.Pick(len(captaincode.Rungs)-1, b.ledger.Cooldowns, time.Now()))
	// Same widening as /v1/route: this path plans wrapper-side (forced /team, or
	// after a brain restart), and without it the director could not assign a leg
	// the user named - it substituted glm for claude (live 2026-07-30).
	order = b.widenForNamed(task, order)
	stats, teams := b.ledger.Stats(), b.ledger.TeamStats()
	b.mu.Unlock()
	// "/team /quality": the ensemble menu is BOUND to the best-rated legs, the
	// same two-layer binding /quality gives solo routes (2026-08-26).
	if prefer == "quality" {
		if top := captaincode.TopQuality(order, stats, 4); len(top) > 0 {
			order = top
		}
	}
	if captaincode.TaskNeedsVision(task) {
		if vo := captaincode.FilterVision(order); len(vo) > 0 {
			order = vo
		}
	}
	// "/team /oss", "/team /deterministic": the ensemble menu is the pool's
	// legs (pool.go); nothing in the pool → the full menu, and the feed says why.
	if pool := ws.Pool; !pool.Empty() {
		if po := captaincode.FilterPool(pool, order); len(po) > 0 {
			order = po
		} else {
			b.poolFallbackNote(ws.Dir, pool)
		}
	}
	// Required legs (a leg or /frontier named right after /team) head the
	// menu whatever the filters above did: /frontier is not a CAPTAIN_LEGS
	// leg and has no prior, so allowlist and TopQuality would both drop it -
	// and then the director could only fail (live 2026-09-09).
	for i := len(required) - 1; i >= 0; i-- {
		l := required[i]
		if !legInList(l, order) {
			order = append([]captaincode.Leg{l}, order...)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.planHints = b.valueHints(captaincode.ClassHigh, captaincode.TriageTask(task).Domain, order)
	defer func() { b.planHints = nil }()
	return b.planWith(ws, required, task, "", prefer, order, stats, teams, true)
}

// teamRequired extracts the legs named as directives right after /team -
// "/team /frontier X", "/team /codex-cli /claude X" - which bind the ensemble:
// the user chose a member, the director fills in the rest. Preferences
// (/quality …) are read separately by teamPrefer; the two mix freely.
func teamRequired(raw string) []captaincode.Leg {
	var out []captaincode.Leg
	for {
		m := captainDirective.FindString(raw)
		if m == "" {
			return out
		}
		word := strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(m), "/: ")))
		switch {
		case word == "frontier":
			out = appendLegOnce(out, captaincode.LegFrontier)
		case captaincode.KnownLeg(captaincode.Leg(word)):
			out = appendLegOnce(out, captaincode.Leg(word))
		}
		raw = strings.TrimSpace(raw[len(m):])
	}
}

func appendLegOnce(list []captaincode.Leg, l captaincode.Leg) []captaincode.Leg {
	if legInList(l, list) {
		return list
	}
	return append(list, l)
}

// teamPrefer extracts a preference from the raw turn's leading directives, so
// "/team /quality design X" (and "/team /speed …" etc.) binds the plan.
func teamPrefer(raw string) string {
	for {
		m := captainDirective.FindString(raw)
		if m == "" {
			return ""
		}
		switch strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(m), "/: "))) {
		case "quality", "q", "best":
			return "quality"
		case "speed", "fast":
			return "speed"
		case "save", "cheap":
			return "save"
		}
		raw = strings.TrimSpace(raw[len(m):])
	}
}

// teamChat serves a model="team" wrapper call, streaming progress markers and
// the final synthesis (or returning them as one JSON completion).
func (b *brain) teamChat(w http.ResponseWriter, req oaiChatReq, prompt string) {
	task := lastUserTurn(prompt)
	prefer := teamPrefer(lastUserRaw(req.Messages))
	required := teamRequired(lastUserRaw(req.Messages))
	t0 := time.Now()

	// A named SEQUENCE is a pipeline, and a team is one parallel stage: honor the
	// order via the workflow engine even when the user forced /team (live
	// 2026-07-30: "grok, codex and then claude" ran all three at once).
	if workflowEnabled() {
		if stages := captaincode.NamedStages(task); len(stages) > 1 {
			if wf, ok := b.workflowFromNamedStages(stages, task); ok {
				fmt.Printf("captain brain: /team asked for a named sequence → running workflow %s\n", wf.Key())
				b.runWorkflow(w, req, prompt, wf, "wf_named")
				return
			}
		}
	}
	emit, status, finish := newCompletionWriter(w, req, "team")
	defer finish() // idempotent: guarantees the keepalive goroutine stops

	plan, err := b.teamPlanFor(req.ws, task, prefer, required)
	if err != nil || len(plan.Workers) == 0 {
		if err == nil {
			err = fmt.Errorf("director returned no workers")
		}
		fmt.Printf("captain brain: team plan failed - %v\n", err)
		writeWorkerError(w, "team", err)
		return
	}

	// One-worker plan is not a team: run the single leg normally. Logged and
	// written to history like every other turn - a silent path made a
	// verification run untraceable (2026-09-09).
	if len(plan.Workers) == 1 {
		wk := plan.Workers[0]
		fmt.Printf("captain brain: team plan → single worker %s (%s) - %s\n", wk.Leg, captaincode.ModelID(wk.Leg), plan.Rationale)
		fmt.Printf("captain brain: %s wrapper running (team single worker)…\n", wk.Leg)
		feed := newProgressFeed(string(wk.Leg), status)
		req.ws.Steer.Describe(wk.Leg, wk.Brief) // a /btw is routed by the briefs (brain_btw.go)
		shelf := b.stockShelf(req.ws.Dir, task) // M3.9: one worker, the user's own directory
		defer shelf.Remove()
		leg, res, err := b.runWorker(req.ws, wk.Leg, b.teamWorkerPrompt(req.ws, prompt, wk.Brief, wk.Leg), nil, feed.note)
		feed.close()
		if r2, e2, note := b.salvagePartial(leg, res, err); note != "" {
			res, err = r2, e2
			res.Text = "[captain: " + note + "]\n\n" + res.Text
		}
		hist := runRecord{Kind: "team", Model: string(wk.Leg), Legs: []string{string(leg)}, Task: task,
			Output: res.Text, DurationMs: time.Since(t0).Milliseconds(), Logs: logPaths(res)}
		if err != nil {
			hist.Error = err.Error()
			recordRunHistory(hist)
			fmt.Printf("captain brain: team single worker %s failed in %s - %v\n", leg, time.Since(t0).Round(time.Millisecond), err)
			writeWorkerError(w, leg, err)
			return
		}
		recordRunHistory(hist)
		fmt.Printf("captain brain: team single worker %s done in %s (%d chars)\n", leg, time.Since(t0).Round(time.Millisecond), len(res.Text))
		go b.recordRun(leg, prompt, res, req.ws.Dir, shelf.Refs()...)
		emit(res.Text)
		finish()
		return
	}

	teamLegs := make([]captaincode.Leg, 0, len(plan.Workers))
	for _, wk := range plan.Workers {
		teamLegs = append(teamLegs, wk.Leg)
	}
	teamKey := captaincode.TeamKey(teamLegs)
	// One turn, one task: the workers are ATTEMPTS under a fan-out stage, not
	// one task each. The identity adopts what routing already spent on the
	// plan that chose this team (ROADMAP M1.2).
	taskID := b.openTask(task)
	req.ws.Steer.SetTask(taskID) // a note's shadow joins this task's outcome (brain_shadow.go)
	stageID := b.chargeStage(taskID, "team:"+teamKey)
	// M3.4: register the team's root context so cancellation cascades.
	teamCtx, teamCancel := b.cancelTree.Register(taskID, "team", "team:"+teamKey, context.Background())
	defer teamCancel()
	_ = teamCtx
	fmt.Printf("captain brain: team running %d workers (%s) - %s\n", len(plan.Workers), teamKey, plan.Rationale)
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "run", Leg: "team", Model: teamKey, Text: promptPeek(task)})
	emit(fmt.Sprintf("[captain/team] %d workers in parallel (%s) - %s\n\n", len(plan.Workers), teamKey, plan.Rationale))

	type outcome struct {
		title string
		wk    captaincode.Worker
		leg   captaincode.Leg // the leg that ACTUALLY ran (reroute may substitute)
		res   captaincode.Result
		err   error
	}
	results := make([]outcome, len(plan.Workers))
	// M3.1: isolate concurrent writers in git worktrees so parallel team
	// workers do not stomp each other's files. If isolation is not possible
	// the team serializes — slower, but safe.
	var wts []*captaincode.Worktree
	isolated := false
	if len(plan.Workers) > 1 {
		rev := captaincode.CurrentRevision(req.ws.Dir)
		if rev != "" {
			var werr error
			wts, werr = captaincode.IsolateWorkers(context.Background(), req.ws.Dir, rev, len(plan.Workers))
			if werr != nil {
				fmt.Printf("captain brain: team %s - worktree isolation failed (%v), serializing\n", teamKey, werr)
			} else {
				isolated = true
			}
		}
	}
	done := make(chan struct{})
	// M3.9: one shelf per worker directory. An isolated worker's shelf dies
	// with its worktree; a serialized stage shares the user's directory, so
	// the same shelf is staged once and taken back when the stage ends.
	var shelves []*captaincode.Shelf
	defer func() {
		for _, sh := range shelves {
			sh.Remove()
		}
	}()
	var shelfMu sync.Mutex
	runTeamSlot := func(i int, wk captaincode.Worker, title string) {
		t0 := time.Now()
		ws := req.ws
		if isolated && i < len(wts) && wts[i] != nil {
			ws = req.ws.At(wts[i].Dir)
		}
		if isolated {
			if sh := b.stockShelf(ws.Dir, task); sh != nil {
				shelfMu.Lock()
				shelves = append(shelves, sh)
				shelfMu.Unlock()
			}
		}
		// Per-worker tool activity, named - parallel workers interleave.
		ws.Steer.Describe(wk.Leg, wk.Brief) // a /btw is routed by the briefs (brain_btw.go)
		leg, res, err := b.runWorker(ws, wk.Leg, b.teamWorkerPrompt(ws, prompt, wk.Brief, wk.Leg), nil, func(s string) {
			status(fmt.Sprintf("[%s] %s\n", wk.Leg, s))
		})
		// Salvage a capped or rate-limited worker's output rather than
		// losing the stage.
		if r2, e2, note := b.salvagePartial(leg, res, err); note != "" {
			res, err = r2, e2
			res.Text = "[captain: " + note + "]\n\n" + res.Text
			emit(fmt.Sprintf("[captain/team] ⏱ %s - keeping %d chars of partial output\n", note, len(res.Text)))
		}
		results[i] = outcome{title: title, wk: wk, leg: leg, res: res, err: err}
		if err != nil {
			emit(fmt.Sprintf("[captain/team] ✗ %s (%s) failed: %s\n", leg, captaincode.ModelID(leg), promptPeek(err.Error())))
			b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: string(leg), Model: captaincode.ModelID(leg), Text: "team worker error: " + promptPeek(err.Error()), Ms: time.Since(t0).Milliseconds()})
		} else {
			emit(fmt.Sprintf("[captain/team] ✓ %s (%s) done (%d chars)\n", leg, captaincode.ModelID(leg), len(res.Text)))
			b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: string(leg), Model: captaincode.ModelID(leg), Text: "team worker: " + promptPeek(res.Text), Ms: time.Since(t0).Milliseconds()})
		}
	}
	for i, wk := range plan.Workers {
		title := fmt.Sprintf("w%d-%s", i+1, wk.Leg)
		emit(fmt.Sprintf("[captain/team] → %s (%s): %s\n", wk.Leg, captaincode.ModelID(wk.Leg), promptPeek(wk.Brief)))
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "run", Leg: string(wk.Leg), Model: captaincode.ModelID(wk.Leg), Effort: string(req.ws.Effort), Text: "team worker: " + promptPeek(wk.Brief)})
		_ = title
	}
	if isolated {
		var wg sync.WaitGroup
		for i, wk := range plan.Workers {
			title := fmt.Sprintf("w%d-%s", i+1, wk.Leg)
			wg.Add(1)
			go func(i int, wk captaincode.Worker, title string) {
				defer wg.Done()
				runTeamSlot(i, wk, title)
			}(i, wk, title)
		}
		go func() { wg.Wait(); close(done) }()
		hb := time.NewTicker(30 * time.Second)
		defer hb.Stop()
	waiting:
		for {
			select {
			case <-done:
				break waiting
			case <-hb.C:
				emit(fmt.Sprintf("[captain/team] … still working (%s)\n", time.Since(t0).Round(time.Second)))
			}
		}
	} else {
		if sh := b.stockShelf(req.ws.Dir, task); sh != nil {
			shelves = append(shelves, sh)
		}
		for i, wk := range plan.Workers {
			title := fmt.Sprintf("w%d-%s", i+1, wk.Leg)
			runTeamSlot(i, wk, title)
		}
	}
	captaincode.CloseAll(wts)

	outputs := map[string]captaincode.WorkerOutput{}
	events := map[string]*captaincode.Event{}
	for _, o := range results {
		ev := captaincode.Event{Task: truncate(task, 120), Class: plan.Class, Leg: o.leg, Team: teamKey,
			Reason: "team: " + plan.Rationale, Tokens: o.res.Tokens, CostUSD: o.res.CostUSD, Duration: o.res.DurationMs}
		usage := captaincode.CallUsage(o.leg, o.res.Tokens, o.res.CostUSD, nil)
		ev.CostUSD, ev.CostStatus = usage.CostUSD, usage.CostStatus
		ev.TaskID = taskID
		ev.AttemptID = b.chargeMember(taskID, stageID, o.leg, "worker", o.res.DurationMs, usage)
		if o.err != nil {
			ev.Outcome = "fail"
			ev.Error = o.err.Error()
			b.mu.Lock()
			b.ledger.Record(ev)
			b.mu.Unlock()
			continue
		}
		ev.Outcome = "ok"
		outputs[o.title] = captaincode.WorkerOutput{Leg: o.leg, Text: o.res.Text}
		events[o.title] = &ev
	}
	if len(outputs) == 0 {
		b.completeWorkflowTask(taskID, captaincode.StateFailed)
		writeWorkerError(w, "team", fmt.Errorf("all %d team workers failed", len(plan.Workers)))
		return
	}

	final := ""
	stocked := teamShelfRefs(shelves)
	if ma, err := b.doAssessMulti(taskID, task, outputs, "none", stocked...); err == nil && strings.TrimSpace(ma.Synthesis) != "" {
		// A team is not a leg (the same distinction Event.Team draws), so the
		// rows carry the task and no leg rather than a leg that never ran.
		b.recordShelf(taskID, "", plan.Class, captaincode.TriageTask(task).Domain, skillRefNames(stocked), ma.Skills)
		final = ma.Synthesis
		sum, n := 0.0, 0
		b.mu.Lock()
		reviewer := b.effectiveDirector()
		b.mu.Unlock()
		for _, s := range ma.Scores {
			if ev, ok := events[s.Worker]; ok {
				ev.Quality, ev.Verdict = s.Quality, s.Verdict
				journalReview(req.ws, ev.Leg, reviewer, task, s.Quality, s.Verdict, s.Notes)
			}
			if s.Quality > 0 {
				sum += s.Quality
				n++
			}
		}
		if n > 0 {
			b.mu.Lock()
			b.ledger.Record(captaincode.Event{Task: truncate(task, 120), Class: plan.Class, Team: teamKey,
				Reason: "team: " + plan.Rationale, Outcome: "ok", Quality: sum / float64(n), Verdict: "team-mean"})
			b.mu.Unlock()
		}
	} else {
		// Grading is not availability: deliver the raw outputs.
		var sb strings.Builder
		sb.WriteString("[captain/team] synthesis unavailable - worker outputs follow.\n")
		for title, out := range outputs {
			fmt.Fprintf(&sb, "\n--- %s (%s) ---\n%s\n", title, out.Leg, out.Text)
		}
		final = sb.String()
	}
	b.mu.Lock()
	for _, ev := range events {
		b.ledger.Record(*ev)
	}
	saveErr := b.ledger.Save()
	b.mu.Unlock()
	if saveErr != nil {
		fmt.Fprintf(os.Stderr, "captain brain: save team events: %v\n", saveErr)
	}

	elapsed := time.Since(t0)
	hist := runRecord{Kind: "team", Model: teamKey, Task: task, Output: final, DurationMs: elapsed.Milliseconds()}
	for _, o := range results {
		w := workerRecord{Leg: string(o.leg), DurationMs: o.res.DurationMs, Text: o.res.Text, Log: o.res.Log}
		if o.err != nil {
			w.Error = o.err.Error()
		}
		hist.Legs = append(hist.Legs, string(o.leg))
		hist.Workers = append(hist.Workers, w)
	}
	recordRunHistory(hist)
	b.completeWorkflowTask(taskID, captaincode.StateSucceeded)
	fmt.Printf("captain brain: team done in %s (%d/%d workers ok, %d chars)\n",
		elapsed.Round(time.Millisecond), len(outputs), len(plan.Workers), len(final))
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: "team", Model: teamKey, Text: promptPeek(final), Ms: elapsed.Milliseconds()})
	emit("\n" + final)
	finish()
}

// frontierChat serves model="frontier": the claude leg at frontier settings
// (pinned strongest model version + maxed thinking budget, 2x time budget).
// Runs record under the claude leg - frontier IS claude at full effort.
func (b *brain) frontierChat(w http.ResponseWriter, req oaiChatReq, prompt string) {
	emit, status, finish := newCompletionWriter(w, req, "frontier")
	defer finish()
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "run", Leg: "claude", Model: "frontier", Effort: string(captaincode.EffortMax), Text: "frontier: " + promptPeek(lastUserTurn(prompt))})
	t0 := time.Now()
	// Frontier thinks for minutes before its first token - the progress feed is
	// the only thing standing between the user and an apparently dead turn.
	feed := newProgressFeed("frontier", status)
	defer feed.close()
	// Through the reroute net like every other leg: a rate-limited claude
	// cools down and the task moves to the next-best leg (codex-cli), the
	// way a /team /frontier plan already did. Calling the frontier runner
	// directly returned the 429 to the TUI, which retried four times in
	// twenty seconds and gave up (live 2026-09-12).
	ranLeg, res, err := b.runWorkerRerouted(req.ws, captaincode.LegFrontier, prompt, func(d string) { feed.touch(); emit(d) }, feed.note, "")
	feed.close()
	if err != nil {
		fmt.Printf("captain brain: frontier error in %s - %v\n", time.Since(t0).Round(time.Millisecond), err)
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: string(ranLeg), Model: "frontier", Text: "error: " + promptPeek(err.Error()), Ms: time.Since(t0).Milliseconds()})
		writeWorkerError(w, ranLeg, err)
		return
	}
	recordRunHistory(runRecord{Kind: "frontier", Model: "frontier", Legs: []string{string(ranLeg)},
		Task: lastUserTurn(prompt), Output: res.Text, DurationMs: time.Since(t0).Milliseconds()})
	fmt.Printf("captain brain: frontier done on %s in %s (%d chars)\n", ranLeg, time.Since(t0).Round(time.Millisecond), len(res.Text))
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: string(ranLeg), Model: "frontier", Text: promptPeek(res.Text), Ms: time.Since(t0).Milliseconds()})
	go b.recordRun(ranLeg, prompt, res, req.ws.Dir)
	if !req.Stream || !res.Streamed {
		emit(res.Text)
	}
	finish()
}

// sseKeepaliveEvery is how often quiet streams emit an SSE comment heartbeat
// (var for tests).
var sseKeepaliveEvery = 15 * time.Second

// newCompletionWriter abstracts "send text to the client" over both wrapper
// modes: SSE chunks when stream=true, one buffered JSON completion otherwise.
// status carries live "what is it doing" lines on the reasoning channel - the
// TUI renders them, the deliverable never contains them (buffered mode drops
// them: a non-streaming client only wants the answer).
func newCompletionWriter(w http.ResponseWriter, req oaiChatReq, model string) (emit func(string), status func(string), finish func()) {
	emit, status, finish = newCompletionWriterRaw(w, req, model)
	// A /btw taken mid-run is shown IN the turn, whichever path streams it
	// (solo, frontier, team, workflow): the turn's Steer writes through emit.
	req.ws.Steer.SetAnnounce(emit)
	return emit, status, finish
}

// turnUsage accumulates what a multi-worker turn spent (team, workflow,
// frontier): every run's tokens and cost, reported to the TUI in the
// completion's usage block so its Context panel moves (brain_usage.go).
// Keyed by the turn's Steer, which every worker run of the turn shares.
type turnUsage struct {
	mu    sync.Mutex
	spent map[*captaincode.Steer]map[string]any
}

var turnSpend = &turnUsage{spent: map[*captaincode.Steer]map[string]any{}}

// add folds one worker run into the turn's usage.
func (t *turnUsage) add(s *captaincode.Steer, leg captaincode.Leg, res captaincode.Result) {
	if s == nil {
		return
	}
	u := usageBlock(leg, res)
	t.mu.Lock()
	defer t.mu.Unlock()
	cur := t.spent[s]
	if cur == nil {
		t.spent[s] = u
		return
	}
	for _, k := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		cur[k] = cur[k].(int) + u[k].(int)
	}
	cur["cost"] = cur["cost"].(float64) + u["cost"].(float64)
	cur["captain_cost_usd"] = cur["cost"]
}

// take returns the turn's usage and forgets it.
func (t *turnUsage) take(s *captaincode.Steer) map[string]any {
	if s == nil {
		return zeroUsage()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.spent[s]
	delete(t.spent, s)
	if u == nil {
		return zeroUsage()
	}
	return u
}

func newCompletionWriterRaw(w http.ResponseWriter, req oaiChatReq, model string) (emit func(string), status func(string), finish func()) {
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	if !req.Stream {
		var bmu sync.Mutex // workers emit progress concurrently
		var buf strings.Builder
		return func(s string) { bmu.Lock(); buf.WriteString(s); bmu.Unlock() }, func(string) {}, sync.OnceFunc(func() {
			writeJSON(w, 200, map[string]any{
				"id": id, "object": "chat.completion", "created": created, "model": model,
				"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
					"message": map[string]any{"role": "assistant", "content": buf.String()}}},
				"usage": turnSpend.take(req.ws.Steer),
			})
		})
	}
	flush, _ := w.(http.Flusher)
	var mu sync.Mutex
	wroteHeader, first := false, true
	closed := false // after teardown NOTHING may touch w: writing to a
	// ResponseWriter whose handler has returned panics inside net/http and takes
	// the whole brain down with it (live 2026-07-31: SIGSEGV in the keepalive
	// goroutine killed the process and every in-flight run with it).
	writeHeaderLocked := func() {
		if !wroteHeader {
			wroteHeader = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(200)
		}
	}
	// Keepalive heartbeats: extended thinking and long tool phases stream no
	// text for minutes; a silent SSE connection gets idle-killed somewhere in
	// the stack, the TUI spinner dies, and the worker's result is discarded
	// (live 2026-07-25). SSE comment lines are ignored by parsers but reset
	// every idle timer - the wheel keeps spinning as long as work runs.
	stopKA := make(chan struct{})
	go func() {
		// Last line of defense (2026-08-28): a handler path that misses
		// finish() leaves this goroutine flushing a dead ResponseWriter -
		// that panic killed the WHOLE BRAIN twice (07-31, 08-28). A dead
		// heartbeat is harmless; a dead process takes every in-flight run.
		defer func() { recover() }()
		t := time.NewTicker(sseKeepaliveEvery)
		defer t.Stop()
		for {
			select {
			case <-stopKA:
				return
			case <-t.C:
				mu.Lock()
				if !closed {
					writeHeaderLocked()
					fmt.Fprint(w, ": keepalive\n\n")
					if flush != nil {
						flush.Flush()
					}
				}
				mu.Unlock()
			}
		}
	}()
	chunk := func(delta map[string]any, fin any) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		writeHeaderLocked()
		obj := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}},
		}
		if fin != nil {
			obj["usage"] = turnSpend.take(req.ws.Steer) // the AI SDK reads usage off the finishing chunk
		}
		payload, _ := json.Marshal(obj)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flush != nil {
			flush.Flush()
		}
	}
	return func(s string) {
			if s == "" {
				return
			}
			if first {
				first = false
				chunk(map[string]any{"role": "assistant", "content": s}, nil)
				return
			}
			chunk(map[string]any{"content": s}, nil)
		}, func(s string) {
			if s == "" {
				return
			}
			chunk(map[string]any{"reasoning_content": s}, nil)
		}, sync.OnceFunc(func() {
			// Idempotent so every handler can `defer finish()` AND call it
			// explicitly: the deferred call is what guarantees the keepalive
			// goroutine dies on the error paths that return early.
			close(stopKA)
			chunk(map[string]any{}, "stop")
			mu.Lock()
			if !closed {
				fmt.Fprint(w, "data: [DONE]\n\n")
				if flush != nil {
					flush.Flush()
				}
			}
			closed = true
			mu.Unlock()
		})
}
