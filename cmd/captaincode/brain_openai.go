// OpenAI-compatible surface so the opencode fork can call captain's OWN model
// wrappers as if they were a normal provider. Today it serves the claude leg via
// captain's `claude -p` wrapper (Max subscription) - so claude runs through
// captain, not opencode's anthropic registry/API.
//
// Register a provider in opencode config pointing at http://127.0.0.1:14097/v1:
//
//	{ "provider": { "captain": {
//	    "npm": "@ai-sdk/openai-compatible",
//	    "name": "Captain",
//	    "options": { "baseURL": "http://127.0.0.1:14097/v1" },
//	    "models": { "claude": { "name": "Claude (captain wrapper)" } } } } }
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type oaiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string OR array of {type,text}
}

type oaiChatReq struct {
	Model    string       `json:"model"`
	Stream   bool         `json:"stream"`
	Messages []oaiMessage `json:"messages"`
	// WorkflowID runs a previously compiled + confirmed workflow (CWL).
	WorkflowID string `json:"workflow_id,omitempty"`
	// ws is the folder the calling TUI is open in (see brain_workspace.go);
	// resolved from the request, never from the body.
	ws captaincode.Workspace
}

// lastUserRaw returns the last user turn exactly as typed - before directive
// stripping - which is where workflow syntax has to be detected.
func lastUserRaw(msgs []oaiMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" || msgs[i].Role == "" {
			return strings.TrimSpace(messageText(msgs[i].Content))
		}
	}
	return ""
}

const titleMarker = captaincode.TitleMarker

// isTitleTurn spots opencode's session-title call from the request itself:
// its SYSTEM message carries the marker (or, for an older client, its last
// user turn does). Only this request's own framing counts. Scanning the whole
// conversation was the bug: a session that had inherited a worker transcript
// (a `-c` that resumed a worker before the umbrella fix) carried the words
// in an old user message forever, so EVERY prompt typed in it - "/frontier",
// "/codex-cli" included - came back as a six-word title from the free leg
// (live 2026-09-12).
func isTitleTurn(msgs []oaiMessage) bool {
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(messageText(m.Content), titleMarker) {
			return true
		}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" || msgs[i].Role == "" {
			return strings.Contains(messageText(msgs[i].Content), titleMarker)
		}
	}
	return false
}

// isTitleRequest is the flattened-prompt view of the same question (the
// brain's rewrite, or the marker in the request's leading [system] block).
func isTitleRequest(prompt string) bool { return captaincode.IsTitlePrompt(prompt) }

// autoFallbackLeg answers "auto" when routing itself fails (every leg cooling,
// no eligible leg). CAPTAIN_FALLBACK_LEG overrides; claude is the last resort
// because a failed route usually means the cheap ladder is exhausted.
func (b *brain) autoFallbackLeg() captaincode.Leg {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_FALLBACK_LEG")); v != "" {
		if l := captaincode.Leg(v); captaincode.KnownLeg(l) {
			return l
		}
	}
	return captaincode.LegClaude
}

// titlePrompt is the whole prompt a title request gets. The text is the user's
// message, which is usually an imperative ("fix the flaky test"), so the
// instruction has to be explicit that the job is to NAME it, not do it.
func titlePrompt(text string) string {
	return captaincode.TitleRewritePrefix + " (3 to 6 words) for a conversation that starts with the message below. " +
		"Do not carry out the request, do not use tools, do not explain.\n\nMessage:\n" + truncate(text, 400)
}

// titleLeg is the cheapest runnable leg for naming a session.
func (b *brain) titleLeg() captaincode.Leg {
	for _, l := range []captaincode.Leg{captaincode.LegFree, captaincode.LegGrok, captaincode.LegCodex} {
		if len(b.allowed) == 0 || b.allowed[l] {
			return l
		}
	}
	return captaincode.LegFree
}

// promptPeek collapses a prompt/output to a single tidy line for the activity
// feed (the sidebar shows this as the worker's "inside" preview).
func promptPeek(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = captaincode.CutHead(s, 160) + "…"
	}
	return s
}

// messageText flattens OpenAI content (string or content-part array) to text.
// lastUserIndex is the index of the last user message, -1 when none.
func lastUserIndex(msgs []oaiMessage) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}

func messageText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b []byte
		for _, p := range parts {
			if p.Type == "text" || p.Text != "" {
				b = append(b, p.Text...)
			}
		}
		return string(b)
	}
	return ""
}

// captainDirective matches a leading captain TUI prefix - the preference
// prefixes (/quality, /speed, /save …) and the forced-leg prefixes (/claude,
// /team …). The fork parses them for its route call but sends the message on
// VERBATIM, so the worker used to receive "/quality rewrite this" and answer
// about the unknown slash command instead of doing the work (live
// 2026-07-29). They are routing metadata: strip them before dispatch.
//
// Generated from the leg roster (LegIDs) plus the pseudo-models: a hand-written
// list silently lagged the roster - deepseek/gemini/kimi were missing, so
// "/gemini do X" reached the worker with its prefix (found 2026-09-09).
var captainDirective = regexp.MustCompile(`(?i)^\s*/(` +
	strings.Join(longestFirst(append(append([]string{"quality", "q", "best", "speed", "fast", "save", "cheap", "team", "frontier", "btw", "interrupt"}, captaincode.PoolWords...), legIDs()...)), "|") +
	`)\b[\s:]+`)

// longestFirst orders alternation so a hyphenated name (codex-cli) is tried
// before its prefix (codex).
func longestFirst(words []string) []string {
	out := append([]string{}, words...)
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// legIDs is the brain's view of the leg roster (every model id it serves).
func legIDs() []string { return captaincode.LegIDs() }

// stripCaptainDirectives removes leading directives from one user turn. A turn
// that is ONLY a directive is left intact - there is no task to salvage, and
// the worker should see what the user actually typed.
//
// A turn that is a WORKFLOW expression is rendered as its plain instructions
// (`Workflow.PlainRequest`): a worker that reads "> /cursor review it" answers
// about a slash command instead of doing the work, and the topology is the
// executor's business, not the worker's.
func stripCaptainDirectives(text string) string {
	if captaincode.IsWorkflowExpr(text) {
		if wf, err := captaincode.ParseWorkflow(strings.TrimSpace(text)); err == nil {
			if plain := wf.PlainRequest(); plain != "" {
				return plain
			}
		}
	}
	out := text
	for {
		m := captainDirective.FindString(out)
		if m == "" {
			return out
		}
		rest := strings.TrimSpace(out[len(m):])
		if rest == "" {
			return out
		}
		out = rest
	}
}

// promptFrom builds a single claude -p prompt from the message list, keeping the
// system framing and the conversation turns.
func promptFrom(msgs []oaiMessage) string {
	var b []byte
	for _, m := range msgs {
		t := messageText(m.Content)
		if t == "" {
			continue
		}
		switch m.Role {
		case "system":
			b = append(b, "[system]\n"...)
		case "assistant":
			b = append(b, "[assistant]\n"...)
		default:
			b = append(b, "[user]\n"...)
			t = stripCaptainDirectives(t)
		}
		b = append(b, t...)
		b = append(b, "\n\n"...)
	}
	return string(b)
}

// promptBudget is the max prompt size (in chars) the wrapper hands a leg's
// worker. The wrapper replays the WHOLE conversation each turn, so a
// long-lived session otherwise grows past the worker model's context and
// every call fails with ContextOverflowError (live 2026-07-18: 193 msgs ≈
// 200k tokens vs codex). Budgets are conservative chars≈4·tokens estimates
// below each model's window, leaving room for output. Override all legs with
// CAPTAIN_WRAPPER_MAX_PROMPT (chars).
func promptBudget(leg captaincode.Leg) int {
	if v := os.Getenv("CAPTAIN_WRAPPER_MAX_PROMPT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if captaincode.IsFrontier(leg) {
		leg = captaincode.LegClaude
	}
	// From the registry's context window: ~3 chars per token, leaving room
	// for output, capped at 700k chars (compaction handles the rest).
	if s, ok := captaincode.Spec(leg); ok && s.Ctx > 0 {
		budget := s.Ctx * 3
		if budget > 700_000 {
			budget = 700_000
		}
		if budget < 300_000 {
			budget = 300_000
		}
		return budget
	}
	return 300_000
}

// replayBudget caps how much conversation a turn replays, by triaged class:
// a trivial edit keeps ~12k tokens of recent context, a medium task ~50k, and
// high-complexity work keeps the leg's full window. CAPTAIN_REPLAY_BUDGETS=0
// disables the scaling.
func replayBudget(c captaincode.Class) int {
	switch c {
	case captaincode.ClassTrivial:
		return 48_000
	case captaincode.ClassMedium:
		return 200_000
	}
	// High: 400k chars (~100k tokens) - compaction summarizes the overflow, so
	// a giant session stops shipping MBs to the worker every turn (2026-08-27).
	return 400_000
}

// windowPrompt fits prompt into budget chars by keeping the head (system
// framing) and the tail (the turns being answered) and eliding the middle
// with a visible marker. Under-budget prompts pass through untouched.
func windowPrompt(prompt string, budget int) string {
	if len(prompt) <= budget || budget <= 0 {
		return prompt
	}
	head := budget / 5
	marker := fmt.Sprintf("\n\n[captain: conversation truncated - %d chars elided to fit the model's context]\n\n", len(prompt)-budget)
	tail := budget - head
	// Rune-safe cuts: a byte cut inside "→" reached codex exec as invalid
	// UTF-8 and its CLI refused the whole run (2026-09-15).
	return captaincode.CutHead(prompt, head) + marker + captaincode.CutTail(prompt, tail)
}

// chatCompletions runs captain's claude wrapper and returns an OpenAI-compatible
// completion (SSE when stream=true - opencode's provider streams by default).
func (b *brain) chatCompletions(w http.ResponseWriter, r *http.Request) {
	// Track whether the response has been committed: an error raised after
	// the SSE keepalive/progress feed started must be spoken INTO the stream
	// (writeWorkerError), not written as a JSON body on top of it - that
	// produced "superfluous WriteHeader" and a silent, empty turn (live
	// 2026-09-09, a failed /team plan).
	w = trackWriter(w)
	var req oaiChatReq
	if !decode(w, r, &req) {
		return
	}
	req.ws = workspaceOf(r)
	// Several prompts queued behind the previous turn arrive as one request
	// with several trailing user messages: each is its own turn, in order
	// (brain_queue.go). A round the brain issued itself never splits.
	if r.Context().Value(noDedupeKey{}) == nil && r.Context().Value(queuedKey{}) == nil && !isTitleTurn(req.Messages) {
		if pending := queuedPrompts(req.Messages); pending != nil {
			b.runQueued(w, r, req, pending)
			return
		}
	}
	// Modifiers compose with control words in either order: "/oss /repeat 5
	// <task>" is "/repeat 5 /oss <task>" (hoist.go). Canonicalize the last
	// user turn before anything reads its head, and force the model the
	// plugin would have forced had the modifiers not stood in front of it.
	if i := lastUserIndex(req.Messages); i >= 0 {
		if raw0 := messageText(req.Messages[i].Content); raw0 != "" {
			if hoisted := captaincode.HoistLeading(raw0); hoisted != raw0 {
				req.Messages[i].Content, _ = json.Marshal(hoisted)
				// model=frontier is the plugin's reading of the same head; once
				// /frontier is hoisted past a leg, that leg is what runs.
				if (req.Model == "" || req.Model == "auto" || req.Model == "frontier") && !isTitleTurn(req.Messages) {
					if f := captaincode.LeadingForced(hoisted); f != "" {
						fmt.Printf("captain brain: modifiers hoisted → %s forced by the turn's head\n", f)
						req.Model = f
					}
				}
			}
		}
	}
	prompt := promptFrom(req.Messages)
	if prompt == "" {
		writeErr(w, 400, "no message content")
		return
	}
	// The turn's /btw handle, keyed by the folder the TUI is open in: a note
	// typed there while this turn runs reaches its workers (brain_btw.go).
	req.ws.Steer = b.steerOpen(req.ws.Dir)
	defer b.steerClose(req.ws.Steer)

	// The fork's title generator calls the SAME model with the user's text, so a
	// team/workflow turn fired TWICE - the real turn plus a title request, each
	// starting its own 3-worker audit (live 2026-07-30: two simultaneous runs of
	// the same workflow). Naming a session is a one-line job: cheapest leg, never
	// a team, a workflow or a frontier call.
	titleReq := isTitleTurn(req.Messages)
	if titleReq {
		leg := b.titleLeg()
		if req.Model != string(leg) {
			fmt.Printf("captain brain: title request → %s (no team/workflow/frontier)\n", leg)
		}
		req.Model = string(leg)
	}

	// The repository the task is about (reporefs.go): by default the folder
	// the TUI is open in; a task that names another repo runs there.
	if !titleReq {
		req.ws = b.followTask(req.ws, lastUserTurn(prompt))
	}
	// The request's effort (effort.go): what the user asked for, else the
	// task's difficulty rating. A route below refines the rating; a title
	// gets none (the transport's default, i.e. as little as it does).
	raw := lastUserRaw(req.Messages)
	if !titleReq {
		// Read the preference from the turn AS TYPED: promptFrom strips the
		// leading directives, so a "/quality …" at the head of the turn was
		// invisible here and to the route (only a mid-prompt one survived,
		// found 2026-09-17).
		prefer := captaincode.MidPromptPrefer(lastUserRaw(req.Messages))
		if req.Model == "frontier" || captaincode.MidPromptFrontier(lastUserRaw(req.Messages)) {
			prefer = "frontier"
		}
		req.ws.Effort = captaincode.EffortFor(prefer, captaincode.TriageTask(lastUserTurn(prompt)).Class)
		// /oss and /deterministic: which legs the turn may run on (pool.go).
		req.ws.Pool = captaincode.MidPromptPool(lastUserRaw(req.Messages))
	}

	// A typed workflow OUTRANKS the forced pseudo-model short-circuits: the
	// fork sends model=frontier for "/frontier draft > /codex review", but the
	// user wrote a topology (2026-08-25) - frontierChat/teamChat must not
	// swallow it. Only a CLEANLY PARSING multi-stage expression takes this
	// early exit; anything else falls through to the original dispatch (and
	// parse errors are reported by the LooksLikeWorkflow branch below).
	if workflowEnabled() && !titleReq && strings.HasPrefix(strings.TrimSpace(raw), "/") {
		if wf, err := captaincode.ParseWorkflow(raw); err == nil && (wf.MultiStage() || wf.HasGate()) {
			b.runWorkflow(w, req, prompt, wf, "")
			return
		}
	}
	// captain/frontier: claude at max effort, forced by the user.
	if req.Model == "frontier" && !titleReq {
		b.frontierChat(w, req, prompt)
		return
	}
	// captain/team: the director-planned worker tree runs brain-side.
	if req.Model == "team" && !titleReq {
		b.teamChat(w, req, prompt)
		return
	}
	// captain/workflow (CWL): the USER's topology. Either a confirmed plan by
	// id, or an expression typed straight into the prompt - the fork forwards
	// the message verbatim, so detection happens here and needs no fork change.
	// /captain prints the cheat sheet.
	if !titleReq && b.handleCaptainHelp(w, req, lastUserRaw(req.Messages)) {
		return
	}
	// /context manages cross-project sharing - a control word, never a task.
	if !titleReq && b.handleContext(w, req, lastUserRaw(req.Messages)) {
		return
	}
	// /euclid - memory status and distillation (MM38).
	if !titleReq && b.handleEuclid(w, req, lastUserRaw(req.Messages)) {
		return
	}
	// /parallel runs a second task beside this chat - a control word, never a task.
	if !titleReq && b.handleParallel(w, req, lastUserRaw(req.Messages)) {
		return
	}
	// /repeat manages detached re-run threads - a control word, never a task.
	if !titleReq && b.handleRepeat(r.Context(), w, req, lastUserRaw(req.Messages)) {
		return
	}
	// /btw - the queued copy of a note already handed to the running worker
	// is acknowledged, not run; one nobody took runs as the follow-up turn.
	if !titleReq && b.handleBtw(w, req, lastUserRaw(req.Messages)) {
		return
	}
	// /interrupt - the queued copy prints what the out-of-band call did.
	if !titleReq && b.handleInterrupt(w, req, lastUserRaw(req.Messages)) {
		return
	}
	// /init regenerates captain's configs (worker permissions, env scaffold) -
	// a control word, never a worker task.
	if !titleReq && b.handleInit(w, req, raw) {
		return
	}
	// …and only now the route: a control word is never a task. With the
	// auto route ahead of these handlers, "/repeat 3 /frontier <task>" went
	// to the director whole; when it planned a team the turn ran as that
	// team once and the loop never started (live 2026-09-15 - it had only
	// ever worked when triage fast-pathed or the director failed), and
	// "/repeat show" paid a 7s triage call for a status line.
	// captain/auto: the caller does NOT choose a leg. Triage runs, the director
	// is asked only when triage cannot decide, and the dispatch happens inside
	// this turn - where progress already streams. A caller that decides first
	// (the old fork, or any client calling /v1/route before sending) freezes
	// its UI for the whole decision: measured p50 2.9s, p90 25s, max 44s on our
	// own traffic. This path never does.
	if req.Model == "auto" && !titleReq {
		task := lastUserTurn(prompt)
		if resp, fail := b.decideRoute(routeReq{Task: task, Prefer: captaincode.MidPromptPrefer(lastUserRaw(req.Messages)), ws: req.ws}); fail != nil {
			req.Model = string(b.autoFallbackLeg())
			fmt.Printf("captain brain: auto could not route (%s) → %s\n", fail.msg, req.Model)
		} else {
			req.Model = resp.Model
			if resp.Effort != "" {
				req.ws.Effort = captaincode.Effort(resp.Effort)
			}
			fmt.Printf("captain brain: auto → %s · %s effort · %s\n", resp.Model, req.ws.Effort, resp.Rationale)
		}
		switch req.Model {
		case "team":
			b.teamChat(w, req, prompt)
			return
		case "frontier":
			b.frontierChat(w, req, prompt)
			return
		}
	}
	if workflowEnabled() && !titleReq {
		// A confirmed plan by id (API clients / model="workflow").
		if req.WorkflowID != "" {
			e, ok := b.takeWorkflow(req.WorkflowID)
			if !ok {
				writeErr(w, 400, "workflow "+req.WorkflowID+" is unknown or expired - compile it again")
				return
			}
			b.runWorkflow(w, req, prompt, e.wf, req.WorkflowID)
			return
		}
		// "/wf <english>" compiles + previews; "/run wf_x" executes.
		if b.handleWorkflowControl(w, req, prompt, raw) {
			return
		}
		// A typed expression runs straight away - the user named the legs. A
		// single leg WITH a gate is also workflow-shaped: only the workflow
		// executor owns the gate machinery (R2).
		if strings.HasPrefix(strings.TrimSpace(raw), "/") {
			if wf, err := captaincode.ParseWorkflow(raw); err == nil && (wf.MultiStage() || wf.HasGate()) {
				b.runWorkflow(w, req, prompt, wf, "")
				return
			} else if err != nil && captaincode.LooksLikeWorkflow(raw) {
				writeErr(w, 400, "workflow syntax: "+err.Error())
				return
			}
		}
	}
	// Cross-project reference material, when this project is allowed to read
	// others. Appended (not prepended) so it rides in the kept tail and does
	// not disturb the compaction cache key, and announced in the terminal -
	// silent context is unauditable context.
	if !titleReq {
		if block, srcs := sharedContextFor(currentProject(req.ws), lastUserTurn(prompt)); block != "" {
			prompt += block
			fmt.Printf("captain brain: shared context injected (%d chars) from %s\n", len(block), strings.Join(srcs, ", "))
			b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: "context", Model: "context",
				Text: fmt.Sprintf("quoted %d chars from %s", len(block), strings.Join(srcs, ", "))})
		}
	}
	// A route-time workflow (named sequence) outranks both the team plan and a
	// solo fallback: the user specified the order, so run it.
	if workflowEnabled() {
		if task := lastUserTurn(prompt); b.hasWorkflowForTask(task) {
			if wf, ok := b.takeWorkflowForTask(task); ok {
				fmt.Printf("captain brain: executing route-time workflow %s (named sequence)\n", wf.Key())
				b.runWorkflow(w, req, prompt, wf, "wf_named")
				return
			}
		}
	}

	// A fan-out plan the router already paid for must not be thrown away: when
	// the director takes longer than the fork's route budget
	// (CAPTAIN_ROUTE_TIMEOUT_MS), the fork falls back to a single leg and calls
	// the wrapper with it - so a 3-reviewer team the user asked for silently ran
	// solo on grok (live 2026-07-30, 57.7s plan vs a 45s budget). The plan is
	// cached by task text; if one is waiting, execute it.
	if teamEnabled() && req.Model != "team" && !titleReq {
		if task := lastUserTurn(prompt); b.hasTeamPlan(task) {
			fmt.Printf("captain brain: recovered cached team plan for %q (wrapper was called with %s - route budget likely expired)\n", promptPeek(task), req.Model)
			b.teamChat(w, req, prompt)
			return
		}
	}

	// The model id IS the leg name (captain provider models: claude/codex/grok/
	// free/cursor). Dispatch it through captain's wrapper for that leg.
	leg := captaincode.Leg(req.Model)
	if !captaincode.KnownLeg(leg) {
		leg = captaincode.LegClaude
	}
	// Replay budgets scale with the turn's complexity (usage analysis F3/I2):
	// the stateless wrapper replays the conversation every turn, which put
	// claude at 331k tokens per run - mostly history a typo fix never needed.
	// windowPrompt keeps the head (system framing) and the tail (recent turns),
	// so style/context from the last few exchanges survives the cut.
	budget := promptBudget(leg)
	if os.Getenv("CAPTAIN_REPLAY_BUDGETS") != "0" {
		if cap := replayBudget(captaincode.TriageTask(lastUserTurn(prompt)).Class); cap > 0 && cap < budget {
			budget = cap
		}
	}
	if titleReq {
		// A title is six words. It gets six words' worth of prompt: no working
		// context, no Euclid orientation, no deliverable contract - each of
		// those tells the model it is an engineer with a task, and on
		// 2026-09-11 a title request duly started an "explore the repo"
		// subagent in the user's repository.
		prompt = titlePrompt(lastUserTurn(prompt))
	} else {
		prompt = b.fitPrompt(req.ws, leg, prompt, budget)
		prompt += workerContext(req.ws) + deliverableContract + callbackContract(req.ws, leg)
	}
	// One execution per (leg, task): the fork re-issues a turn's request, and a
	// retry of an 8-minute run used to start a SECOND 8-minute run while the
	// first answer went to an abandoned request (live 2026-07-30). A repeat
	// within soloResultTTL is served the same answer.
	dedupeKey := string(leg) + "\x00" + lastUserTurn(prompt)
	// A /repeat iteration is real work: attaching it to the previous
	// iteration's cached answer would make the whole thread a no-op.
	internal := r.Context().Value(noDedupeKey{}) != nil
	if cur, attached := b.beginSolo(dedupeKey); attached && !internal {
		b.serveAttachedSolo(w, req, leg, cur)
		return
	}
	defer b.finishSolo(dedupeKey)

	// An answer produced for a client that had gone away (stall→reroute chains
	// outlive TUI requests; a brain restart wipes the in-memory dedupe) is
	// preserved in run history marked Abandoned. An identical resend gets that
	// answer instead of paying for the work twice ("I never got the answer
	// from codex", 2026-08-01 - a 14-minute chain delivered into a dead
	// connection). Delivered answers are never replayed: asking again after
	// you SAW the answer means run it again.
	if orphan, ok := findAbandonedAnswer(lastUserTurn(prompt)); ok {
		fmt.Printf("captain brain: serving preserved answer %s (previous request abandoned mid-run)\n", orphan.ID)
		emit, _, finish := newCompletionWriter(w, req, string(leg))
		defer finish() // idempotent; a missed teardown feeds the keepalive-panic class
		emit(fmt.Sprintf("[captain: recovered - your previous request for this dropped before %s finished; this is that answer (%s)]\n\n",
			strings.Join(orphan.Legs, "+"), orphan.ID))
		emit(orphan.Output)
		finish()
		markAnswerDelivered(orphan.ID)
		return
	}

	fmt.Printf("captain brain: %s wrapper running (%d msgs)…\n", leg, len(req.Messages))
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "run", Leg: string(leg), Model: string(leg), Effort: string(req.ws.Effort), Text: promptPeek(lastUserTurn(prompt))})
	t0 := time.Now()

	// M3.9: stock the worker's shelf for this task. A solo worker runs in the
	// user's own directory, so the shelf is staged there, kept out of `git
	// status` while it is there, and removed when the turn ends - a title
	// request gets none of it. With nothing synced this is a nil shelf and no
	// work at all.
	var shelf *captaincode.Shelf
	if !titleReq {
		shelf = b.stockShelf(req.ws.Dir, lastUserTurn(prompt))
		defer shelf.Remove()
	}
	stocked := shelf.Refs()

	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	model := req.Model
	if model == "" {
		model = string(leg)
	}

	if !req.Stream {
		ranLeg, res, err := b.runWorkerRerouted(req.ws, leg, prompt, nil, nil, "")
		if err == nil {
			res = b.nudgeNarration(req.ws, ranLeg, prompt, res, nil, nil)
		}
		// Partial salvage (parity with team/workflow): a capped run WITH real
		// text is a degraded success, not 15 discarded minutes (live
		// r0824-2121d5). AFTER the nudge check so a salvaged stub can never
		// trigger a narration rerun and burn a second cap.
		if r2, e2, note := b.salvagePartial(ranLeg, res, err); note != "" {
			res, err = r2, e2
			res.Text = "[captain: " + note + "]\n\n" + res.Text
		}
		if ranLeg != leg && err == nil {
			res.Text = fmt.Sprintf("[captain: %s unavailable → answered by %s]\n\n", leg, ranLeg) + res.Text
		}
		leg = ranLeg
		elapsed := time.Since(t0)
		if err != nil {
			b.onWorkerError(leg, err)
			b.recordSolo(dedupeKey, "", err)
			if !titleReq { // a session title is housekeeping, not work
				recordRunHistory(runRecord{Kind: "solo", Model: model, Legs: []string{string(leg)},
					Task: lastUserTurn(prompt), Output: res.Text, Error: err.Error(), DurationMs: elapsed.Milliseconds(), Logs: logPaths(res)})
			}
			fmt.Printf("captain brain: %s wrapper error in %s - %v\n", leg, elapsed.Round(time.Millisecond), err)
			b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: string(leg), Model: string(leg), Text: "error: " + err.Error(), Ms: elapsed.Milliseconds()})
			writeWorkerError(w, leg, err)
			return
		}
		b.recordSolo(dedupeKey, res.Text, nil)
		if !titleReq { // a session title is housekeeping, not work
			recordRunHistory(runRecord{Kind: "solo", Model: model, Legs: []string{string(leg)},
				Task: lastUserTurn(prompt), Output: res.Text, DurationMs: elapsed.Milliseconds(),
				Abandoned: r.Context().Err() != nil, Logs: logPaths(res)})
		}
		fmt.Printf("captain brain: %s wrapper done in %s (%d chars)\n", leg, elapsed.Round(time.Millisecond), len(res.Text))
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: string(leg), Model: string(leg), Text: promptPeek(res.Text), Ms: elapsed.Milliseconds()})
		if !titleReq { // …and must not teach the router anything
			go b.recordRun(leg, prompt, res, req.ws.Dir, stocked...) // learning loop: off the response path
		}
		writeJSON(w, 200, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": model,
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": res.Text},
			}},
			"usage": turnSpend.take(req.ws.Steer),
		})
		return
	}

	// SSE streaming: forward each token from the worker as its own chunk so the
	// TUI types the answer out live instead of sitting silent for minutes. Headers
	// are written lazily on the first chunk, so if the worker errors BEFORE any
	// output (e.g. rate-limited) we can still return a proper HTTP error status.
	flush, _ := w.(http.Flusher)
	var smu sync.Mutex
	wroteHeader := false
	first := true
	writeHeader := func() {
		if wroteHeader {
			return
		}
		wroteHeader = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
	}
	var finalUsage map[string]any // set before the stop chunk: the AI SDK reads usage off the last chunk
	chunk := func(delta map[string]any, finish any) {
		smu.Lock()
		defer smu.Unlock()
		writeHeader()
		obj := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		if finish != nil && finalUsage != nil {
			obj["usage"] = finalUsage
		}
		payload, _ := json.Marshal(obj)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flush != nil {
			flush.Flush()
		}
	}
	// Heartbeats during quiet stretches (thinking, long tool runs): a silent
	// SSE stream gets idle-killed and the turn dies spinner-and-all while the
	// worker keeps going (live 2026-07-25). SSE comments are parser-invisible.
	stopKA := make(chan struct{})
	defer close(stopKA)
	go func() {
		t := time.NewTicker(sseKeepaliveEvery)
		defer t.Stop()
		for {
			select {
			case <-stopKA:
				return
			case <-t.C:
				smu.Lock()
				writeHeader()
				fmt.Fprint(w, ": keepalive\n\n")
				if flush != nil {
					flush.Flush()
				}
				smu.Unlock()
			}
		}
	}()
	// Visible progress (tool activity + elapsed-time heartbeat) rides the
	// reasoning channel, so the TUI shows what the worker is doing without a
	// single character of it landing in the answer.
	feed := newProgressFeed(string(leg), func(s string) {
		chunk(map[string]any{"reasoning_content": s}, nil)
	})
	defer feed.close()
	// A background /parallel run that landed since the last turn is announced
	// here - on the progress channel, never spliced into the answer text.
	// …but never into a detached round's own stream (a /repeat or /parallel
	// iteration): that stream is captured, not watched, and it would consume
	// the notice meant for the user's next turn.
	if !internal {
		if n := b.parallelNotice(req.ws.Dir); n != "" {
			feed.note(n)
		}
		if n := b.repeatNotice(req.ws.Dir); n != "" {
			feed.note(n)
		}
	}
	answer := func(d string) {
		feed.touch()
		if first {
			first = false
			chunk(map[string]any{"role": "assistant", "content": d}, nil)
			return
		}
		chunk(map[string]any{"content": d}, nil)
	}
	ranLeg, res, err := b.runWorkerRerouted(req.ws, leg, prompt, answer, feed.note, "")
	leg = ranLeg
	if err == nil {
		res = b.nudgeNarration(req.ws, leg, prompt, res, answer, feed.note)
	}
	// Partial salvage, stream flavor: text already streamed gets a trailing
	// marker in the success tail (prefixing res.Text would re-send nothing);
	// text NOT yet streamed goes out whole below, so prefix it here.
	partialNote := ""
	if r2, e2, note := b.salvagePartial(leg, res, err); note != "" {
		res, err, partialNote = r2, e2, note
		if first {
			res.Text = "[captain: " + note + "]\n\n" + res.Text
		}
	}
	feed.close()
	elapsed := time.Since(t0)
	if err != nil {
		b.onWorkerError(leg, err)
		b.recordSolo(dedupeKey, "", err)
		if !titleReq { // a session title is housekeeping, not work
			recordRunHistory(runRecord{Kind: "solo", Model: model, Legs: []string{string(leg)},
				Task: lastUserTurn(prompt), Output: res.Text, Error: err.Error(), DurationMs: elapsed.Milliseconds(), Logs: logPaths(res)})
		}
		fmt.Printf("captain brain: %s wrapper error in %s - %v\n", leg, elapsed.Round(time.Millisecond), err)
		b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: string(leg), Model: string(leg), Text: "error: " + err.Error(), Ms: elapsed.Milliseconds()})
		if !wroteHeader {
			writeWorkerError(w, leg, err) // nothing streamed yet → real HTTP error
			return
		}
		// already mid-stream → surface the error inline and close cleanly
		chunk(map[string]any{"content": "\n\n[captain: " + string(leg) + " error - " + err.Error() + "]"}, nil)
		chunk(map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flush != nil {
			flush.Flush()
		}
		return
	}
	b.recordSolo(dedupeKey, res.Text, nil)
	if !titleReq { // a session title is housekeeping, not work
		recordRunHistory(runRecord{Kind: "solo", Model: model, Legs: []string{string(leg)},
			Task: lastUserTurn(prompt), Output: res.Text, DurationMs: elapsed.Milliseconds(),
			Abandoned: r.Context().Err() != nil, Logs: logPaths(res)})
	}
	fmt.Printf("captain brain: %s wrapper done in %s (%d chars, streamed)\n", leg, elapsed.Round(time.Millisecond), len(res.Text))
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "done", Leg: string(leg), Model: string(leg), Text: promptPeek(res.Text), Ms: elapsed.Milliseconds()})
	if !titleReq { // …and must not teach the router anything
		go b.recordRun(leg, prompt, res, req.ws.Dir, stocked...) // learning loop: off the response path
	}
	if first { // worker produced no deltas - emit the whole reply once
		chunk(map[string]any{"role": "assistant", "content": res.Text}, nil)
	} else if res.Partial && partialNote != "" { // text already streamed → the marker trails it
		chunk(map[string]any{"content": "\n\n[captain: " + partialNote + " - the output above is partial]"}, nil)
	}
	finalUsage = turnSpend.take(req.ws.Steer)
	chunk(map[string]any{}, "stop")
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flush != nil {
		flush.Flush()
	}
}

// writeWorkerError returns an OpenAI-style error (not a 200 with the failure as
// the assistant's "answer"), so the fork shows a real error and the scorecard
// never learns from an error string. Rate limits map to 429.
// trackedWriter records whether anything has been written to the response,
// so a late error can pick the right channel (HTTP status vs inline text).
type trackedWriter struct {
	http.ResponseWriter
	committed atomic.Bool
}

func trackWriter(w http.ResponseWriter) *trackedWriter {
	if tw, ok := w.(*trackedWriter); ok {
		return tw
	}
	return &trackedWriter{ResponseWriter: w}
}

func (t *trackedWriter) WriteHeader(code int) {
	t.committed.Store(true)
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackedWriter) Write(p []byte) (int, error) {
	t.committed.Store(true)
	return t.ResponseWriter.Write(p)
}

func (t *trackedWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Committed reports whether the header has gone out.
func (t *trackedWriter) Committed() bool { return t.committed.Load() }

// streamCommitted reports whether w is a tracked writer that already started
// its response.
func streamCommitted(w http.ResponseWriter) bool {
	tw, ok := w.(*trackedWriter)
	return ok && tw.Committed()
}

// writeInlineFailure speaks a failure into an already-open SSE stream as the
// assistant's text: the only channel left once the header is out. The
// caller's deferred finish() closes the stream.
func writeInlineFailure(w http.ResponseWriter, text string) {
	payload, _ := json.Marshal(map[string]any{
		"id": fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": "captain",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": nil}},
	})
	fmt.Fprintf(w, "data: %s\n\n", payload)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeWorkerError(w http.ResponseWriter, leg captaincode.Leg, err error) {
	if streamCommitted(w) {
		writeInlineFailure(w, fmt.Sprintf("\n[captain] %s failed: %s\n", leg, err.Error()))
		return
	}
	code, etype := http.StatusBadGateway, "worker_error"
	msg := string(leg) + " wrapper: " + err.Error()
	if errors.Is(err, captaincode.ErrRateLimited) {
		code, etype = http.StatusTooManyRequests, "rate_limit_exceeded"
		msg = string(leg) + " leg is rate-limited (subscription window) - cooling down"
	}
	if errors.Is(err, captaincode.ErrProviderDown) {
		code, etype = http.StatusServiceUnavailable, "provider_unavailable"
		msg = "the " + string(leg) + " leg's provider is temporarily unavailable (upstream outage) - cooling this leg down briefly; retry shortly or switch model"
	}
	if errors.Is(err, captaincode.ErrContextOverflow) {
		// The conversation, not the worker, is at fault - retrying the same
		// input overflows again, so 400 (the SDK blind-retries 5xx). Windowing
		// should normally prevent this; reaching here means even the windowed
		// prompt overflowed.
		code, etype = http.StatusBadRequest, "context_overflow"
		msg = "this conversation is too large for the " + string(leg) + " leg even after truncation - start a new session (/new) or switch to a larger-context model"
	}
	writeJSON(w, code, map[string]any{"error": map[string]any{"message": msg, "type": etype, "code": etype}})
}

// benchPolicy says how long a failure keeps a leg off the ladder, and why.
// The brain and the CLI both answer to it: the CLI benched rate limits only,
// so a logged-out or unconfigured leg was dispatched again on the very next
// turn, every turn (2026-09-22). A zero duration means "record it, don't
// bench it" - an unclassified error is not evidence about the leg.
func benchPolicy(leg captaincode.Leg, err error) (time.Duration, string) {
	var d time.Duration
	var why string
	switch {
	case captaincode.IsSpendLimit(err):
		// A monthly cap, not a window: off the ladder for the day, and the
		// reason says what reopens it.
		d, why = 24*time.Hour, "monthly spend limit reached - raise the cap at claude.ai/settings/usage, then `captain legs reopen "+string(leg)+"`"
	case errors.Is(err, captaincode.ErrRateLimited):
		d, why = 30*time.Minute, "rate-limited"
		// The provider said when its window reopens: bench until then (+1m
		// slack), within sane bounds. A flat 30m benched claude until 01:17
		// for a window that reopened at 02:50 (2026-09-10).
		var rl *captaincode.RateLimitError
		if errors.As(err, &rl) && !rl.ResetAt.IsZero() {
			if until := time.Until(rl.ResetAt) + time.Minute; until > time.Minute {
				d = until
				if d > 6*time.Hour {
					d = 6 * time.Hour
				}
			}
			why = "rate-limited, window resets " + rl.ResetAt.Local().Format("15:04")
		}
	case errors.Is(err, captaincode.ErrProviderBilling):
		// Reopens when someone pays, not on a clock: off the ladder for the
		// day, and the reason says what to do.
		d, why = 24*time.Hour, "provider credits depleted - top up or subscribe, then `captain legs reopen <leg>`"
	case errors.Is(err, captaincode.ErrProviderNotConfigured):
		// Not a rejected key but an absent one: the serve resolves no model
		// for this leg. Same hour-long bench, a reason that names the fix.
		d, why = time.Hour, "provider not configured - `opencode auth login`, then `captain legs reopen "+string(leg)+"`"
	case errors.Is(err, captaincode.ErrProviderAuth):
		// A rejected key does not heal by itself: bench the leg for an hour
		// (the next attempt is an instant 403 anyway) and say what to fix.
		d, why = time.Hour, "credentials rejected - check the provider key"
	case errors.Is(err, captaincode.ErrProviderDown):
		d, why = 10*time.Minute, "provider unavailable"
	case errors.Is(err, captaincode.ErrWorkerStalled):
		// The dispatcher already retried a stall once on a fresh session; a leg
		// that stalls twice has a sick provider stream - bench it briefly.
		d, why = 10*time.Minute, "stalling"
	}
	return d, why
}

// onWorkerError cools a leg down in the brain's ledger so subsequent
// /v1/route calls skip it: 30m for a rate limit (subscription window), a
// short window for a provider outage (transient - live 2026-07-19: xAI 503
// bursts; 30m would bench a healthy leg long after recovery).
func (b *brain) onWorkerError(leg captaincode.Leg, err error) {
	d, why := benchPolicy(leg, err)
	b.mu.Lock()
	// Reliability is a routing signal: every handled failure lands on the
	// ledger (glm stalled all morning with a spotless q9.0 - the director
	// could not see it, 2026-07-25). Generic errors record but don't bench.
	b.ledger.Record(captaincode.Event{Leg: leg, Reason: "wrapper", Outcome: "fail", Error: truncate(err.Error(), 160)})
	// Quota telemetry (M2.3): a rate-limit message carries real quota state.
	// Record it so `captain quota` can show "exhausted, resets 02:50
	// (rate-limit)" rather than only "cooling down".
	if errors.Is(err, captaincode.ErrRateLimited) {
		var rl *captaincode.RateLimitError
		if errors.As(err, &rl) {
			if b.ledger.Quotas == nil {
				b.ledger.Quotas = map[captaincode.Leg]captaincode.Quota{}
			}
			b.ledger.RecordQuota(captaincode.QuotaFromRateLimit(rl, time.Now()))
		}
	}
	if d > 0 {
		b.ledger.Cooldown(leg, d)
	}
	saveErr := b.ledger.Save()
	b.mu.Unlock()
	if d == 0 {
		return
	}
	if saveErr != nil {
		fmt.Fprintf(os.Stderr, "captain brain: save cooldown: %v\n", saveErr)
	}
	fmt.Printf("captain brain: %s %s → cooldown %s (until %s): %s\n", leg, why, d.Round(time.Second),
		time.Now().Add(d).Format("15:04"), truncate(strings.Join(strings.Fields(err.Error()), " "), 200))
}

// recordQuotaFromHeaders extracts proactive quota telemetry from a worker
// response's HTTP headers (M2.3). The opencode error JSON embeds upstream
// x-ratelimit-* response headers; the adapter runner now carries them on
// Result.Headers. This is the wiring step the roadmap named: a response
// carrying x-ratelimit-remaining-requests:5 tells the routing gate the leg
// is about to hit its limit BEFORE the window closes, rather than discovering
// it after a failed call. Called after every worker dispatch — success or
// failure — so a 200 with low remaining is recorded the same way a 429 is.
func (b *brain) recordQuotaFromHeaders(leg captaincode.Leg, res captaincode.Result) {
	if res.Headers == nil {
		return
	}
	q, ok := captaincode.QuotaFromHeaders(leg, res.Headers, time.Now())
	if !ok {
		return
	}
	b.mu.Lock()
	if b.ledger.Quotas == nil {
		b.ledger.Quotas = map[captaincode.Leg]captaincode.Quota{}
	}
	b.ledger.RecordQuota(q)
	b.mu.Unlock()
}

// salvagePartial turns a failed run that carries a partial deliverable
// (WorthKeeping: time cap, or a rate limit after real work) into a degraded
// success with a marker explaining why. A rate limit still benches the leg -
// the text is kept, the window is still closed.
func (b *brain) salvagePartial(leg captaincode.Leg, res captaincode.Result, err error) (captaincode.Result, error, string) {
	if err == nil || !captaincode.WorthKeeping(res, err) {
		return res, err, ""
	}
	note := fmt.Sprintf("%s cut off at the time limit - partial output", leg)
	if errors.Is(err, captaincode.ErrInterrupted) {
		note = fmt.Sprintf("%s stopped by /interrupt - what it produced so far", leg)
	}
	if errors.Is(err, captaincode.ErrRateLimited) {
		note = fmt.Sprintf("%s hit its usage limit - partial output", leg)
		var rl *captaincode.RateLimitError
		if errors.As(err, &rl) && !rl.ResetAt.IsZero() {
			note = fmt.Sprintf("%s hit its usage limit (window resets %s) - partial output", leg, rl.ResetAt.Local().Format("15:04"))
		}
		b.onWorkerError(leg, err) // bench the leg; the text is kept regardless
	}
	res.Partial = true
	return res, nil, note
}

// models advertises every captain-wrapped leg for provider validation.
func (b *brain) models(w http.ResponseWriter, _ *http.Request) {
	data := make([]any, 0, len(captaincode.AllLegs)+1)
	for _, leg := range captaincode.AllLegs {
		if !captaincode.ServesTasks(leg) {
			continue // a decision leg takes questions, not turns
		}
		data = append(data, map[string]any{"id": string(leg), "object": "model", "owned_by": "captain"})
	}
	// The team pseudo-model: a director-planned worker tree, executed brain-side.
	data = append(data, map[string]any{"id": "team", "object": "model", "owned_by": "captain"})
	// frontier: claude pinned to the strongest version at max thinking budget.
	data = append(data, map[string]any{"id": "frontier", "object": "model", "owned_by": "captain"})
	// workflow: the user's own topology (CWL), executed brain-side.
	data = append(data, map[string]any{"id": "workflow", "object": "model", "owned_by": "captain"})
	// auto: let the brain decide the leg, inside the turn. This is what a
	// terminal should send when the user did not name a leg.
	data = append(data, map[string]any{"id": "auto", "object": "model", "owned_by": "captain"})
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}
