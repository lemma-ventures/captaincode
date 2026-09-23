package main

// `/captain` - the cheat sheet, in the TUI.
//
// Captain adds a dozen control words to the prompt line and there was nowhere
// to look them up: the slash palette lists names, not what they do together
// (asked for 2026-09-01). This prints the lot, grouped by what you are trying
// to achieve, and says plainly which ones cannot reach the brain while a turn
// is streaming - the single most confusing thing about the current UX.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const captainHelp = `### Captain Code - commands

**Pick who runs it**
- ` + "`/claude` `/codex` `/codex-cli` `/luna` `/cursor` `/grok` `/grok-max` `/gemini` `/deepseek` `/ds4-flash` `/kimi` `/glm` `/minimax` `/qwen` `/step` `/gpt-oss` `/free`" + ` - force one leg
- ` + "`/team <task>`" + ` - director plans an ensemble · ` + "`/team /quality <task>`" + ` binds it to the best-rated legs
- ` + "`/team /frontier <task>`" + ` (or ` + "`/team /codex-cli …`" + `, any leg) - that member is binding, the director fills the rest
- ` + "`/frontier <task>`" + ` - maximum effort; the frontier legs (claude and codex-cli today) take turns, the one behind its share of recent turns going next
- ` + "`/codex-cli <task>`" + ` - gpt-6-astra at maximum effort via the Codex CLI (frontier-class, slow)
- ` + "`/quality` `/speed` `/save`" + ` - preference, before or after a leg prefix · ` + "`/quality`" + ` takes turns across the two best legs at high effort, ` + "`/save`" + ` across the open-weight legs, each on its own model at medium effort
- ` + "`/oss`" + ` - only open-weight models · ` + "`/deterministic`" + ` - only legs green in the Agentic Determinism Index, pinned to the measured serving tuple (` + "`captain adi`" + ` shows who qualifies); both compose with everything: ` + "`/oss /repeat 5 <task>`" + `, ` + "`/team /deterministic <task>`" + `

**Chain work (Captain Workflow Language)**
- ` + "`/grok analyse X > /codex review it`" + ` - ` + "`>`" + ` runs stages in sequence
- ` + "`/grok fix A + /cursor fix B`" + ` - ` + "`+`" + ` runs legs in parallel (max 4 per stage)
- one task per line also fans out in parallel
- a bare leg repeats the previous assignment: ` + "`/grok review X > /codex`" + `
- ` + "`gate: <cmd>`" + ` on a stage must exit 0 · ` + "`/wf <english>`" + ` compiles a plan · ` + "`/run wf_x`" + ` executes it

**Run things in the background**
- ` + "`/repeat N <task>`" + ` - repeat until N rounds (no N = until stopped); rounds stream in that turn
- ` + "`/repeat show|status`" + ` · ` + "`/repeat stop`" + ` (current round finishes) · ` + "`/repeat abort`" + ` (kills it)
- ` + "`/parallel <task>`" + ` - run a second task beside this chat · ` + "`/parallel show|status|stop`" + `
- ` + "`/btw <note>`" + ` - tell the worker that is already running something more (claude and the opencode legs take it mid-run; codex and cursor get it as the next turn)
- ` + "`/interrupt [reason]`" + ` - stop the running worker without losing its work: it writes a handoff (done / left / how to resume) and ends; codex and cursor are stopped with what they streamed; the handoff goes to memory

**Settings & housekeeping**
- ` + "`/init`" + ` - regenerate worker permissions (web, edits, bash; .env stays denied)
- ` + "`/context status|publish on|off|consume all|none|<path>`" + ` - cross-project sharing (off by default)
- ` + "`/euclid status`" + ` · ` + "`/euclid distill [apply]`" + ` - Euclid memory: your brains, and register edits from the run journal (` + "`captain euclid init [--repo]`" + ` to start · ` + "`captain euclid link <repo>`" + ` / ` + "`links`" + ` for cross-repo memory)
- ` + "`/captain director`" + ` - who directs, why, and how much each judge has worked lately
- ` + "`/captain claude`" + ` (any judge leg) · ` + "`/captain frontier`" + ` best-ranked · ` + "`/captain quality`" + ` best of tier 2 · ` + "`/captain auto`" + ` least-used capable · ` + "`/captain reset`" + `
- ` + "`/captain more oss`" + ` (also cheap, quality, fast, frontier, deterministic) · ` + "`/captain less oss`" + ` · ` + "`/captain more oss 20%`" + ` · ` + "`/captain oss=20% frontier=30% …`" + ` - the routing mix; prints the targets. Unset, frontier=quality=cheap=fast=oss=20% and deterministic=0, and that default does not steer
- ` + "`/captain targets`" + ` - print the mix · ` + "`/captain mix reset`" + ` - back to the default
- ` + "`/captain`" + ` - this list · the full reference is docs/TUI.md

**When a turn is streaming, the TUI QUEUES what you type** - no slash command can
reach the brain until it ends. Press **Esc** to free the input, or use the shell:
- ` + "`captain kill`" + ` - stop the run in flight (` + "`--loops`" + ` also ends /repeat)
- ` + "`captain stop`" + ` - end a /repeat loop after the current round
- ` + "`captain runs`" + ` / ` + "`captain show <id>`" + ` - every run, with full output
- ` + "`captain status`" + ` / ` + "`captain watch`" + ` - what is running now
- ` + "`captain legs`" + ` / ` + "`captain legs add <id> <provider/model>`" + ` - the model registry (no rebuild) · ` + "`captain priors sync`" + ` - leaderboard priors
`

func (b *brain) handleCaptainHelp(w http.ResponseWriter, req oaiChatReq, raw string) bool {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "/captain" || t == "/captain help" || t == "/help" {
		emit, _, finish := newCompletionWriter(w, req, "captain")
		defer finish()
		emit(captainHelp)
		finish()
		return true
	}
	// `/captain director` shows the helm; `/captain <leg|frontier|quality|auto|reset>`
	// switches it - the same entry the CLI and HTTP use (director_cmd.go).
	if !strings.HasPrefix(t, "/captain ") {
		return false
	}
	word := strings.TrimSpace(strings.TrimPrefix(t, "/captain "))
	if cmd, ok := captaincode.ParseSteerCommand(word); ok {
		emit, _, finish := newCompletionWriter(w, req, "captain")
		defer finish()
		emit(b.applySteerCommand(cmd))
		finish()
		return true
	}
	if strings.ContainsAny(word, " \t") {
		return false // "/captain do this and that" is a task, not a control word
	}
	emit, _, finish := newCompletionWriter(w, req, "captain")
	defer finish()
	if word == "director" || word == "status" {
		emit(b.directorStatusText())
		finish()
		return true
	}
	pick, mode, err := b.switchDirector(word)
	if err != nil {
		emit("**/captain " + word + "**: " + err.Error() + "\n\n" + directorUsageText)
		finish()
		return true
	}
	b.pushActivity(activity{Dir: req.ws.Dir, Kind: "route", Leg: string(pick.Leg), Model: "director", Text: "director " + mode + ": " + pick.Reason})
	emit(fmt.Sprintf("**Director → %s** (%s)\n\n%s\n", pick.Leg, mode, pick.Reason))
	finish()
	return true
}

const directorUsageText = "`/captain director` shows the helm · `/captain <leg>` pins one (claude, grok, glm, kimi, gemini, codex…) · `/captain frontier` the best-ranked judge · `/captain quality` the best of tier 2 · `/captain auto` the least-used capable leg over the trailing window · `/captain reset` back to the default"

// directorStatusText is what `/captain director` prints.
func (b *brain) directorStatusText() string {
	b.mu.Lock()
	ladder := b.ladder()
	eff := b.effectiveDirector()
	mode := b.directorModeName()
	reason := b.dirModePick.Reason
	usage := b.directorUsage(captaincode.DirectorWindow())
	cool := map[captaincode.Leg]time.Time{}
	for l, u := range b.ledger.Cooldowns {
		cool[l] = u
	}
	b.mu.Unlock()
	var sb strings.Builder
	fmt.Fprintf(&sb, "### Director: **%s** (%s)\n", eff, mode)
	if reason != "" {
		fmt.Fprintf(&sb, "%s\n", reason)
	}
	fmt.Fprintf(&sb, "\nLadder: %s\n\n", strings.Join(legStrings(ladder), " → "))
	fmt.Fprintf(&sb, "| judge | directs as | index | used over %s | |\n|---|---|---:|---:|---|\n", captaincode.DirectorWindow().Round(time.Hour))
	now := time.Now()
	for _, c := range captaincode.DirectorCandidates() {
		note := ""
		if until, ok := cool[c.Leg]; ok && now.Before(until) {
			note = "cooling until " + until.Format("15:04")
		}
		mark := ""
		if c.Leg == eff {
			mark = "☸"
		}
		fmt.Fprintf(&sb, "| %s %s | %s | %.1f | %s | %s |\n", mark, c.Leg, c.Model, c.Perf, usage[c.Leg].Round(time.Minute), note)
	}
	sb.WriteString("\n" + directorUsageText + "\n")
	return sb.String()
}
