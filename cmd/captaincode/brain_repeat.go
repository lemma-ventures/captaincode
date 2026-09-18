package main

// /repeat - re-run a prompt automatically after each execution finishes
// (2026-08-30). `/repeat 10 /team audit the deck` runs the ensemble ten times;
// `/repeat /grok check the feed` runs until stopped; `/repeat finish` (or
// `wrapup`, `stop`) ends it once the round in flight completes, `/repeat
// abort` now.
//
// Threads are DETACHED on purpose. A repeat that streamed inside its launching
// turn would hold the TUI turn open for its whole life, and the fork queues
// typed input while a turn streams - so `/repeat stop` could never be typed.
// Detaching returns the turn immediately, leaving the input free for the stop
// word. Each iteration runs the FULL dispatch path (solo, team, workflow), so
// it records to run history and the scorecards exactly like a typed turn; watch
// them live with `captain watch` or read them with `captain runs`.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"time"
)

// repeatHardCap bounds an unbounded thread: an infinite loop on a PAID leg is
// a runaway bill, so "infinite" means "until stopped, or this many". Override
// with CAPTAIN_REPEAT_MAX.
func repeatHardCap() int {
	if v := os.Getenv("CAPTAIN_REPEAT_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 100
}

// repeatPause separates iterations so a fast-failing thread cannot spin.
const repeatPause = 3 * time.Second

// repeatMaxFails stops a thread that is failing consistently - repeating a
// broken task 100 times helps nobody.
const repeatMaxFails = 3

// roundRecord is what a finished round did - kept so the TUI can show it.
// Detached rounds cannot stream into the turn that launched them, so without
// this the user supervises a loop they cannot see (asked for 2026-08-31).
type roundRecord struct {
	n       int
	at      time.Time
	dur     time.Duration
	text    string
	summary string // what the round DID, in a line or two
	err     string
	shown   bool
	noop    bool // the round deferred instead of working (see roundDeferred)
}

// roundDeferred spots a round that did nothing: a short answer that talks
// about waiting, scheduling or deferring instead of delivering. A headless
// round cannot come back later - "scheduled wakeup" is a no-op in `claude -p`
// and a background process ends with the run - so round 3 of a /frontier
// thread read the repo for a minute, waited 58 minutes for a benchmark round
// 2 had started (dead since round 2 ended), and returned "No files were
// touched … deferred … to await the runner's fixed-point phase and scheduled
// wakeup" (2026-09-12). The next round is told, so it does the work instead.
func roundDeferred(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" || len(t) > 600 {
		return false
	}
	for _, w := range []string{"deferred", "defer ", "await", "wakeup", "wake-up", "scheduled", "no files were touched", "no code changes"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// roundContract is appended to every round's task: what a round is and is
// not, so the worker never plans around a future it will not have.
const roundContract = "\n\n[captain] This is one round of a repeating task. A round is a fresh, headless run: nothing you schedule survives it, a background process you start ends with it, and no wakeup comes. Do the next concrete step NOW, in this round, and report what changed; if a long computation is needed, run it to completion inside this round (it may take an hour) rather than leaving it for later."

type repeatThread struct {
	id         string
	dir        string // the workspace the thread runs in - its rounds and its listing belong to that TUI
	task       string // the prompt as typed, minus the /repeat directive
	target     int    // 0 = until stopped (bounded by repeatHardCap)
	done       int
	failed     int
	started    time.Time
	lastErr    string
	cancel     context.CancelFunc
	stopped    bool   // graceful: no NEW round starts; the one in flight finishes
	stopReason string // why the loop ended, when it was not the user
	finished   bool
	rounds     []roundRecord
	// live is the running round's progress so far (the worker's tool
	// activity and heartbeats), what a watcher shows between two rounds.
	live      []string
	liveSeq   int // total lines ever pushed, so a watcher knows what it has shown
	roundFrom time.Time
	// notes are answers to control words typed WHILE this thread is watched
	// (/v1/repeat/ctl): the watching turn prints them in place, since the
	// typed copy sits queued behind it until the watch ends.
	notes   []string
	noteSeq int
}

const repeatLiveKeep = 40

// roundSummary distills a round to what it actually DID. Rounds return walls
// of prose (a team round is two workers plus a director review), and a
// supervisor scrolling past 3k characters per round is as blind as one seeing
// nothing (2026-08-31). Runs on the compaction leg - fast and cheap - and
// falls back to the first meaningful line rather than blocking the loop.
func (b *brain) roundSummary(task, text string) string {
	if strings.TrimSpace(text) == "" {
		return "(no output)"
	}
	if b.roundSummaryFn != nil {
		return b.roundSummaryFn(text)
	}
	d := captaincode.NewDispatcher(opencodePort)
	d.Title = "round-summary"
	d.NoTools = true
	d.Timeout = 90 * time.Second
	leg := compactLeg()
	res, err := d.Run(leg,
		"In AT MOST 2 sentences, say what this agent round actually DID: concrete "+
			"changes, files touched, decisions taken, or why nothing changed. Name "+
			"specifics. No preamble, no restating the task.\n\n"+text)
	// The digest is supervision overhead the loop pays for. It runs after the
	// round's own turn has been billed and closed, so it gets a task row of
	// its own rather than disappearing (ROADMAP M1.2).
	if h := b.chargeOwnTask("repeat round digest: " + truncate(task, 80)); h != nil {
		h(leg, "round-summary", res, err)
	}
	if err != nil || strings.TrimSpace(res.Text) == "" {
		return firstLine(text, "")
	}
	return strings.TrimSpace(res.Text)
}

// repeatRoundKeep bounds retained round text: supervision needs the gist of
// recent rounds, not a full second transcript in memory.
const repeatRoundKeep = 8
const repeatRoundChars = 1_200

func (b *brain) repeatState() map[string]*repeatThread {
	if b.repeats == nil {
		b.repeats = map[string]*repeatThread{}
	}
	return b.repeats
}

// repeatControl recognizes "<word>", "<word> <thread-id|last|all>" and nothing
// else. Anything wordier is a task, not a command.
func repeatControl(rest string) (word, arg string, ok bool) {
	f := strings.Fields(rest)
	if len(f) == 0 {
		return "", "", false
	}
	switch f[0] {
	case "status", "stop", "finish", "wrapup", "abort", "show", "watch":
	default:
		return "", "", false
	}
	if len(f) == 1 {
		return f[0], "", true
	}
	if len(f) == 2 && (f[1] == "last" || f[1] == "all" || strings.HasPrefix(f[1], "rp_")) {
		return f[0], f[1], true
	}
	return "", "", false // "watch the queue" is a task
}

// parseRepeat splits "/repeat [N] <rest>" into count and the inner prompt.
// count 0 means unbounded. ok=false when the text is not a /repeat directive.
func parseRepeat(raw string) (count int, rest string, ok bool) {
	t := strings.TrimSpace(raw)
	low := strings.ToLower(t)
	if !strings.HasPrefix(low, "/repeat") {
		return 0, "", false
	}
	t = strings.TrimSpace(t[len("/repeat"):])
	if t == "" {
		return 0, "", true // bare "/repeat" → status is more useful than a loop
	}
	fields := strings.Fields(t)
	if n, err := strconv.Atoi(fields[0]); err == nil && n > 0 {
		return n, strings.TrimSpace(strings.TrimPrefix(t, fields[0])), true
	}
	return 0, t, true
}

// handleRepeat intercepts the /repeat control words. Returns true when it
// answered the request.
func (b *brain) handleRepeat(ctx context.Context, w http.ResponseWriter, req oaiChatReq, raw string) bool {
	count, rest, ok := parseRepeat(raw)
	if !ok {
		return false
	}
	emit, status, finish := newCompletionWriter(w, req, "repeat")
	defer finish()

	restLow := strings.ToLower(strings.TrimSpace(rest))
	// A control word only counts when it stands alone or is followed by a
	// thread reference. Otherwise "/repeat watch the queue" - a perfectly
	// ordinary task that happens to start with "watch" - would be swallowed as
	// a command (caught by TestRepeatStopEndsTheThread, 2026-08-31).
	word, arg, isCtl := repeatControl(restLow)
	switch {
	case restLow == "" || (isCtl && word == "status"):
		emit(b.repeatStatus(req.ws.Dir))
		finish()
		return true
	case isCtl && word == "watch":
		b.repeatWatch(ctx, emit, status, req.ws.Dir, arg)
		finish()
		return true
	case isCtl && word == "show":
		emit(b.repeatShow(req.ws.Dir, arg))
		finish()
		return true
	case isCtl && word == "abort":
		emit(b.repeatAbort(req.ws.Dir, arg))
		finish()
		return true
	case isCtl && (word == "stop" || word == "finish" || word == "wrapup"):
		// Both graceful: the round in flight completes, no new one starts.
		// `finish` and `wrapup` say what `stop` does - operators read "stop"
		// as "kill" and hesitated to type it mid-round (2026-09-13).
		emit(b.repeatStop(req.ws.Dir, arg))
		finish()
		return true
	}

	th := b.startRepeat(req, rest, count)
	horizon := fmt.Sprintf("%d rounds", th.target)
	if th.target == 0 {
		horizon = fmt.Sprintf("until stopped (hard cap %d)", repeatHardCap())
	}
	// The launching turn STAYS OPEN and streams the rounds. Requiring a second
	// command was the wrong call: someone who starts a loop expects to see it,
	// not to remember an incantation (2026-09-01 - a loop ran 15 rounds while
	// the user watched an empty screen). The thread is still detached, so Esc
	// only stops WATCHING: the loop continues and `/repeat stop` is typable
	// the moment the input is free.
	emit(fmt.Sprintf("**repeat thread %s started** - %s\n\nTask: %s\n\n"+
		"The running round's activity shows above as it happens; rounds appear below as they finish. "+
		"**Esc** stops watching (the loop keeps running). While you watch, the TUI queues what you type: "+
		"press Esc first, then `/repeat finish` (ends the loop once the current round completes; `wrapup` and `stop` are the same) or `/repeat show` - "+
		"or run `! captain stop` at any time.\n",
		th.id, horizon, promptPeek(rest)))
	b.repeatWatch(ctx, emit, status, req.ws.Dir, th.id)
	finish()
	return true
}

// repeatStopHTTP is the out-of-band stop. While a turn streams, the TUI QUEUES
// typed input - so `/repeat stop` cannot reach the brain until the stream ends
// (live 2026-09-01: "QUEUED /repeat stop is queued"). A loop you can watch must
// still be stoppable, so stopping is reachable over HTTP, from any terminal,
// with `captain stop`.
func (b *brain) repeatStopHTTP(w http.ResponseWriter, r *http.Request) {
	hard := r.URL.Query().Get("abort") == "1"
	// `captain stop` from a terminal stops that folder's threads; with no
	// folder named, every thread on the machine.
	dir, mine := workspaceFilter(r)
	id := "all"
	if !mine {
		dir, id = "", "everywhere"
	}
	msg := b.repeatStop(dir, id)
	if hard {
		msg = b.repeatAbort(dir, id)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "result": msg})
}

// repeatCtlHTTP is a control word typed while a watch streams. The TUI
// queues the typed turn behind the stream, so the plugin sends the word
// here the moment it is typed (chat.message fires before the queue); the
// answer goes back to the caller AND into the watching turn, where the
// user is looking (live 2026-09-13: "/repeat show is still queued").
// POST /v1/repeat/ctl?cwd=<dir> {"word":"show","arg":""}
func (b *brain) repeatCtlHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST")
		return
	}
	var req struct {
		Word string `json:"word"`
		Arg  string `json:"arg"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	dir, _ := workspaceFilter(r)
	arg := strings.TrimSpace(req.Arg)
	var msg string
	switch strings.ToLower(strings.TrimSpace(req.Word)) {
	case "show":
		msg = b.repeatShow(dir, arg)
	case "status":
		msg = b.repeatStatus(dir)
	case "stop", "finish", "wrapup":
		msg = b.repeatStop(dir, arg)
	case "abort":
		msg = b.repeatAbort(dir, arg)
	default:
		writeErr(w, 400, "unknown control word "+req.Word)
		return
	}
	watched := b.repeatNote(dir, arg, msg)
	writeJSON(w, 200, map[string]any{"ok": true, "result": msg, "watched": watched})
}

// repeatNote hands a control answer to the thread a watcher may be showing;
// reports whether such a thread exists (a watcher prints it in place).
func (b *brain) repeatNote(dir, id, msg string) bool {
	b.rmu.Lock()
	defer b.rmu.Unlock()
	var pick *repeatThread
	for k, th := range b.repeatState() {
		if threadMatches(th.dir, k, dir, id) && (pick == nil || th.started.After(pick.started)) {
			pick = th
		}
	}
	if pick == nil {
		return false
	}
	pick.notes = append(pick.notes, msg)
	if len(pick.notes) > 8 {
		pick.notes = pick.notes[len(pick.notes)-8:]
	}
	pick.noteSeq++
	return !pick.finished
}

// repeatNotice announces rounds that finished since the last turn, once each,
// on the progress channel - the same discipline as /parallel: visible for
// supervision, never spliced into an unrelated answer.
func (b *brain) repeatNotice(dir string) string {
	b.rmu.Lock()
	defer b.rmu.Unlock()
	var lines []string
	for id, th := range b.repeatState() {
		if th.dir != dir {
			continue // another TUI's loop: its rounds are announced there
		}
		for i := range th.rounds {
			r := &th.rounds[i]
			if r.shown {
				continue
			}
			r.shown = true
			switch {
			case r.err != "":
				lines = append(lines, fmt.Sprintf("✗ %s round %d failed after %s - %s",
					id, r.n, r.dur.Round(time.Second), promptPeek(r.err)))
			case r.noop:
				lines = append(lines, fmt.Sprintf("○ %s round %d deferred after %s (no work done) - the next round is told to do it: %s",
					id, r.n, r.dur.Round(time.Second), promptPeek(r.summary)))
			default:
				lines = append(lines, fmt.Sprintf("✓ %s round %d in %s: %s",
					id, r.n, r.dur.Round(time.Second), promptPeek(r.summary)))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n(`/repeat show` for the full round outputs)\n"
}

// repeatWatch streams rounds into the TUI as they finish.
//
// Round notices ride the next turn's progress channel, which means a loop
// whose rounds take 15 minutes only reports when the user happens to type -
// useless for supervision (2026-08-31). This keeps ONE turn open and emits
// each round as it lands. The thread stays detached: interrupting the watch
// (Esc) frees the input without stopping the loop, which is exactly why
// /repeat was detached in the first place.
// repeatWatch streams a thread to the turn that watches it: each finished
// round as text, and - between rounds - the running round's progress on the
// status channel, so the watcher sees the worker work rather than a silent
// wait it cannot interrupt (typed input is queued while a turn streams).
func (b *brain) repeatWatch(ctx context.Context, emit, status func(string), dir, id string) {
	pick := func() *repeatThread {
		b.rmu.Lock()
		defer b.rmu.Unlock()
		var t *repeatThread
		for k, th := range b.repeatState() {
			if threadMatches(th.dir, k, dir, id) {
				if t == nil || th.started.After(t.started) {
					t = th
				}
			}
		}
		return t
	}
	th := pick()
	if th == nil {
		emit("no repeat thread to watch (`/repeat status`).")
		return
	}
	emit(fmt.Sprintf("**watching %s** - the running round's activity shows above, rounds appear here as they finish. Esc to stop watching; the loop keeps running (typed input is queued until then; `! captain stop` works at any time).\n\nTask: %s\n",
		th.id, promptPeek(th.task)))

	sent := 0
	shown := 0 // th.liveSeq up to which progress has been forwarded
	noted := 0 // th.noteSeq up to which control answers have been printed
	for {
		b.rmu.Lock()
		rounds := append([]roundRecord(nil), th.rounds...)
		done, finished := th.done, th.finished
		var fresh []string
		if status != nil && th.liveSeq > shown {
			n := th.liveSeq - shown
			if n > len(th.live) {
				n = len(th.live)
			}
			fresh = append(fresh, th.live[len(th.live)-n:]...)
			shown = th.liveSeq
		}
		var notes []string
		if th.noteSeq > noted {
			notes = append(notes, th.notes[len(th.notes)-(th.noteSeq-noted):]...)
			noted = th.noteSeq
		}
		b.rmu.Unlock()
		for _, n := range notes {
			emit("\n---\n\n" + n + "\n")
		}
		for _, s := range fresh {
			s = strings.TrimSpace(s)
			// The round's own feed header ("**captain · claude**") and the
			// reroute marker are not activity.
			if s == "" || strings.HasPrefix(s, "**captain") || strings.HasPrefix(s, "captain ·") {
				continue
			}
			status(fmt.Sprintf("round %d · %s\n", done+1, s)) // one line each: the TUI concatenates deltas
		}

		// rounds is a sliding window; index by round number, not position.
		for _, r := range rounds {
			if r.n <= sent {
				continue
			}
			sent = r.n
			if r.err != "" {
				emit(fmt.Sprintf("\n---\n\n**round %d** ✗ failed after %s\n\n%s\n",
					r.n, r.dur.Round(time.Second), promptPeek(r.err)))
				continue
			}
			if r.noop {
				emit(fmt.Sprintf("\n---\n\n**round %d** ○ deferred after %s - no work done; the next round is told to do it\n\n%s\n",
					r.n, r.dur.Round(time.Second), r.summary))
				continue
			}
			emit(fmt.Sprintf("\n---\n\n**round %d** ✓ %s · %s\n\n%s\n\n_(full output: `/repeat show`)_\n",
				r.n, r.at.Format("15:04:05"), r.dur.Round(time.Second), r.summary))
		}
		if finished {
			why := ""
			b.rmu.Lock()
			if th.stopReason != "" {
				why = " - " + th.stopReason
			}
			b.rmu.Unlock()
			emit(fmt.Sprintf("\n---\n\n**%s finished** - %d round(s) total%s.\n", th.id, done, why))
			return
		}
		select {
		case <-ctx.Done(): // the user stopped watching; the loop continues
			return
		case <-time.After(time.Second):
		}
	}
}

// repeatShow prints what recent rounds actually did, in the TUI.
func (b *brain) repeatShow(dir, id string) string {
	b.rmu.Lock()
	defer b.rmu.Unlock()
	st := b.repeatState()
	var pick *repeatThread
	for k, th := range st {
		if threadMatches(th.dir, k, dir, id) {
			if pick == nil || th.started.After(pick.started) {
				pick = th
			}
		}
	}
	if pick == nil || len(pick.rounds) == 0 {
		return "no completed rounds yet (`/repeat status` for thread state)."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "### %s - last %d round(s)\n\nTask: %s\n", pick.id, len(pick.rounds), promptPeek(pick.task))
	for _, r := range pick.rounds {
		fmt.Fprintf(&sb, "\n---\n\n**round %d** · %s · %s\n\n", r.n, r.at.Format("15:04:05"), r.dur.Round(time.Second))
		if r.err != "" {
			fmt.Fprintf(&sb, "✗ %s\n", r.err)
			continue
		}
		if r.summary != "" {
			fmt.Fprintf(&sb, "**did:** %s\n\n", r.summary)
		}
		sb.WriteString(r.text + "\n")
	}
	return sb.String()
}

func (b *brain) repeatStatus(dir string) string {
	b.rmu.Lock()
	defer b.rmu.Unlock()
	st := b.repeatState()
	mine, elsewhere := 0, 0
	for _, th := range st {
		if th.dir == dir {
			mine++
		} else {
			elsewhere++
		}
	}
	if mine == 0 {
		msg := "no repeat threads running in this folder.\n\nStart one with `/repeat 10 <prompt>` (or `/repeat <prompt>` for an open-ended thread)."
		if elsewhere > 0 {
			msg += fmt.Sprintf("\n\n(%d thread(s) run in other folders; `/repeat status` there lists them.)", elsewhere)
		}
		return msg
	}
	var sb strings.Builder
	sb.WriteString("### repeat threads\n\n")
	for _, th := range st {
		if th.dir != dir {
			continue
		}
		target := fmt.Sprintf("%d", th.target)
		if th.target == 0 {
			target = "∞"
		}
		state := "running"
		if th.finished {
			state = "finished"
		}
		fmt.Fprintf(&sb, "- **%s** - %s · %d/%s done, %d failed · %s\n  %s\n",
			th.id, state, th.done, target, th.failed,
			time.Since(th.started).Round(time.Second), promptPeek(th.task))
		if th.stopReason != "" {
			fmt.Fprintf(&sb, "  stopped: %s\n", th.stopReason)
		}
		if th.lastErr != "" {
			fmt.Fprintf(&sb, "  last error: %s\n", promptPeek(th.lastErr))
		}
		for _, r := range th.rounds { // what each recent round actually did
			mark := "✓"
			if r.err != "" {
				mark = "✗"
			} else if r.noop {
				mark = "○" // deferred: no work done
			}
			gist := r.summary
			if r.err != "" {
				gist = r.err
			}
			fmt.Fprintf(&sb, "    %s round %d · %s · %s\n", mark, r.n,
				r.dur.Round(time.Second), promptPeek(gist))
		}
	}
	if elsewhere > 0 {
		fmt.Fprintf(&sb, "\n(%d thread(s) run in other folders.)\n", elsewhere)
	}
	sb.WriteString("\n`/repeat watch` follows rounds live · `/repeat show` prints them in full · `/repeat finish` (or `wrapup`, `stop`) ends the loop once the current round completes · `/repeat abort` kills it now.")
	return sb.String()
}

// threadMatches is the selection rule shared by /repeat and /parallel: an
// explicit id names one thread wherever it runs; "", "last" and "all" mean
// this folder's threads; "everywhere" is the machine-wide stop.
func threadMatches(threadDir, threadID, dir, id string) bool {
	switch id {
	case "", "last", "all":
		return threadDir == dir
	case "everywhere":
		return true
	}
	return threadID == id
}

// firstLine is the one-line gist of a round for the status digest.
func firstLine(text, err string) string {
	if err != "" {
		return err
	}
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return "(no output)"
}

func (b *brain) repeatStop(dir, id string) string {
	b.rmu.Lock()
	defer b.rmu.Unlock()
	st := b.repeatState()
	var stopped []string
	for k, th := range st {
		if th.finished {
			continue
		}
		if threadMatches(th.dir, k, dir, id) {
			// Graceful by default: cancelling the context would abort the round
			// in flight, and that round is usually the expensive one. Live
			// 2026-08-31: a stop killed a 25-minute claude round mid-work and
			// the thread ended 0/∞ done. `/repeat abort` is the hard version.
			th.stopped = true
			stopped = append(stopped, fmt.Sprintf("%s (%d done)", k, th.done))
		}
	}
	if len(stopped) == 0 {
		return "nothing to stop - no repeat thread is running (`/repeat status` to check)."
	}
	return "finishing: " + strings.Join(stopped, ", ") +
		"\n\nThe round in flight is left to finish - its work is kept and shown with `/repeat show`. No new round starts." +
		"\nUse `/repeat abort` to kill the running round instead."
}

// repeatAbort is the hard stop: cancel the round in flight too.
func (b *brain) repeatAbort(dir, id string) string {
	b.rmu.Lock()
	defer b.rmu.Unlock()
	var killed []string
	for k, th := range b.repeatState() {
		if th.finished {
			continue
		}
		if threadMatches(th.dir, k, dir, id) {
			th.stopped = true
			th.cancel()
			th.finished = true
			killed = append(killed, k)
		}
	}
	if len(killed) == 0 {
		return "nothing to abort."
	}
	return "aborted (round in flight cancelled, its work lost): " + strings.Join(killed, ", ")
}

// startRepeat registers and launches a detached thread.
func (b *brain) startRepeat(req oaiChatReq, task string, count int) *repeatThread {
	ctx, cancel := context.WithCancel(context.Background())
	th := &repeatThread{
		id:      "rp_" + fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff),
		dir:     req.ws.Dir,
		task:    task,
		target:  count,
		started: time.Now(),
		cancel:  cancel,
	}
	b.rmu.Lock()
	b.repeatState()[th.id] = th
	b.rmu.Unlock()

	// The iteration request replays the conversation with the /repeat directive
	// stripped from the last user turn - the worker must see the task, never
	// the control word.
	// Streaming, so the round's progress lines exist to be captured and shown
	// to a watcher; the captureWriter folds the stream back into one answer.
	iter := oaiChatReq{Model: modelForTask(task, req.Model), Stream: true, Messages: append([]oaiMessage(nil), req.Messages...), ws: req.ws}
	for i := len(iter.Messages) - 1; i >= 0; i-- {
		if iter.Messages[i].Role == "user" || iter.Messages[i].Role == "" {
			iter.Messages[i] = oaiMessage{Role: "user", Content: jsonString(task + roundContract)}
			break
		}
	}
	fmt.Printf("captain brain: repeat %s started (target=%d) - %s\n", th.id, count, promptPeek(task))
	go b.runRepeat(ctx, th, iter)
	return th
}

func (b *brain) runRepeat(ctx context.Context, th *repeatThread, iter oaiChatReq) {
	cap := th.target
	if cap == 0 || cap > repeatHardCap() {
		cap = repeatHardCap()
	}
	for i := 0; i < cap; i++ {
		b.rmu.Lock()
		halted := th.stopped
		b.rmu.Unlock()
		if halted || ctx.Err() != nil {
			break
		}
		start := time.Now()
		b.rmu.Lock()
		th.live, th.roundFrom = nil, start
		// A round that deferred is not a state the next round may inherit
		// silently: name it, in the task itself.
		round := iter
		if n := len(th.rounds); n > 0 && th.rounds[n-1].noop {
			prev := th.rounds[n-1]
			round.Messages = append([]oaiMessage(nil), iter.Messages...)
			for i := len(round.Messages) - 1; i >= 0; i-- {
				if round.Messages[i].Role == "user" || round.Messages[i].Role == "" {
					round.Messages[i] = oaiMessage{Role: "user", Content: jsonString(th.task + roundContract +
						fmt.Sprintf("\n\n[captain] Round %d of this task returned no work: %q. Whatever it was waiting for is gone with it. Do the step it deferred, now.", prev.n, promptPeek(prev.text)))}
					break
				}
			}
		}
		b.rmu.Unlock()
		rec := &captureWriter{onStatus: func(s string) {
			b.rmu.Lock()
			th.live = append(th.live, s)
			th.liveSeq++
			if len(th.live) > repeatLiveKeep {
				th.live = th.live[len(th.live)-repeatLiveKeep:]
			}
			b.rmu.Unlock()
		}}
		func() {
			defer func() { // an iteration must never take the brain down
				if r := recover(); r != nil {
					b.rmu.Lock()
					th.failed, th.lastErr = th.failed+1, fmt.Sprintf("panic: %v", r)
					b.rmu.Unlock()
				}
			}()
			if b.chatFn != nil { // test seam
				b.chatFn(rec)
				return
			}
			b.chatCompletions(rec, chatRequestFrom(ctx, round))
		}()

		text, errText := rec.answer()
		b.rmu.Lock()
		th.done++
		if errText != "" || rec.status >= 400 || rec.status == 0 {
			th.failed++
			th.lastErr = promptPeek(errText)
		} else {
			th.failed = 0 // consecutive-failure counter
		}
		if len(text) > repeatRoundChars {
			text = text[:repeatRoundChars] + "\n…[truncated - full text in `captain show`]"
		}
		task := th.task
		b.rmu.Unlock()
		digest := ""
		if errText == "" {
			digest = b.roundSummary(task, text) // outside the lock: it makes a call
		}
		b.rmu.Lock()
		noop := errText == "" && roundDeferred(text)
		th.rounds = append(th.rounds, roundRecord{n: th.done, at: start,
			dur: time.Since(start), text: text, summary: digest, err: errText, noop: noop})
		if noop {
			noteFailure(iter.ws, fmt.Sprintf("/repeat round %d of %q deferred instead of working (%s): a headless round cannot wait for a later wakeup; do the step inside the round",
				th.done, promptPeek(th.task), promptPeek(text)))
		}
		if len(th.rounds) > repeatRoundKeep {
			th.rounds = th.rounds[len(th.rounds)-repeatRoundKeep:]
		}
		done, failed := th.done, th.failed
		b.rmu.Unlock()

		fmt.Printf("captain brain: repeat %s iteration %d/%v done in %s (status %d)\n",
			th.id, done, targetLabel(th.target), time.Since(start).Round(time.Second), rec.status)
		b.pushActivity(activity{Dir: iter.ws.Dir, Kind: "done", Leg: "repeat", Model: "repeat",
			Text: fmt.Sprintf("%s iteration %d: %s", th.id, done, promptPeek(th.task)),
			Ms:   time.Since(start).Milliseconds()})

		b.rmu.Lock()
		halted = th.stopped
		b.rmu.Unlock()
		if halted {
			fmt.Printf("captain brain: repeat %s stopped after round %d (graceful)\n", th.id, done)
			break
		}
		if failed >= repeatMaxFails {
			fmt.Printf("captain brain: repeat %s stopped - %d consecutive failures\n", th.id, failed)
			break
		}
		// No-progress stop: two rounds saying the same thing mean the loop has
		// nothing left to do. Compared on the round TEXT, not the summary,
		// because the summary is itself a model call that rewords itself.
		b.rmu.Lock()
		var prev, last string
		if n := len(th.rounds); n >= 2 {
			prev, last = th.rounds[n-2].text, th.rounds[n-1].text
		}
		b.rmu.Unlock()
		if same, score := roundsAreNearIdentical(prev, last); same {
			reason := fmt.Sprintf("no progress: rounds %d and %d were %.0f%% identical", done-1, done, score*100)
			b.rmu.Lock()
			th.stopReason = reason
			b.rmu.Unlock()
			fmt.Printf("captain brain: repeat %s stopped - %s\n", th.id, reason)
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(repeatPause):
		}
	}
	b.rmu.Lock()
	th.finished = true
	b.rmu.Unlock()
	fmt.Printf("captain brain: repeat %s finished (%d iterations)\n", th.id, th.done)
}

func targetLabel(n int) any {
	if n == 0 {
		return "∞"
	}
	return n
}

// noDedupeKey marks an internally-generated turn: every repeat iteration is
// real work, so it must never ATTACH to the previous iteration's cached answer
// (solo dedupe would otherwise replay it for soloResultTTL).
type noDedupeKey struct{}

// jsonString renders a Go string as a JSON message content value.
func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// chatRequestFrom builds the internal POST that drives one iteration. The
// thread's context rides along, so `/repeat stop` also cancels the iteration
// currently in flight at the HTTP layer.
func chatRequestFrom(ctx context.Context, req oaiChatReq) *http.Request {
	body, _ := json.Marshal(req)
	r, _ := http.NewRequestWithContext(context.WithValue(ctx, noDedupeKey{}, true),
		http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(workspaceHeader, req.ws.Dir) // a detached round stays in the folder that started it
	return r
}

// discardWriter is an http.ResponseWriter for detached runs: the turn that
// launched the thread is long gone, so an iteration's bytes have nowhere to
// stream. The body is kept only to report an error back through the thread.
type discardWriter struct {
	hdr    http.Header
	status int
	body   strings.Builder
}

func (d *discardWriter) Header() http.Header {
	if d.hdr == nil {
		d.hdr = http.Header{}
	}
	return d.hdr
}
func (d *discardWriter) WriteHeader(s int) {
	if d.status == 0 {
		d.status = s
	}
}
func (d *discardWriter) Write(p []byte) (int, error) {
	if d.status == 0 {
		d.status = 200
	}
	if d.body.Len() < 4096 { // enough to carry an error message
		d.body.Write(p)
	}
	return len(p), nil
}

// modelForTask derives the dispatch model from a task's own leading directive.
//
// Normally the FORK parses "/team", "/frontier" and forced-leg prefixes and
// sends the right `model` field; the brain only ever sees the result. A
// detached iteration (/repeat, /parallel) is built inside the brain, so nobody
// does that parsing for it - the task text kept its "/team" while the request
// carried whatever model the launching turn happened to use, and
// stripCaptainDirectives then removed the prefix. Live 2026-08-31:
// "/repeat /team /quality continue the implementation…" ran as a SOLO claude
// turn, 24 minutes, zero iterations completed. Preferences like /quality are
// read further down by each path (teamPrefer), so only the MODEL is resolved
// here.
func modelForTask(task, fallback string) string {
	t := strings.TrimSpace(task)
	for {
		m := captainDirective.FindString(t)
		if m == "" {
			return fallback
		}
		word := strings.ToLower(strings.Trim(strings.TrimSpace(m), "/: \t"))
		switch word {
		case "team", "frontier":
			return word
		case "quality", "q", "best", "speed", "fast", "save", "cheap":
			t = strings.TrimSpace(t[len(m):]) // a preference, keep looking
			continue
		default:
			if captaincode.KnownLeg(captaincode.Leg(word)) {
				return word
			}
			return fallback
		}
	}
}

// ── no-progress stop ─────────────────────────────────────────────────────────
// The consecutive-failure guard catches a loop that is breaking. It does not
// catch a loop that is SUCCEEDING at nothing: on 2026-08-31 an open-ended
// `/repeat /team …until the backlog is empty` ran 22 rounds over 3h50m and
// spent an entire OpenRouter balance, and from round ~15 its own output said
// the backlog was already empty. Every round succeeded, so nothing stopped it.
//
// Two rounds that say the same thing are the signal: the work is done, or the
// goal has no machine-checkable stop condition. Either way, continuing costs
// money and produces nothing.

// repeatSimilarity is the threshold above which two rounds count as saying the
// same thing. CAPTAIN_REPEAT_SIMILARITY overrides; 0 disables the guard.
func repeatSimilarity() float64 {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_REPEAT_SIMILARITY")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			return f
		}
	}
	return 0.85
}

// repeatMinJudgeable is the shortest normalised answer worth comparing. Two
// one-word rounds are a failure signature, not evidence about progress.
const repeatMinJudgeable = 40

// normalizeRound strips what changes between rounds without meaning anything:
// case, whitespace, punctuation, and digits (timestamps, counters, "pass 3").
func normalizeRound(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// trigrams is the character-trigram set of a normalised string.
func trigrams(s string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for i := 0; i+3 <= len(s); i++ {
		out[s[i:i+3]] = struct{}{}
	}
	return out
}

// roundsAreNearIdentical reports whether two rounds said the same thing, and
// the similarity that decided it. Trigrams rather than equality because a model
// rephrases itself every time it repeats itself.
func roundsAreNearIdentical(a, b string) (bool, float64) {
	na, nb := normalizeRound(a), normalizeRound(b)
	if len(na) < repeatMinJudgeable || len(nb) < repeatMinJudgeable {
		return false, 0
	}
	ta, tb := trigrams(na), trigrams(nb)
	if len(ta) == 0 || len(tb) == 0 {
		return false, 0
	}
	shared := 0
	for t := range ta {
		if _, ok := tb[t]; ok {
			shared++
		}
	}
	union := len(ta) + len(tb) - shared
	if union == 0 {
		return false, 0
	}
	score := float64(shared) / float64(union)
	threshold := repeatSimilarity()
	if threshold <= 0 {
		return false, score
	}
	return score >= threshold, score
}
