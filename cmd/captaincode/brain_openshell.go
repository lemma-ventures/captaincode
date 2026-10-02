package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// openShellTaskEmpty reports a turn with no task besides its directives: a
// bare "/openshell" would otherwise send the directive itself to a paid run.
func openShellTaskEmpty(raw string) bool {
	rest := strings.TrimSpace(raw)
	for rest != "" {
		probe := rest + " " // a directive needs a separator, even at the end
		m := captainDirective.FindString(probe)
		if m == "" {
			return false
		}
		rest = strings.TrimSpace(probe[len(m):])
	}
	return true
}

var errOpenShellShutdown = errors.New("openshell: the brain is shutting down; retry after it restarts")

var errOpenShellEntry = errors.New("openshell runs only when requested explicitly: start the turn with /openshell or use captain with openshell")

func (b *brain) openShellChat(w http.ResponseWriter, r *http.Request, req oaiChatReq, prompt string) {
	_, budgetCheckCancel, budgetErr := captaincode.OpenShellBudgetContext(r.Context(), nil)
	if budgetErr != nil {
		writeErr(w, http.StatusBadRequest, budgetErr.Error())
		return
	}
	budgetCheckCancel()
	raw := lastUserRaw(req.Messages)
	if req.WorkflowID != "" {
		writeErr(w, http.StatusBadRequest, "openshell: cached workflows are unsupported; provide explicit /openshell stages")
		return
	}
	wf, workflowErr := captaincode.ParseOpenShellWorkflow(raw)
	if workflowErr != nil {
		writeErr(w, http.StatusBadRequest, workflowErr.Error())
		return
	}
	planned := openShellTeamTurn(req.Model, raw)
	if planned && len(teamRequired(raw)) != 1 {
		writeErr(w, http.StatusBadRequest, "openshell: a sandbox team cannot include host workers; name only /openshell after /team")
		return
	}
	if planned && len(wf.Stages) > 0 {
		writeErr(w, http.StatusBadRequest, "openshell: /team /openshell plans its own workers; drop /team to run typed /openshell stages")
		return
	}
	if openShellTaskEmpty(raw) || utf8.RuneCountInString(prompt) > 16384 || strings.ContainsRune(prompt, 0) {
		writeErr(w, http.StatusBadRequest, "openshell: provide a user task and at most 16384 characters of conversation; host compaction is disabled")
		return
	}
	dir := strings.TrimSpace(r.Header.Get(workspaceHeader))
	if dir == "" {
		dir = strings.TrimSpace(r.URL.Query().Get("cwd"))
	}
	if dir != "" {
		info, err := os.Stat(dir)
		if !filepath.IsAbs(dir) || err != nil || !info.IsDir() {
			writeErr(w, http.StatusBadRequest, "openshell: workspace must be an existing absolute directory")
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	key := fmt.Sprintf("openshell-http\x00%s\x00%x", req.ws.Dir, sha256.Sum256([]byte(raw+"\x00"+prompt)))
	b.wmu.Lock()
	if b.solo == nil {
		b.solo = map[string]*soloRun{}
	}
	cur, attached := b.solo[key]
	if !attached {
		cur = &soloRun{started: time.Now(), done: make(chan struct{}), err: errors.New("openshell: request did not complete")}
		b.solo[key] = cur
	}
	b.wmu.Unlock()
	if attached {
		// A streaming retry gets the heartbeats a long sandbox run needs while
		// it waits, or an idle timer somewhere in the stack cuts it before the
		// result exists. The raw writer leaves the running turn's /btw
		// announcements where they are.
		if req.Stream {
			emit, status, finish := newCompletionWriterRaw(w, req, string(captaincode.LegOpenShell))
			defer finish()
			status("OpenShell: the same request is already running; waiting for its result\n")
			select {
			case <-r.Context().Done():
				return
			case <-cur.done:
			}
			b.wmu.Lock()
			text, err := cur.text, cur.err
			b.wmu.Unlock()
			if err != nil {
				// Through emit, under the writer's lock: writeWorkerError would
				// write beside the keepalive goroutine.
				text = fmt.Sprintf("\n[captain] %s failed: %s\n", captaincode.LegOpenShell, err.Error())
			}
			emit(text)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-cur.done:
		}
		b.wmu.Lock()
		text, err := cur.text, cur.err
		b.wmu.Unlock()
		if err != nil {
			writeWorkerError(w, captaincode.LegOpenShell, err)
			return
		}
		emit, _, finish := newCompletionWriter(w, req, string(captaincode.LegOpenShell))
		emit(text)
		finish()
		return
	}
	defer func() {
		b.wmu.Lock()
		delete(b.solo, key)
		close(cur.done)
		b.wmu.Unlock()
	}()
	b.mu.Lock()
	if b.lifeContext().Err() != nil {
		b.mu.Unlock()
		b.recordSolo(key, "", errOpenShellShutdown)
		writeErr(w, http.StatusServiceUnavailable, errOpenShellShutdown.Error())
		return
	}
	taskID, attemptID, err := beginOpenShellSolo(b.ledger, lastUserTurn(prompt), b.processID)
	ctx := r.Context()
	cancel := func() {}
	budgetCancel := func() {}
	if err == nil {
		ctx, budgetCancel, err = captaincode.OpenShellBudgetContext(ctx, b.ledger.BudgetFor(taskID))
		if err == nil {
			ctx, cancel = b.cancelTree.Register(taskID, attemptID, "openshell", ctx)
			b.sandboxes.Add(1)
		} else {
			err = errors.Join(err, recordOpenShellSolo(b.ledger, taskID, attemptID, lastUserTurn(prompt), openShellNotDispatched(), err))
		}
	}
	b.mu.Unlock()
	defer cancel()
	if budgetCancel != nil {
		defer budgetCancel()
	}
	if err != nil {
		b.recordSolo(key, "", err)
		writeWorkerError(w, captaincode.LegOpenShell, err)
		return
	}
	defer b.sandboxes.Done()
	defer context.AfterFunc(b.lifeContext(), cancel)()
	ctx = captaincode.WithOpenShellCheckpoint(ctx, func(checkpoint captaincode.OpenShellCheckpoint) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		if err := b.ledger.RecordOpenShellCheckpoint(attemptID, checkpoint); err != nil {
			return err
		}
		return b.ledger.Save()
	})
	w.Header().Set("X-Captain-Task-ID", taskID)
	w.Header().Set("X-Captain-Attempt-ID", attemptID)
	stop := context.AfterFunc(ctx, func() { req.ws.Steer.Interrupt("OpenShell request cancelled") })
	defer stop()
	emit, status, finish := newCompletionWriter(w, req, string(captaincode.LegOpenShell))
	if req.Stream {
		defer finish()
		status("OpenShell: isolated execution; task " + taskID + "\n")
	}
	b.inflightRuns.Add(1)
	defer b.inflightRuns.Add(-1)
	b.active.begin(req.ws, captaincode.LegOpenShell, prompt)
	defer b.active.end(captaincode.LegOpenShell)
	req.ws.Steer.Began(captaincode.LegOpenShell)
	defer req.ws.Steer.Ended(captaincode.LegOpenShell)
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "run", Leg: "openshell", Model: "openshell", Text: promptPeek(lastUserTurn(prompt))})
	var res captaincode.Result
	if ctx.Err() != nil || !req.ws.Steer.Interrupted().IsZero() {
		err = captaincode.ErrInterrupted
		res = openShellNotDispatched()
	} else if planned {
		keep := func(plan captaincode.OpenShellPlanRecord) error {
			b.mu.Lock()
			defer b.mu.Unlock()
			if err := b.ledger.RecordOpenShellPlan(attemptID, plan); err != nil {
				return err
			}
			return b.ledger.Save()
		}
		res, err = b.runPlannedOpenShellTeam(ctx, req.ws, lastUserTurn(prompt), openShellHistory(req.Messages), status, keep)
	} else if len(wf.Stages) > 0 {
		res, err = b.runOpenShellWorkflow(ctx, req.ws, wf, openShellHistory(req.Messages))
	} else if b.runWorkerFn != nil {
		var leg captaincode.Leg
		leg, res, err = b.runWorkerFn(captaincode.LegOpenShell, prompt, nil, nil)
		if leg != captaincode.LegOpenShell {
			err = errors.New("openshell: refused a result from a host worker")
		}
	} else {
		res, err = req.ws.RunOpenShell(ctx, prompt)
	}
	b.mu.Lock()
	if ctx.Err() != nil || !req.ws.Steer.Interrupted().IsZero() {
		res.Export = nil
		res.Text = "OpenShell interrupted: no verified export delivered."
		err = captaincode.ErrInterrupted
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}
	if err == nil && res.Export == nil {
		err = errors.New("openshell: worker returned no verified export")
	}
	var saveErr error
	if b.openShellStoppedByRestart(attemptID, err) {
		err = fmt.Errorf("%w: the brain is restarting; after it starts, run captain task resume %s %s", err, taskID, attemptID)
		saveErr = b.ledger.Save()
	} else {
		saveErr = recordOpenShellSolo(b.ledger, taskID, attemptID, lastUserTurn(prompt), res, err)
	}
	b.mu.Unlock()
	if saveErr != nil {
		err = errors.Join(err, fmt.Errorf("openshell: save result: %w", saveErr))
	}
	if err != nil {
		b.recordSolo(key, "", err)
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: "openshell", Model: "openshell", Text: "error: " + err.Error(), Ms: res.DurationMs})
		writeWorkerError(w, captaincode.LegOpenShell, err)
		return
	}
	text := res.Text + "\ntask: " + taskID + "\n"
	b.recordSolo(key, text, nil)
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: "openshell", Model: "openshell", Text: "verified export (not applied)", Ms: res.DurationMs})
	turnSpend.add(req.ws.Steer, captaincode.LegOpenShell, res)
	emit(text)
	finish()
}

// openShellTeamTurn reports a "/team /openshell <task>" turn: a team whose
// named member is the sandbox. It runs on openShellChat, never on the host
// team path: the director only splits the task, and every worker is a
// sandbox.
func openShellTeamTurn(model, raw string) bool {
	if model != "team" && captaincode.LeadingForced(raw) != "team" {
		return false
	}
	return legInList(captaincode.LegOpenShell, teamRequired(raw))
}

// openShellHistory is the conversation before the last user turn, which
// sandbox workers receive ahead of their assignment.
func openShellHistory(msgs []oaiMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return promptFrom(msgs[:i])
		}
	}
	return ""
}

func (b *brain) runOpenShellWorkflow(ctx context.Context, ws captaincode.Workspace, wf captaincode.Workflow, history string) (captaincode.Result, error) {
	if b.runOpenShellWorkflowFn != nil {
		return b.runOpenShellWorkflowFn(ctx, ws, wf, history)
	}
	return ws.RunOpenShellWorkflow(ctx, wf, history)
}

// runPlannedOpenShellTeam has the tool-less director split task into sandbox
// assignments, then runs them as one parallel stage of /openshell workers.
// The planner's calls are director attempts: they come off the attempt cap
// before the team's own admission and settle with the task. keep saves the
// plan on the task before any sandbox starts; if it fails, none does.
func (b *brain) runPlannedOpenShellTeam(ctx context.Context, ws captaincode.Workspace, task, history string, status func(string), keep func(captaincode.OpenShellPlanRecord) error) (captaincode.Result, error) {
	planFn := captaincode.PlanOpenShellTeam
	if b.planOpenShellTeamFn != nil {
		planFn = b.planOpenShellTeamFn
	}
	status("OpenShell: the director is splitting the task into sandbox assignments\n")
	plan, err := planFn(ctx, ws.Dir, task, history)
	if err == nil {
		ctx, err = captaincode.WithOpenShellAttemptsSpent(ctx, plan.DirectorAttempts)
	}
	if err == nil {
		if err = keep(plan.Record()); err != nil {
			err = fmt.Errorf("openshell: save team plan: %w", err)
		}
	}
	if err != nil {
		return captaincode.Result{OpenShellAttempts: &captaincode.OpenShellAttemptUsage{Directors: plan.DirectorAttempts}}, err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "the director planned %d sandbox worker(s) in %d call(s)", len(plan.Assignments), plan.DirectorAttempts)
	if plan.Rationale != "" {
		sb.WriteString(" - " + terminalSafe(plan.Rationale, 200))
	}
	for i, brief := range plan.Assignments {
		fmt.Fprintf(&sb, "\n  w%d: %s", i+1, terminalSafe(strings.Join(strings.Fields(brief), " "), 200))
	}
	note := sb.String()
	status("OpenShell: " + note + "\n")
	res, err := b.runOpenShellWorkflow(ctx, ws, plan.Workflow, history)
	usage := captaincode.OpenShellAttemptUsage{Unmeasured: 1}
	if res.OpenShellAttempts != nil {
		usage = *res.OpenShellAttempts
	}
	usage.Directors += plan.DirectorAttempts
	res.OpenShellAttempts = &usage
	res.Text = "[captain/openshell] " + note + "\n\n" + res.Text
	return res, err
}
