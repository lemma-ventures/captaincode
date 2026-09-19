package captaincode

// /btw (2026-09-16): a note for a worker that has already started.
//
// A prompt is sent, the worker runs for ten minutes, and halfway through the
// user remembers a constraint or spots a mistake. The TUI queues anything
// typed behind the running turn, so the note would arrive as the NEXT turn,
// after the worker had finished doing the wrong thing. Two transports can
// take a message mid-run: claude -p reads further user messages from stdin
// in stream-json input mode and folds them into the running turn at its next
// tool boundary (verified live 2026-09-16: a note written 4s into a `sleep 6`
// tool call changed the same turn's answer); opencode serve merges a second
// POST to a busy session into the running turn (verified the same day).
// codex exec and cursor-agent have no such channel - their note runs as the
// turn that follows, which is what the TUI queues anyway.
//
// Steer is the request's handle: the brain creates one per chat turn, the
// transports that can take a note attach their channel to it, and the /btw
// endpoint sends through it. It also tracks which legs are running under the
// request, so the brain can say "codex is running and takes no notes" rather
// than "delivered" when nothing is attached.

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Steer is one chat turn's steering handle (see the file comment).
type Steer struct {
	Dir     string    // the folder the TUI is open in - the workspace a /btw names
	Started time.Time // when the turn began

	mu          sync.Mutex
	running     map[Leg]int
	channels    map[int]steerChannel
	stops       map[int]steerStop
	seq         int
	notes       []SteerNote
	pending     []string       // notes sent before any channel attached: delivered on the first Attach
	interrupted time.Time      // when /interrupt reached this turn (zero = never)
	announce    func(string)   // writes into the turn's visible output (the completion writer sets it)
	briefs      map[Leg]string // what each worker of the turn was assigned (team, workflow) - how a note is routed
	taskID      string         // the turn's task identity once opened (M1.2): joins a note's shadow to the task's outcome
}

type steerChannel struct {
	leg  Leg
	send func(string) error
}

type steerStop struct {
	leg  Leg
	stop func()
}

// SteerNote is one note sent through the handle and the legs that took it.
type SteerNote struct {
	At   time.Time
	Text string
	Took []Leg
}

// NewSteer opens a handle for a turn in dir.
func NewSteer(dir string) *Steer {
	return &Steer{Dir: dir, Started: time.Now(), running: map[Leg]int{}, channels: map[int]steerChannel{}, stops: map[int]steerStop{}}
}

// Began / Ended bracket a worker run under this turn (team turns run several).
func (s *Steer) Began(l Leg) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[l]++
}

func (s *Steer) Ended(l Leg) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[l] <= 1 {
		delete(s.running, l)
	} else {
		s.running[l]--
	}
}

// Running lists the legs with a worker in flight under this turn.
func (s *Steer) Running() []Leg {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedLegs(s.running)
}

// Attach registers a way to reach a running worker mid-turn; the returned
// func detaches it (deferred by the transport when its run ends).
func (s *Steer) Attach(l Leg, send func(string) error) func() {
	if s == nil || send == nil {
		return func() {}
	}
	s.mu.Lock()
	s.seq++
	id := s.seq
	s.channels[id] = steerChannel{leg: l, send: send}
	held := s.pending
	s.pending = nil
	s.mu.Unlock()
	// A note typed while the turn was still compacting or routing (no
	// worker yet, live 2026-09-17) reaches the worker as its first input.
	for _, text := range held {
		if err := send(text); err == nil {
			s.mu.Lock()
			s.notes = append(s.notes, SteerNote{At: time.Now(), Text: text, Took: []Leg{l}})
			s.mu.Unlock()
		}
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.channels, id)
	}
}

// Held reports how many notes wait for a channel.
func (s *Steer) Held() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// AttachStop registers a way to end a running worker at once, keeping what
// it produced (every transport has one: the run's context); the returned
// func detaches it.
func (s *Steer) AttachStop(l Leg, stop func()) func() {
	if s == nil || stop == nil {
		return func() {}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := s.seq
	s.stops[id] = steerStop{leg: l, stop: stop}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.stops, id)
	}
}

// Interrupt ends the turn's work without losing it: a worker that takes
// notes mid-run (claude, the opencode legs) is asked to stop and write a
// handoff - what it implemented, what is left, how to resume - and finishes
// on that; a worker with no channel (codex exec, cursor-agent) is stopped
// now and what it streamed is kept. Returns the legs asked and the legs
// stopped. A turn interrupted once is not asked twice: the second call
// stops everything.
func (s *Steer) Interrupt(reason string) (asked, stopped []Leg) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	again := !s.interrupted.IsZero()
	s.interrupted = time.Now()
	withChannel := map[Leg]bool{}
	for _, c := range s.channels {
		withChannel[c.leg] = true
	}
	stops := make([]steerStop, 0, len(s.stops))
	for _, st := range s.stops {
		stops = append(stops, st)
	}
	s.mu.Unlock()
	if !again {
		took, _ := s.Send(InterruptLine(reason))
		asked = took
	}
	askedSet := map[Leg]bool{}
	for _, l := range asked {
		askedSet[l] = true
	}
	seen := map[Leg]bool{}
	for _, st := range stops {
		if askedSet[st.leg] {
			continue // it will hand off on its own; StopAll ends it if it does not
		}
		st.stop()
		if !seen[st.leg] {
			seen[st.leg] = true
			stopped = append(stopped, st.leg)
		}
	}
	sort.Slice(stopped, func(i, j int) bool { return stopped[i] < stopped[j] })
	return asked, stopped
}

// Abort marks the turn interrupted WITHOUT asking anyone to hand off, then
// ends every running worker now: ctrl+c, where the user wants it stopped
// this instant and the partial is enough.
func (s *Steer) Abort() []Leg {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.interrupted = time.Now()
	s.mu.Unlock()
	return s.StopAll()
}

// StopAll ends every running worker of the turn now (the grace ran out).
func (s *Steer) StopAll() []Leg {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	stops := make([]steerStop, 0, len(s.stops))
	for _, st := range s.stops {
		stops = append(stops, st)
	}
	s.mu.Unlock()
	seen := map[Leg]bool{}
	var out []Leg
	for _, st := range stops {
		st.stop()
		if !seen[st.leg] {
			seen[st.leg] = true
			out = append(out, st.leg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Interrupted reports when /interrupt reached the turn (zero = never).
func (s *Steer) Interrupted() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interrupted
}

// Attached lists the legs a note sent now would reach.
func (s *Steer) Attached() []Leg {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[Leg]int{}
	for _, c := range s.channels {
		set[c.leg]++
	}
	return sortedLegs(set)
}

// SetAnnounce installs the turn's visible-output writer; Announce writes a
// line there - what the user sees in the TUI when a note is taken.
func (s *Steer) SetAnnounce(fn func(string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.announce = fn
	s.mu.Unlock()
}

func (s *Steer) Announce(text string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	fn := s.announce
	s.mu.Unlock()
	if fn != nil {
		fn(text)
	}
}

// Describe records a worker's assignment (a team brief, a workflow stage's
// prompt) so a note can be routed to the worker it concerns.
func (s *Steer) Describe(l Leg, brief string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.briefs == nil {
		s.briefs = map[Leg]string{}
	}
	s.briefs[l] = strings.TrimSpace(brief)
	s.mu.Unlock()
}

// SetTask records the turn's task identity; Task reads it ("" until opened).
func (s *Steer) SetTask(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.taskID = id
	s.mu.Unlock()
}

func (s *Steer) Task() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskID
}

// Briefs is a copy of the assignments recorded with Describe.
func (s *Steer) Briefs() map[Leg]string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[Leg]string, len(s.briefs))
	for k, v := range s.briefs {
		out[k] = v
	}
	return out
}

// Send hands the note to every attached channel (SendTo with no filter).
func (s *Steer) Send(text string) ([]Leg, error) { return s.SendTo(text, nil) }

// SendTo hands the note to the attached channels of the named legs (all
// attached when only is empty) and records it. It returns the legs that
// took it; a channel that fails (the worker finished a moment ago, its
// stdin is closed) is skipped and reported through err.
func (s *Steer) SendTo(text string, only []Leg) ([]Leg, error) {
	text = strings.TrimSpace(text)
	if s == nil || text == "" {
		return nil, errors.New("nothing to send")
	}
	s.mu.Lock()
	chans := make([]steerChannel, 0, len(s.channels))
	for _, c := range s.channels {
		if len(only) > 0 && !legIn(c.leg, only) {
			continue
		}
		chans = append(chans, c)
	}
	if len(chans) == 0 && len(s.running) == 0 && len(only) == 0 {
		// Nothing attached and nothing running yet: the turn is still being
		// prepared. Hold the note for the worker about to start.
		s.pending = append(s.pending, text)
		s.mu.Unlock()
		return nil, nil
	}
	s.mu.Unlock()
	var took []Leg
	var errs []string
	seen := map[Leg]bool{}
	for _, c := range chans {
		if err := c.send(text); err != nil {
			errs = append(errs, string(c.leg)+": "+err.Error())
			continue
		}
		if !seen[c.leg] {
			seen[c.leg] = true
			took = append(took, c.leg)
		}
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	s.mu.Lock()
	s.notes = append(s.notes, SteerNote{At: time.Now(), Text: text, Took: took})
	s.mu.Unlock()
	if len(errs) > 0 {
		return took, errors.New(strings.Join(errs, "; "))
	}
	return took, nil
}

// Notes lists what was sent through this handle, oldest first.
func (s *Steer) Notes() []SteerNote {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SteerNote(nil), s.notes...)
}

func sortedLegs(set map[Leg]int) []Leg {
	out := make([]Leg, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// SteerLine is a note as the worker reads it: marked as the user's, sent
// mid-run, and to be taken into account from here on - so a worker deep in
// a plan treats it as an amendment, not as a new task.
func SteerLine(text string) string {
	return "[captain /btw] The user sent this note while you were working. It complements or amends the task you were given; take it into account from here on and say in your final answer how it was handled:\n\n" + strings.TrimSpace(text)
}

// InterruptLine is the handoff request a worker reads on /interrupt.
func InterruptLine(reason string) string {
	line := "[captain /interrupt] The user is stopping this run now. Do NOT start any new work and do not run further tools except to finish a write already in progress. Reply with a HANDOFF note and then end your turn:\n" +
		"1. Implemented: what you changed, file by file, with any commits.\n" +
		"2. Left to do: the remaining steps, in order, with anything half-done marked.\n" +
		"3. Resume: the exact command or prompt to pick this up, and any gotcha you hit.\n" +
		"Keep it factual and short."
	if r := strings.TrimSpace(reason); r != "" {
		line += "\nThe user's reason: " + r
	}
	return line
}

// ErrInterrupted marks a run the user ended with /interrupt: what it
// produced is kept (WorthKeeping), it is never rerouted.
var ErrInterrupted = errors.New("interrupted by the user")

// interruptible wraps a run's context so the turn's Steer can end it and
// keep what it produced: stopped is set when it did. detach must be deferred.
func interruptible(ctx context.Context, steer *Steer, l Leg) (context.Context, *atomic.Bool, func()) {
	stopped := &atomic.Bool{}
	if steer == nil {
		return ctx, stopped, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	detach := steer.AttachStop(l, func() {
		stopped.Store(true)
		cancel()
	})
	return ctx, stopped, func() { detach(); cancel() }
}

// NoteAddress reads an explicit addressee at the head of a note - "@grok
// use the other cap", "/claude: skip the docs", "grok, claude: …" - and
// returns the legs named and the note without them. No address: nil, text.
func NoteAddress(text string) ([]Leg, string) {
	rest := strings.TrimSpace(text)
	var legs []Leg
	for {
		m := noteAddrRe.FindStringSubmatch(rest)
		if m == nil {
			break
		}
		l := Leg(strings.ToLower(m[1]))
		if !KnownLeg(l) {
			break
		}
		legs = append(legs, l)
		rest = strings.TrimSpace(rest[len(m[0]):])
	}
	if len(legs) == 0 {
		return nil, text
	}
	return legs, rest
}

var noteAddrRe = regexp.MustCompile(`^[@/]?([A-Za-z][A-Za-z0-9-]*)\s*[:,]?\s*`)
