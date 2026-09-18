package main

// The workflow SKILL, wired into the TUI without a fork change.
//
//	/wf grok analyses this file, then cursor reviews it, then codex and claude
//	    red-team it
//	→ compile (one director call with the skill injected)
//	→ print the expression + cost preview + a run line
//	/run wf_7fa2  → execute it
//
// The fork forwards every message verbatim, so both control words are handled
// here in the wrapper: nothing runs until the user sends the run line, and the
// expression is always printed so the plan can be edited instead of
// re-explained (spec §4.4/§4.5).

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// workflowControl recognizes the two control words at the start of a turn.
var (
	wfCompilePrefixes = []string{"/wf ", "/workflow "}
	wfRunPrefixes     = []string{"/run ", "/wfrun "}
)

// workflowIntent returns the English intent when the turn asks for a compile.
func workflowIntent(raw string) (string, bool) {
	t := strings.TrimSpace(raw)
	for _, p := range wfCompilePrefixes {
		if len(t) > len(p) && strings.EqualFold(t[:len(p)], p) {
			return strings.TrimSpace(t[len(p):]), true
		}
	}
	return "", false
}

// workflowRunID returns the workflow id when the turn asks to run one.
func workflowRunID(raw string) (string, bool) {
	t := strings.TrimSpace(raw)
	for _, p := range wfRunPrefixes {
		if len(t) > len(p) && strings.EqualFold(t[:len(p)], p) {
			id := strings.Fields(strings.TrimSpace(t[len(p):]))
			if len(id) > 0 && strings.HasPrefix(id[0], "wf_") {
				return id[0], true
			}
		}
	}
	return "", false
}

// compileWorkflow runs the skill: one director call, validated by re-parsing.
func (b *brain) compileWorkflow(intent, convo string) (captaincode.CompiledWorkflow, map[captaincode.Leg]captaincode.LegStats, error) {
	b.mu.Lock()
	open := b.filterAllowed(captaincode.Pick(len(captaincode.Rungs)-1, b.ledger.Cooldowns, time.Now()))
	stats := b.ledger.Stats()
	cooldowns := map[captaincode.Leg]time.Time{}
	for l, t := range b.ledger.Cooldowns {
		cooldowns[l] = t
	}
	mgr := captaincode.Manager{Director: b.effectiveDirector(), Port: b.mgr.Port}
	b.mu.Unlock()

	if b.compileFn != nil { // test seam
		c, err := b.compileFn(intent, convo, open, stats)
		return c, stats, err
	}
	c, err := mgr.CompileWorkflow(intent, convo, open, stats, cooldowns)
	return c, stats, err
}

// workflowPreview renders the confirmation block. It leads with cost, always
// prints the raw expression (so a plan can be edited, not re-explained), and
// ends with the exact line that executes it.
func workflowPreview(id string, c captaincode.CompiledWorkflow, stats map[captaincode.Leg]captaincode.LegStats) string {
	wf := c.Workflow
	low, high := captaincode.EstimateWorkflow(wf, stats, 0)
	var sb strings.Builder
	fmt.Fprintf(&sb, "workflow %s · %d stage%s · %d run%s + review · ~%s-%s · 1 output\n\n",
		id, len(wf.Stages), plural(len(wf.Stages)), wf.Runs(), plural(wf.Runs()),
		low.Round(time.Second), high.Round(time.Second))
	for i, st := range wf.Stages {
		for j, l := range st.Legs {
			tag := fmt.Sprintf("  %d ", i+1)
			if j > 0 {
				tag = "    "
			}
			assign := l.Prompt
			if assign == "" {
				assign = "(improve the previous output)"
			}
			mark := ""
			if len(st.Legs) > 1 {
				mark = "   ⟍ parallel"
				if j > 0 {
					mark = "   ⟋"
				}
			}
			fmt.Fprintf(&sb, "%s %-9s %s%s\n", tag, l.Leg, assign, mark)
		}
	}
	fmt.Fprintf(&sb, "  ✓  %-9s one aggregate, findings attributed\n", "review")
	if c.Rationale != "" {
		fmt.Fprintf(&sb, "\n  why: %s\n", c.Rationale)
	}
	for _, w := range c.Warnings {
		fmt.Fprintf(&sb, "  ⚠ %s\n", w)
	}
	fmt.Fprintf(&sb, "\n  /run %s   ·   or edit the expression below and send it\n  %s\n", id, c.Expression)
	return sb.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// handleWorkflowControl serves the compile and run turns. Reports whether the
// turn was a workflow control word (and therefore fully handled).
func (b *brain) handleWorkflowControl(w http.ResponseWriter, req oaiChatReq, prompt, raw string) bool {
	if !workflowEnabled() {
		return false
	}
	if id, ok := workflowRunID(raw); ok {
		e, found := b.takeWorkflow(id)
		if !found {
			emit, _, finish := newCompletionWriter(w, req, "workflow")
			defer finish()
			emit(fmt.Sprintf("workflow %s is unknown or expired (workflows keep for %s).\nCompile it again with `/wf <what you want>`, or type the expression directly.", id, workflowTTL))
			finish()
			return true
		}
		fmt.Printf("captain brain: workflow %s confirmed → executing\n", id)
		b.runWorkflow(w, req, prompt, e.wf, id)
		return true
	}
	// Saved-workflow subcommands (save/run/list) before English compile.
	if b.handleSavedWorkflow(w, req, prompt, raw) {
		return true
	}
	intent, ok := workflowIntent(raw)
	if !ok {
		return false
	}
	emit, status, finish := newCompletionWriter(w, req, "workflow")
	defer finish()
	feed := newProgressFeed("compiling the workflow", status)
	defer feed.close()
	feed.note("⚙ director is compiling your workflow\n")
	t0 := time.Now()
	c, stats, err := b.compileWorkflow(intent, prompt)
	feed.close()
	if err != nil {
		fmt.Printf("captain brain: workflow compile failed in %s - %v\n", time.Since(t0).Round(time.Millisecond), err)
		emit(fmt.Sprintf("could not compile that into a workflow: %v\n\nYou can always write it yourself, e.g.\n  /grok analyse @file > /cursor review it > /claude red-team it", err))
		finish()
		return true
	}
	id := b.storeWorkflow(c.Workflow, intent)
	fmt.Printf("captain brain: workflow %s compiled in %s - %s\n", id, time.Since(t0).Round(time.Millisecond), c.Expression)
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: "workflow", Model: c.Workflow.Key(),
		Text: fmt.Sprintf("%s compiled: %s", id, promptPeek(c.Expression))})
	emit(workflowPreview(id, c, stats))
	finish()
	return true
}
