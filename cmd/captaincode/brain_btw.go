package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// /btw - a note for the worker that is already running (pkg steer.go).
//
// Typed in the TUI, the word reaches the brain twice: the plugin sends it
// out of band the moment it is submitted (chat.message fires before the
// TUI queues the turn) and the brain hands it to the running worker through
// the turn's Steer; then the TUI's queued copy arrives as the next chat
// completion, and the brain answers it with an acknowledgement instead of
// running the note again. A note nobody could take mid-run (codex exec,
// cursor-agent, or no worker at all) is not acknowledged: the queued copy
// runs as the ordinary follow-up turn it would have been anyway.

// steerBook is the brain's view: the turns that could take a note now, and
// the notes recently delivered (for the queued copy to recognise).
type steerBook struct {
	mu         sync.Mutex
	live       map[*captaincode.Steer]struct{}
	done       map[string][]steerDone // by origin dir
	interrupts map[string]string      // by origin dir: the last /interrupt's outcome, for the queued copy
}

type steerDone struct {
	at   time.Time
	text string
	took []captaincode.Leg
}

// steerDoneTTL bounds how long a delivered note waits for its queued copy:
// the copy arrives when the running turn ends, which a long worker can
// stretch to the worker cap.
const steerDoneTTL = 45 * time.Minute

// steerOpen registers a turn's handle; steerClose forgets it.
func (b *brain) steerOpen(dir string) *captaincode.Steer {
	s := captaincode.NewSteer(dir)
	b.steers.mu.Lock()
	defer b.steers.mu.Unlock()
	if b.steers.live == nil {
		b.steers.live = map[*captaincode.Steer]struct{}{}
	}
	b.steers.live[s] = struct{}{}
	return s
}

func (b *brain) steerClose(s *captaincode.Steer) {
	if s == nil {
		return
	}
	b.steers.mu.Lock()
	defer b.steers.mu.Unlock()
	delete(b.steers.live, s)
}

// btwDeliver hands text to the workers running under dir's turns. It
// returns the legs that took it (mid-run), the legs running that could not,
// and the sentence the caller shows.
func (b *brain) btwDeliver(dir, text string) (took, deaf []captaincode.Leg, msg string) {
	text = strings.TrimSpace(text)
	dir = filepath.Clean(dir)
	b.steers.mu.Lock()
	var turns []*captaincode.Steer
	for s := range b.steers.live {
		if s.Dir == dir {
			turns = append(turns, s)
		}
	}
	b.steers.mu.Unlock()
	seen := map[captaincode.Leg]bool{}
	held := 0
	var routed string // how a multi-worker turn chose its addressees
	for _, s := range turns {
		if len(s.Running()) == 0 {
			if len(s.Attached()) == 0 {
				// The turn is still compacting or routing: the worker that
				// starts next reads the note first (steer.go Attach).
				if _, err := s.Send(text); err == nil {
					held++
					s.Announce("\n\n> **/btw** (held for the worker about to start): " + text + "\n\n")
				}
			}
			continue
		}
		attached := map[captaincode.Leg]bool{}
		if att := s.Attached(); len(att) > 0 {
			// One worker: it takes the note. Several (team, workflow): the
			// note goes to the worker(s) it concerns - the ones the user
			// addressed ("@grok …"), else the director's pick from the
			// briefs, else all of them.
			only, note, why := b.routeNote(s, text, att)
			legs, err := s.SendTo(note, only)
			if err != nil {
				fmt.Printf("captain brain: /btw not fully delivered: %v\n", err)
			}
			for _, l := range legs {
				attached[l] = true
				if !seen[l] {
					seen[l] = true
					took = append(took, l)
				}
			}
			if len(legs) > 0 {
				line := "\n\n> **/btw** → " + legList(legs)
				if why != "" {
					line += " (" + why + ")"
					routed = why
				}
				s.Announce(line + ": " + note + "\n\n")
			}
		}
		for _, l := range s.Running() {
			if !attached[l] && !seen[l] {
				seen[l] = true
				deaf = append(deaf, l)
			}
		}
	}
	peek := promptPeek(text)
	switch {
	case len(took) == 0 && held > 0:
		b.steers.mu.Lock()
		if b.steers.done == nil {
			b.steers.done = map[string][]steerDone{}
		}
		b.steers.done[dir] = append(b.steers.done[dir], steerDone{at: time.Now(), text: text})
		b.steers.mu.Unlock()
		b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "btw", Model: "pending", Text: "btw held for the worker about to start: " + peek})
		msg = "btw held for the worker about to start (the turn is still being prepared): " + peek
		took = []captaincode.Leg{"pending"}
	case len(took) > 0:
		b.steers.mu.Lock()
		if b.steers.done == nil {
			b.steers.done = map[string][]steerDone{}
		}
		b.steers.done[dir] = append(b.steers.done[dir], steerDone{at: time.Now(), text: text, took: took})
		b.steers.mu.Unlock()
		b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "btw", Model: legList(took), Text: "btw → " + legList(took) + ": " + peek})
		msg = fmt.Sprintf("btw → %s, taken mid-run: %s", legList(took), peek)
		if routed != "" {
			msg += " (" + routed + ")"
		}
		if len(deaf) > 0 {
			msg += fmt.Sprintf(" (%s takes no notes mid-run)", legList(deaf))
		}
	case len(deaf) > 0:
		msg = fmt.Sprintf("%s is running and takes no notes mid-run - your note runs as the next turn, right after it finishes", legList(deaf))
	default:
		msg = "no worker is running in " + filepath.Base(dir) + " - your note runs as a normal turn"
	}
	return took, deaf, msg
}

// btwTakeDone consumes a delivered note matching text (the queued copy), if
// one was delivered recently under dir.
func (b *brain) btwTakeDone(dir, text string) (steerDone, bool) {
	dir = filepath.Clean(dir)
	text = strings.TrimSpace(text)
	b.steers.mu.Lock()
	defer b.steers.mu.Unlock()
	list := b.steers.done[dir]
	keep := list[:0]
	var hit steerDone
	found := false
	for _, d := range list {
		if time.Since(d.at) > steerDoneTTL {
			continue
		}
		if !found && d.text == text {
			hit, found = d, true
			continue
		}
		keep = append(keep, d)
	}
	if len(keep) == 0 {
		delete(b.steers.done, dir)
	} else {
		b.steers.done[dir] = keep
	}
	return hit, found
}

func legList(legs []captaincode.Leg) string {
	names := make([]string, 0, len(legs))
	for _, l := range legs {
		names = append(names, string(l))
	}
	return strings.Join(names, ", ")
}

// btwHTTP is the out-of-band entry the plugin calls the moment /btw is typed.
// POST /v1/btw?cwd=<dir> {"text":"..."}
func (b *brain) btwHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, 400, "empty note")
		return
	}
	dir, ok := workspaceFilter(r)
	if !ok {
		dir = defaultWorkspace().Dir
	}
	took, deaf, msg := b.btwDeliver(dir, req.Text)
	fmt.Printf("captain brain: /btw in %s - %s\n", filepath.Base(dir), msg)
	writeJSON(w, 200, map[string]any{"ok": true, "delivered": len(took) > 0, "took": took, "running": deaf, "result": msg})
}

var btwWord = regexp.MustCompile(`(?is)^\s*/btw\b[\s:]*(.*)$`)

// handleBtw is the queued copy of a /btw turn. Delivered mid-run already →
// acknowledged, nothing runs. Not delivered → false: the turn runs as the
// follow-up it is (promptFrom strips the word).
func (b *brain) handleBtw(w http.ResponseWriter, req oaiChatReq, raw string) bool {
	m := btwWord.FindStringSubmatch(raw)
	if m == nil {
		return false
	}
	text := strings.TrimSpace(m[1])
	if text == "" {
		emit, _, finish := newCompletionWriter(w, req, "captain")
		defer finish()
		emit("**/btw <note>** - a note for the worker that is already running: claude and the opencode legs take it mid-run, codex and cursor get it as the next turn.")
		finish()
		return true
	}
	done, ok := b.btwTakeDone(req.ws.Dir, text)
	if !ok {
		fmt.Printf("captain brain: /btw was not taken mid-run - running it as the next turn\n")
		return false
	}
	emit, _, finish := newCompletionWriter(w, req, "captain")
	defer finish()
	emit(fmt.Sprintf("**/btw** taken by %s at %s, while it was working - nothing more to run.", legList(done.took), done.at.Format("15:04:05")))
	finish()
	return true
}

// /interrupt - stop the running worker without losing its work.
//
// A worker that takes notes mid-run (claude, the opencode legs) is asked to
// stop and write a HANDOFF - what it implemented, what is left, how to
// resume - and its answer IS that note: it reaches the TUI as the turn's
// result, the run history, and memory (a journal entry and a promoted note).
// A worker with no channel (codex exec, cursor-agent) is stopped at once and
// what it streamed is kept as a partial. A worker asked to hand off that is
// still running after the grace (CAPTAIN_INTERRUPT_GRACE, default 3m) is
// stopped the same way. Sent out of band by the plugin the moment it is
// typed (the /btw pattern); the queued copy prints the outcome.

func interruptGrace() time.Duration {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_INTERRUPT_GRACE")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 3 * time.Minute
}

// interruptDeliver interrupts every running turn under dir.
func (b *brain) interruptDeliver(dir, reason string) (asked, stopped []captaincode.Leg, msg string) {
	asked, stopped, _, msg = b.interruptDeliverN(dir, reason, nil)
	return asked, stopped, msg
}

// except is the caller's own turn (the queued copy of /interrupt), never a
// turn to withdraw.
func (b *brain) interruptDeliverN(dir, reason string, except *captaincode.Steer) (asked, stopped []captaincode.Leg, withdrawn int, msg string) {
	dir = filepath.Clean(dir)
	b.steers.mu.Lock()
	var turns []*captaincode.Steer
	for s := range b.steers.live {
		if s.Dir != dir || s == except {
			continue
		}
		if len(s.Running()) > 0 {
			turns = append(turns, s)
		} else if len(s.Attached()) == 0 {
			// Still being prepared (compaction, routing): mark it, and the
			// worker never starts (brain.go runOne).
			s.Interrupt(reason)
			withdrawn++
		}
	}
	b.steers.mu.Unlock()
	for _, s := range turns {
		a, st := s.Interrupt(reason)
		asked = append(asked, a...)
		stopped = append(stopped, st...)
		if len(a) > 0 {
			go func(s *captaincode.Steer, grace time.Duration) {
				time.Sleep(grace)
				if left := s.StopAll(); len(left) > 0 {
					fmt.Printf("captain brain: /interrupt - %s did not hand off within %s, stopped with what it produced\n", legList(left), grace)
					b.pushActivity(activity{Dir: s.Dir, Kind: "route", Leg: "interrupt", Model: legList(left), Text: "no handoff within " + grace.String() + " - stopped, output kept"})
				}
			}(s, interruptGrace())
		}
	}
	switch {
	case len(asked) == 0 && len(stopped) == 0 && withdrawn > 0:
		msg = fmt.Sprintf("interrupt: %d turn(s) still being prepared withdrawn - the worker never starts", withdrawn)
		b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "interrupt", Model: "-", Text: msg})
	case len(asked) == 0 && len(stopped) == 0:
		msg = "no worker is running in " + filepath.Base(dir)
	default:
		var parts []string
		if len(asked) > 0 {
			parts = append(parts, fmt.Sprintf("%s asked to stop and hand off (what it did, what is left, how to resume; %s grace)", legList(asked), interruptGrace()))
		}
		if len(stopped) > 0 {
			parts = append(parts, fmt.Sprintf("%s takes no notes mid-run - stopped now, output kept", legList(stopped)))
		}
		msg = "interrupt: " + strings.Join(parts, "; ")
		b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "interrupt", Model: legList(append(append([]captaincode.Leg{}, asked...), stopped...)), Text: msg})
	}
	b.steers.mu.Lock()
	if b.steers.interrupts == nil {
		b.steers.interrupts = map[string]string{}
	}
	b.steers.interrupts[dir] = msg
	b.steers.mu.Unlock()
	fmt.Printf("captain brain: /interrupt in %s - %s\n", filepath.Base(dir), msg)
	return asked, stopped, withdrawn, msg
}

// interruptHTTP: POST /v1/interrupt?cwd=<dir> {"reason": "..."}.
func (b *brain) interruptHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST")
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	dir, ok := workspaceFilter(r)
	if !ok {
		dir = defaultWorkspace().Dir
	}
	asked, stopped, withdrawn, msg := b.interruptDeliverN(dir, req.Reason, nil)
	writeJSON(w, 200, map[string]any{"ok": true, "asked": asked, "stopped": stopped, "withdrawn": withdrawn, "result": msg})
}

var interruptWord = regexp.MustCompile(`(?is)^\s*/interrupt\b[\s:]*(.*)$`)

// handleInterrupt is the queued copy of an /interrupt turn: it prints the
// outcome the out-of-band call recorded (or interrupts now, if the plugin
// did not run - nothing runs by then, so it says so).
func (b *brain) handleInterrupt(w http.ResponseWriter, req oaiChatReq, raw string) bool {
	m := interruptWord.FindStringSubmatch(raw)
	if m == nil {
		return false
	}
	emit, _, finish := newCompletionWriter(w, req, "captain")
	defer finish()
	dir := filepath.Clean(req.ws.Dir)
	b.steers.mu.Lock()
	msg, had := b.steers.interrupts[dir]
	delete(b.steers.interrupts, dir)
	b.steers.mu.Unlock()
	if !had {
		_, _, _, msg = b.interruptDeliverN(dir, strings.TrimSpace(m[1]), req.ws.Steer)
	}
	emit("**/interrupt** - " + msg + "\n")
	finish()
	return true
}

// noteHandoff records an interrupted run in memory: the handoff the worker
// wrote (or the partial it was stopped with) as a journal entry and a note,
// so the next turn - and the next developer - can pick it up.
func noteHandoff(ws captaincode.Workspace, l captaincode.Leg, prompt string, res captaincode.Result, err error) {
	text := strings.TrimSpace(res.Text)
	outcome := "handoff"
	if err != nil {
		outcome = "stopped"
	}
	e := captaincode.JournalEntry{Kind: "interrupt", Task: lastUserTurn(prompt), Leg: string(l), Outcome: outcome,
		DurationMs: res.DurationMs, Summary: clipToParagraphs(text, 1500)}
	if _, jerr := captaincode.JournalRun(ws.Dir, e); jerr != nil {
		fmt.Fprintf(os.Stderr, "captain brain: euclid journal (interrupt): %v\n", jerr)
	}
	if text == "" {
		text = "(nothing produced before the stop)"
	}
	note := fmt.Sprintf("/interrupt on %q (%s, %s): %s", promptPeek(lastUserTurn(prompt)), l, outcome, clipToParagraphs(text, 800))
	if _, nerr := captaincode.AppendNote(ws.Dir, "memory", note, "captain"); nerr != nil {
		fmt.Fprintf(os.Stderr, "captain brain: euclid handoff note: %v\n", nerr)
	}
}

// routeNote decides which of a turn's attached workers a note goes to. One
// worker: all (it). Several: the legs the note addresses ("@grok …", "/claude:
// …"), else the director's pick from the workers' briefs (team, workflow),
// else every worker. Returns the addressees (nil = all), the note without
// its address, and one line saying how it was routed.
func (b *brain) routeNote(s *captaincode.Steer, text string, attached []captaincode.Leg) (only []captaincode.Leg, note string, why string) {
	note = text
	if len(attached) < 2 {
		return nil, note, ""
	}
	if addr, rest := captaincode.NoteAddress(text); len(addr) > 0 {
		var hit []captaincode.Leg
		for _, l := range addr {
			for _, a := range attached {
				if a == l {
					hit = append(hit, l)
				}
			}
		}
		if len(hit) > 0 {
			return hit, rest, "addressed by the user"
		}
	}
	briefs := s.Briefs()
	if len(briefs) < 2 {
		return nil, note, "all workers"
	}
	var legs []captaincode.Leg
	var reason string
	var err error
	// The decision leg answers the same question beside the director, for the
	// record only (brain_shadow.go): the note goes where the director says.
	noteCh := b.noteShadowBeside(text, briefs)
	if b.routeNoteFn != nil {
		legs, reason, err = b.routeNoteFn(text, briefs)
	} else {
		b.mu.Lock()
		mgr := captaincode.Manager{Director: b.effectiveDirector(), Port: b.mgr.Port}
		b.mu.Unlock()
		mgr.CallLabel, mgr.OnCall = "review", b.chargeOwnTask("btw: route a note to a worker")
		legs, reason, err = mgr.RouteNote(text, briefs)
	}
	actual := captaincode.NoteToAll
	switch {
	case err != nil:
		fmt.Printf("captain brain: /btw route: %v - every worker gets the note\n", err)
		only, why = nil, "all workers"
	case len(legs) == 0 || len(legs) >= len(attached):
		only, why = nil, "director: all workers"+orWhy(reason)
	default:
		only, why = legs, "director: "+strings.TrimSuffix(reason, ".")
		actual = joinLegs(legs, "+")
	}
	b.noteShadowJoin(noteCh, s, text, actual)
	return only, note, why
}

func orWhy(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return " - " + strings.TrimSuffix(reason, ".")
}
