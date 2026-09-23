package main

// Shadow decisions in the brain (pkg shadow.go).
//
// The decision leg is asked the routing questions where captain already
// decides them: in the tier-1 triage call (shape and leg ride along with
// class and domain, one request, no added latency), beside the director's
// plan (started before the plan, joined after it - the director takes
// seconds, jev a few hundred milliseconds under it), and beside the
// director's routing of a mid-turn note. The answers are stamped with what
// captain actually did and who did it, recorded on the turn's decision (or,
// for a note, as a shadow row joined to the turn's task), and never acted
// on. `captain jev shadow` reads the record as a calibration per decision
// point; `captain why` shows one turn's. CAPTAIN_JEV_SHADOW=0 turns the
// questions off; the tier-1 triage answer is unaffected either way.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// jevShadowEnabled: CAPTAIN_JEV_SHADOW=0 keeps the shadow questions out of
// the tier-1 call and makes no call beside the director or a note route.
func jevShadowEnabled() bool { return os.Getenv("CAPTAIN_JEV_SHADOW") != "0" }

// shadowGrace is how long a decision waits for a shadow answer after the
// slower call it was started beside has returned. Normally the answer is
// already there; past the grace the call is recorded as unanswered and
// charged when it lands (it is bounded by jevTimeout either way).
var shadowGrace = 2 * time.Second // a test seam

var errShadowLate = errors.New("jev shadow: no answer within the grace after the director")

// Who settled the class when it was not the decision leg or the director.
const (
	byHeuristic = "heuristic" // tier 0
	byClassify  = "classify"  // the free-leg classify, tier 1's fallback
)

type shadowReply struct {
	sh  *captaincode.Shadow
	res captaincode.Result
	err error
}

// directorMenu is the menu tier 2 would hand the director on this turn: every
// open, allowed rung - what the shadow's leg question is asked over when the
// tier-1 call asks it. Caller holds b.mu.
func (b *brain) directorMenu() []captaincode.Leg {
	return b.filterAllowed(captaincode.Pick(len(captaincode.Rungs)-1, b.ledger.Cooldowns, time.Now()))
}

// jevOptions says what rides along with the triage questions: the shadow
// ones, over the given menu (the director's own when nil), unless the shadow
// is off. Caller holds b.mu.
func (b *brain) jevOptions(d captaincode.Domain, menu []captaincode.Leg) captaincode.JevTriageOptions {
	if !jevShadowEnabled() {
		return captaincode.JevTriageOptions{}
	}
	if menu == nil {
		menu = b.directorMenu()
	}
	return captaincode.JevTriageOptions{Shadow: true, Menu: menu, Domain: d}
}

// shadowBeside asks the decision leg the routing questions while the director
// plans, over the director's own menu. Nil when there is nothing to ask: the
// tier-1 call already did (have), jev is not configured, or the shadow is
// off. Caller holds b.mu; the goroutine touches nothing of b.
func (b *brain) shadowBeside(task string, menu []captaincode.Leg, have *captaincode.Shadow) <-chan shadowReply {
	if have != nil || b.jev == nil || !jevShadowEnabled() {
		return nil
	}
	c, opts := b.jev, b.jevOptions(captaincode.TriageTask(task).Domain, menu)
	ch := make(chan shadowReply, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), jevTimeout)
		defer cancel()
		_, sh, res, err := captaincode.TriageWithJev(ctx, c, task, opts)
		ch <- shadowReply{sh: sh, res: res, err: err}
	}()
	return ch
}

// awaitShadow collects a shadow call started beside a slower one. A reply
// past the grace is charged under its own row when it lands, so no jev call
// goes unbilled, and the record says the answer came too late to compare.
func (b *brain) awaitShadow(ch <-chan shadowReply, label string) shadowReply {
	select {
	case r := <-ch:
		return r
	case <-time.After(shadowGrace):
		late := b.chargeOwnTask("jev " + label + ", answered after the grace")
		go func() {
			r := <-ch
			if late != nil {
				late(captaincode.LegJev, label, r.res, r.err)
			}
		}()
		return shadowReply{sh: &captaincode.Shadow{Leg: captaincode.LegJev, At: time.Now(), Err: errShadowLate.Error()}, err: errShadowLate}
	}
}

// shadowJoin collects the route-time shadow started beside the director's
// plan and charges it to the turn. Caller holds b.mu (the hook expects it).
func (b *brain) shadowJoin(ch <-chan shadowReply, onCall captaincode.CallHook) *captaincode.Shadow {
	if ch == nil {
		return nil
	}
	r := b.awaitShadow(ch, "shadow")
	if errors.Is(r.err, errShadowLate) {
		return r.sh
	}
	if onCall != nil {
		onCall(captaincode.LegJev, "shadow", r.res, r.err)
	}
	if r.err != nil {
		fmt.Printf("captain brain: jev shadow beside the director: %v\n", r.err)
	}
	return r.sh
}

// triageShadowRate is the share of confident heuristic turns that ask jev
// beside the route (stage 1). CAPTAIN_TRIAGE_SHADOW_RATE, default 0.2; 0
// turns the sample off.
func triageShadowRate() float64 {
	if v, err := parseFloatEnv("CAPTAIN_TRIAGE_SHADOW_RATE"); err == nil && v >= 0 && v <= 1 {
		return v
	}
	return 0.2
}

// sampleTriageShadow asks jev the triage questions beside a route the
// heuristic settled with confidence, and records the comparison as a shadow
// row stamped by the heuristic - the one place jev's triage answer had
// never been measured. Acts on nothing; the route has already returned by
// the time the row lands. Caller holds b.mu; the goroutine takes it again.
func (b *brain) sampleTriageShadow(task string, tr captaincode.TriageResult) {
	if b.jev == nil || !jevShadowEnabled() || b.ledger == nil {
		return
	}
	rate := triageShadowRate()
	if rate <= 0 {
		return
	}
	if b.rng == nil {
		b.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if b.rng.Float64() >= rate {
		return
	}
	ch := b.shadowBeside(task, nil, nil)
	if ch == nil {
		return
	}
	taskID := b.routeTurnID(task)
	head := truncate(task, 120)
	go func() {
		r := <-ch
		if r.sh == nil {
			return
		}
		r.sh.Stamp(captaincode.PointClass, string(tr.Class), tr.By)
		r.sh.Stamp(captaincode.PointDomain, string(tr.Domain), tr.By)
		if _, ok := r.sh.Answers[captaincode.PointIrreversible]; ok {
			r.sh.Stamp(captaincode.PointIrreversible, fmt.Sprint(tr.Irreversible), tr.By)
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.ledger.RecordShadow(captaincode.ShadowRecord{
			Point:  captaincode.PointClass,
			Points: []string{captaincode.PointClass, captaincode.PointDomain, captaincode.PointIrreversible},
			TaskID: taskID, Task: head, Shadow: *r.sh,
		})
		if hook := b.chargeOwnTask("jev triage shadow on a confident turn"); hook != nil {
			hook(captaincode.LegJev, "shadow", r.res, r.err)
		} else if err := b.ledger.Save(); err != nil {
			fmt.Printf("captain brain: triage shadow row not saved: %v\n", err)
		}
		if r.err != nil {
			fmt.Printf("captain brain: jev triage shadow: %v\n", r.err)
		}
	}()
}

// stampShadow puts what captain actually decided beside each of the decision
// leg's answers - the class and domain by whoever settled them, the shape and
// the leg by the path that chose them - and attaches the shadow to the record.
func stampShadow(d *captaincode.Decision, sh *captaincode.Shadow, classBy string) {
	if sh == nil {
		return
	}
	stampRoutePoints(*d, sh, classBy)
	d.Shadow = sh
}

// stampRoutePoints is the stamping alone, so a second backend's answers can
// be compared against the same decision without going onto it: a Decision
// carries the shadow of the backend captain acted on, and an open sidecar's
// row has to be readable apart from it or the two would pool.
func stampRoutePoints(d captaincode.Decision, sh *captaincode.Shadow, classBy string) {
	sh.Stamp(captaincode.PointClass, string(d.Class), classBy)
	sh.Stamp(captaincode.PointDomain, string(d.Domain), classBy)
	sh.Stamp(captaincode.PointShape, d.Shape, d.Path)
	actual := string(d.Chosen)
	if d.Shape == captaincode.ShapeTeam {
		actual = joinLegs(d.Workers, "+")
	}
	sh.Stamp(captaincode.PointLeg, actual, d.Path)
}

// noteShadowBeside asks the decision leg which worker a mid-turn note
// concerns, from the same briefs the director reads, while the director is
// asked. Nil when there is nothing to ask. Not under b.mu.
func (b *brain) noteShadowBeside(note string, briefs map[captaincode.Leg]string) <-chan shadowReply {
	if b.jev == nil || !jevShadowEnabled() || len(briefs) < 2 {
		return nil
	}
	c := b.jev
	ch := make(chan shadowReply, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), jevTimeout)
		defer cancel()
		sh, res, err := captaincode.RouteNoteWithJev(ctx, c, note, briefs)
		ch <- shadowReply{sh: sh, res: res, err: err}
	}()
	return ch
}

// noteShadowJoin records jev's answer beside the director's (actual: the
// workers the note went to, "+"-joined, or "all") as a shadow row joined to
// the turn's task, and charges the call. Not under b.mu.
func (b *brain) noteShadowJoin(ch <-chan shadowReply, s *captaincode.Steer, note, actual string) {
	if ch == nil || b.ledger == nil {
		return
	}
	r := b.awaitShadow(ch, captaincode.PointNoteRoute)
	if r.sh == nil {
		return
	}
	r.sh.Stamp(captaincode.PointNoteRoute, actual, captaincode.PathDirector)
	b.mu.Lock()
	b.ledger.RecordShadow(captaincode.ShadowRecord{Point: captaincode.PointNoteRoute, TaskID: s.Task(), Task: truncate(note, 120), Shadow: *r.sh})
	b.mu.Unlock()
	if errors.Is(r.err, errShadowLate) {
		return // charged when it lands
	}
	if hook := b.chargeOwnTask("btw: jev shadow of the note's route"); hook != nil {
		hook(captaincode.LegJev, captaincode.PointNoteRoute, r.res, r.err) // charges, and saves the row with it
	}
	if r.err != nil {
		fmt.Printf("captain brain: jev shadow beside the note's route: %v\n", r.err)
	}
}

// joinLegs names a set of legs in a stable order, "+"-joined for a record.
func joinLegs(legs []captaincode.Leg, sep string) string {
	names := make([]string, 0, len(legs))
	for _, l := range legs {
		names = append(names, string(l))
	}
	sort.Strings(names)
	return strings.Join(names, sep)
}

// planLegs lists a plan's workers' legs.
func planLegs(p captaincode.Plan) []captaincode.Leg {
	out := make([]captaincode.Leg, 0, len(p.Workers))
	for _, w := range p.Workers {
		out = append(out, w.Leg)
	}
	return out
}

// ---- the open sidecar, beside whichever backend decided ------------------------

// jevDecidesTriage says some backend may act on a triage answer for this
// task. It is not "a client exists": a sidecar that has not been promoted
// answers only in the shadow, so with no primary configured there is nothing
// to consult and the heuristic stands.
func (b *brain) jevDecidesTriage(task string) bool {
	c, _, _ := b.triageDecider(task)
	return c != nil
}

// triageDecider names the backend that may act on a triage answer for this
// task and the bar it must clear. The pool decides whether the sidecar has
// earned this one; everything else is b.jev, which is the primary handle the
// brain actually holds - the same client the pool named, except in tests,
// where it is a fake server that has to keep deciding.
func (b *brain) triageDecider(task string) (*captaincode.SystemOneClient, float64, string) {
	c, bar, why := b.jevBackends.Decider(captaincode.CapTriage, task)
	if c != nil && c == b.jevBackends.Open {
		return c, bar, why
	}
	return b.jev, 0, why
}

// openBeside asks the open sidecar the same questions the tier-1 call asks -
// class and domain, with shape and leg riding along - while captain settles
// them some other way. This is the only place a sidecar's rows come from, and
// they are what `captain jev shadow --backend <host>` reads to produce the
// bar that would promote it. Nil when there is no sidecar, when the sidecar
// is already the backend deciding this call (its answer is on the decision's
// own shadow then), or when this state would not fit it: an answer read off a
// truncated state is not a datum, and a hundred of them would promote a
// backend on the strength of questions it never saw.
//
// WHICH turns are worth asking about is the caller's call, and both places
// this is called from are turns captain was going to think about anyway - the
// band under CAPTAIN_TRIAGE_JEV_BELOW, and the director's plan. A sample
// drawn from the turns the heuristic settled by itself would say more about
// the heuristic than about the backend.
//
// Not charged: it is a local process reading weights off this disk. Caller
// holds b.mu; the goroutine touches nothing of b.
func (b *brain) openBeside(task string, d captaincode.Domain, menu []captaincode.Leg) <-chan shadowReply {
	c := b.jevBackends.Shadowing(captaincode.CapTriage, task)
	if c == nil || !jevShadowEnabled() {
		return nil
	}
	opts := b.jevOptions(d, menu)
	opts.Shadow = true // the routing points are the ones worth its rows
	ch := make(chan shadowReply, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), jevAskTimeout(c))
		defer cancel()
		_, sh, res, err := captaincode.TriageWithJev(ctx, c, task, opts)
		ch <- shadowReply{sh: sh, res: res, err: err}
	}()
	return ch
}

// recordOpenShadow puts the sidecar's answers on the ledger as a row of their
// own, stamped with what captain actually decided - never on the decision,
// which belongs to the backend captain acted on.
//
// It does not wait. The shadow beside the director can afford to, because the
// director takes seconds and the answer is already there; this call is joined
// on the fast path too, where the route is finished in a millisecond and
// blocking on a wedged sidecar would put its whole timeout on every turn. A
// row nobody is waiting for is worth exactly what it costs to collect later.
// Caller holds b.mu; the goroutine takes it again once the caller is done.
func (b *brain) recordOpenShadow(task string, d captaincode.Decision, ch <-chan shadowReply, classBy string) {
	if ch == nil || b.ledger == nil {
		return
	}
	// The turn's identity now, while it is still this turn's: it is consumed
	// when a worker adopts the route, and a row that joins no outcome still
	// carries its comparison.
	taskID := b.routeTurnID(task)
	go func() {
		r := <-ch // bounded by the sidecar client's own timeout
		if r.sh == nil {
			return
		}
		stampRoutePoints(d, r.sh, classBy)
		b.mu.Lock()
		defer b.mu.Unlock()
		b.ledger.RecordShadow(captaincode.ShadowRecord{
			Point:  captaincode.PointClass,
			Points: []string{captaincode.PointClass, captaincode.PointDomain, captaincode.PointShape, captaincode.PointLeg},
			TaskID: taskID, Task: truncate(task, 120), Shadow: *r.sh,
		})
		if err := b.ledger.Save(); err != nil {
			fmt.Printf("captain brain: open shadow row not saved: %v\n", err)
		}
		if r.err != nil {
			fmt.Printf("captain brain: open sidecar beside triage: %v\n", r.err)
		}
	}()
}
