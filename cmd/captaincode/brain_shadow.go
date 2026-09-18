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

// stampShadow puts what captain actually decided beside each of the decision
// leg's answers - the class and domain by whoever settled them, the shape and
// the leg by the path that chose them - and attaches the shadow to the record.
func stampShadow(d *captaincode.Decision, sh *captaincode.Shadow, classBy string) {
	if sh == nil {
		return
	}
	sh.Stamp(captaincode.PointClass, string(d.Class), classBy)
	sh.Stamp(captaincode.PointDomain, string(d.Domain), classBy)
	sh.Stamp(captaincode.PointShape, d.Shape, d.Path)
	actual := string(d.Chosen)
	if d.Shape == captaincode.ShapeTeam {
		actual = joinLegs(d.Workers, "+")
	}
	sh.Stamp(captaincode.PointLeg, actual, d.Path)
	d.Shadow = sh
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
