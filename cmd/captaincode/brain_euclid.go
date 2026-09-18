package main

// Euclid in the brain (MM38 E1–E2): orientation into every worker prompt,
// a journal line per worker run, and distillation - `/euclid` in the TUI,
// `captain euclid` from the shell (a thin client of the endpoints below).
//
// Workers run stock opencode, so the `<euclid>` orientation the fork's core
// runner would render never reaches them; the brain renders it where the
// prompts are actually built (workerContext). Nothing here runs unless a
// brain exists (see pkg/captaincode/euclid.go - opt-in by construction).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// euclidCwd is the folder the CLI commands work in (the brain's default
// workspace); a request's own folder is req.ws.
func euclidCwd() string { return defaultWorkspace().Dir }

// clipToParagraphs keeps the opening of a deliverable, cut at a paragraph
// boundary, for the journal page's "Did" section.
func clipToParagraphs(text string, limit int) string {
	t := strings.TrimSpace(text)
	if len(t) <= limit {
		return t
	}
	cut := t[:limit]
	if i := strings.LastIndex(cut, "\n\n"); i > limit/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "\n\n…"
}

// noteFailure records an incident captain itself diagnosed into the write
// brain's FAILURES ledger - a stall, a cut-off, a round that deferred - so
// the next distillation can fold it into a guardrail and the director's
// memory block shows it at once. Provider outages and rate limits are NOT
// failures of the project: they stay in the run journal only.
func noteFailure(ws captaincode.Workspace, text string) {
	if _, err := captaincode.AppendNote(ws.Dir, "failure", text, "captain"); err != nil {
		fmt.Fprintf(os.Stderr, "captain brain: euclid failure note: %v\n", err)
	}
}

// journalReview records the director's grade of a worker run: the score,
// the verdict and the reviewer's notes - the only place its reasoning was
// kept before was the ledger's number (2026-09-16: "the director when
// grading should record some thoughts"). A poor verdict is also a failure
// note, so the guardrail reaches FAILURES (and the shared brain) the way a
// run captain had to end does.
func journalReview(ws captaincode.Workspace, worker captaincode.Leg, reviewer captaincode.Leg, task string, score float64, verdict, notes string) {
	notes = strings.TrimSpace(notes)
	e := captaincode.JournalEntry{Kind: "review", Task: lastUserTurn(task), Leg: string(worker), Outcome: verdict,
		Score: score, Verdict: verdict, Reviewer: string(reviewer), Summary: clipToParagraphs(notes, 800)}
	if _, err := captaincode.JournalRun(ws.Dir, e); err != nil {
		fmt.Fprintf(os.Stderr, "captain brain: euclid journal (review): %v\n", err)
	}
	if verdict == "poor" && notes != "" {
		if _, err := captaincode.AppendNote(ws.Dir, "failure", fmt.Sprintf("%s graded poor by the director (%s) on %q: %s",
			worker, reviewer, promptPeek(lastUserTurn(task)), notes), "director"); err != nil {
			fmt.Fprintf(os.Stderr, "captain brain: euclid failure note (review): %v\n", err)
		}
	}
}

// journalWorkerRun records one worker run into the write brain. Distillation
// and title-generator calls are Captain's own bookkeeping, never journaled.
func journalWorkerRun(ws captaincode.Workspace, leg captaincode.Leg, prompt string, res captaincode.Result, err error, kind string) {
	if isTitleRequest(prompt) || captaincode.IsDistillRequest(prompt) {
		return
	}
	outcome := "ok"
	switch {
	case err != nil && strings.TrimSpace(res.Text) != "" && captaincode.WorthKeeping(res, err):
		outcome = "partial"
	case err != nil:
		outcome = "failed"
	case res.Partial:
		outcome = "partial"
	}
	e := captaincode.JournalEntry{Kind: kind, Task: lastUserTurn(prompt), Leg: string(leg), Outcome: outcome,
		DurationMs: res.DurationMs, Files: captaincode.FilesFromWorkerLog(res.Log), Log: res.Log, Chars: len(res.Text),
		Tokens: res.Tokens, CostUSD: res.CostUSD, Summary: clipToParagraphs(res.Text, 1200)}
	if err != nil {
		e.Error = err.Error()
	}
	if _, jerr := captaincode.JournalRun(ws.Dir, e); jerr != nil {
		fmt.Fprintf(os.Stderr, "captain brain: euclid journal: %v\n", jerr)
	}
}

// euclidStatus describes the session's brains.
type euclidStatus struct {
	Cwd     string                    `json:"cwd"`
	Handle  string                    `json:"handle"`
	Brains  []captaincode.EuclidBrain `json:"brains"`
	Journal map[string]int            `json:"journal"` // label → entries since last distillation
	Orient  int                       `json:"orientation_chars"`
	Checks  []captaincode.BrainCheck  `json:"checks"` // the launch check: main brain, local brain
}

func (b *brain) euclidStatusNow(ws captaincode.Workspace) euclidStatus {
	cwd := ws.Dir
	st := euclidStatus{Cwd: cwd, Handle: captaincode.DeveloperHandle(), Brains: captaincode.ReadSet(cwd), Journal: map[string]int{}}
	for _, br := range st.Brains {
		if br.Writable {
			entries, _ := captaincode.ReadJournal(br, captaincode.LastDistilledAt(br))
			st.Journal[br.Label] = len(entries)
		}
	}
	st.Orient = len(captaincode.Orientation(cwd))
	st.Checks = captaincode.CheckBrains(cwd)
	return st
}

func renderEuclidStatus(st euclidStatus) string {
	var sb strings.Builder
	if len(st.Brains) == 0 {
		fmt.Fprintf(&sb, "Euclid: no brain for %s.\nRun `captain euclid init` (main brain in ~/.euclid) or `captain euclid init --repo` (repo brain + your developer subtree) to start remembering.\n", st.Cwd)
		sb.WriteString(captaincode.RenderBrainChecks(st.Checks))
		return sb.String()
	}
	fmt.Fprintf(&sb, "Euclid · handle %s · cwd %s\n", st.Handle, st.Cwd)
	sb.WriteString(captaincode.RenderBrainChecks(st.Checks))
	for _, br := range st.Brains {
		role := "read"
		if br.Writable {
			role = "WRITE"
		}
		line := fmt.Sprintf("  %-5s %-12s %s", role, br.Kind, br.Root)
		if n, ok := st.Journal[br.Label]; ok {
			line += fmt.Sprintf("  · %d runs journaled since last distill", n)
		}
		sb.WriteString(line + "\n")
	}
	fmt.Fprintf(&sb, "orientation injected into worker prompts: %d chars\n", st.Orient)
	sb.WriteString("`/euclid distill` folds the journal into your write brain; notes reach the shared repo brain as files under .euclid/notes/ and are folded on main (`captain euclid share`).\n")
	return sb.String()
}

// distillLeg is the leg that summarizes journals: cheap and fast.
// CAPTAIN_EUCLID_DISTILL_LEG overrides; default gemini when allowed, else free.
func (b *brain) distillLeg() captaincode.Leg {
	if v := captaincode.Leg(strings.TrimSpace(os.Getenv("CAPTAIN_EUCLID_DISTILL_LEG"))); v != "" && captaincode.KnownLeg(v) {
		return v
	}
	for _, l := range []captaincode.Leg{captaincode.LegGemini, captaincode.LegDeepSeek, captaincode.LegFree} {
		if len(b.allowed) == 0 || b.allowed[l] {
			return l
		}
	}
	return captaincode.LegFree
}

// distill runs one distillation for the write brain. apply=true writes the
// edits when the brain is a developer subtree or the main brain; the shared
// repo brain is never written here: it is folded on main from the promoted
// notes (euclid_share.go).
func (b *brain) distill(ws captaincode.Workspace, apply bool) (captaincode.Distillation, string, error) {
	cwd := ws.Dir
	wb, ok := captaincode.WriteBrain(cwd)
	if !ok {
		return captaincode.Distillation{}, "", fmt.Errorf("no write brain for %s - run `captain euclid init`", cwd)
	}
	since := captaincode.LastDistilledAt(wb)
	entries, err := captaincode.ReadJournal(wb, since)
	if err != nil {
		return captaincode.Distillation{}, "", err
	}
	if len(entries) == 0 {
		return captaincode.Distillation{Brain: wb, Since: since}, "nothing new in the journal since the last distillation", nil
	}
	prompt := captaincode.DistillPrompt(wb, entries)
	leg := b.distillLeg()
	var res captaincode.Result
	if b.runWorkerFn != nil {
		_, res, err = b.runWorkerFn(leg, prompt, nil, nil)
	} else {
		res, err = ws.RunWorkerStreamHooks(leg, prompt, opencodePort, nil, nil)
	}
	// Memory consolidation is a provider call on the user's quota, not a free
	// side effect of remembering: it gets its own task row (ROADMAP M1.2).
	if h := b.chargeOwnTask("memory: distill " + wb.Label); h != nil {
		h(leg, "distill", res, err)
	}
	if err != nil {
		return captaincode.Distillation{}, "", fmt.Errorf("distill on %s: %w", leg, err)
	}
	d, err := captaincode.ParseDistillation(res.Text)
	if err != nil {
		return captaincode.Distillation{}, "", err
	}
	d.Brain, d.Since, d.Entries = wb, since, len(entries)
	report := captaincode.RenderPatch(wb, d)
	if !apply {
		return d, report + "\n(dry run - `/euclid distill apply` or `captain euclid distill --apply` writes these to your write brain)\n", nil
	}
	touched, err := captaincode.ApplyEdits(wb, d.Edits)
	if err != nil {
		return d, report, err
	}
	if err := captaincode.MarkDistilled(wb, time.Now()); err != nil {
		return d, report, err
	}
	report += fmt.Sprintf("\napplied %d edits to %s (%s)\n", len(touched), wb.Label, wb.Root)
	if wb.Kind == "developer" {
		report += "your developer subtree is local (gitignored): edits stay on this machine.\n"
	}
	fmt.Printf("captain brain: euclid distilled %d journal entries → %d edits in %s\n", len(entries), len(touched), wb.Root)
	return d, report, nil
}

// handleEuclid intercepts the `/euclid` control word.
func (b *brain) handleEuclid(w http.ResponseWriter, req oaiChatReq, raw string) bool {
	t := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(t), "/euclid") {
		return false
	}
	args := strings.Fields(strings.TrimSpace(t[len("/euclid"):]))
	emit, _, finish := newCompletionWriter(w, req, "euclid")
	defer finish()
	cmd := "status"
	if len(args) > 0 {
		cmd = strings.ToLower(args[0])
	}
	switch cmd {
	case "status", "":
		emit(renderEuclidStatus(b.euclidStatusNow(req.ws)))
	case "distill":
		apply := len(args) > 1 && (args[1] == "apply" || args[1] == "--apply")
		_, report, err := b.distill(req.ws, apply)
		if err != nil {
			emit("[euclid] " + err.Error() + "\n")
		} else {
			emit(report)
		}
	default:
		emit("usage: /euclid [status|distill [apply]]\n")
	}
	finish()
	return true
}

// HTTP: GET /v1/euclid/status, POST /v1/euclid/distill {"apply":bool}.
func (b *brain) euclidStatusHTTP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, b.euclidStatusNow(workspaceOf(r)))
}

// euclidFoldHTTP answers a fold prompt (euclid_share_cmd.go) with the
// distill leg: the CLI folds the shared brain itself, the brain only lends
// it a model. POST /v1/euclid/fold {"prompt": "..."} → {"text": "..."}.
func (b *brain) euclidFoldHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt string `json:"prompt"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !captaincode.IsDistillRequest(req.Prompt) {
		writeErr(w, 400, "not a fold prompt")
		return
	}
	leg := b.distillLeg()
	ws := workspaceOf(r)
	var res captaincode.Result
	var err error
	if b.runWorkerFn != nil {
		_, res, err = b.runWorkerFn(leg, req.Prompt, nil, nil)
	} else {
		res, err = ws.RunWorkerStreamHooks(leg, req.Prompt, opencodePort, nil, nil)
	}
	if h := b.chargeOwnTask("memory: fold shared brain"); h != nil {
		h(leg, "distill", res, err)
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"leg": leg, "text": res.Text})
}

func (b *brain) euclidDistillHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Apply bool `json:"apply"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	d, report, err := b.distill(workspaceOf(r), req.Apply)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"distillation": d, "report": report})
}
