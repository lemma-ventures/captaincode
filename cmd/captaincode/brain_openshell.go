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
	} else if len(wf.Stages) > 0 {
		var history string
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				history = promptFrom(req.Messages[:i])
				break
			}
		}
		if b.runOpenShellWorkflowFn != nil {
			res, err = b.runOpenShellWorkflowFn(ctx, req.ws, wf, history)
		} else {
			res, err = req.ws.RunOpenShellWorkflow(ctx, wf, history)
		}
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
	saveErr := recordOpenShellSolo(b.ledger, taskID, attemptID, lastUserTurn(prompt), res, err)
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
