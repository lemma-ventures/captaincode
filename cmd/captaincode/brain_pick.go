package main

import (
	"math/rand"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// exploreCap is the share of the rule's recent decided picks that may be
// draws away from the best by mean (SCORING.md Phase 3).
const exploreCap = 0.2

// timePick runs the Phase 3 rule over legs for a task on domain d. The
// lanes chose the legs; the rule picks among them. Caller holds b.mu.
func (b *brain) timePick(legs []captaincode.Leg, d captaincode.Domain) (captaincode.TimePick, bool) {
	est := b.modelEstimator()
	recent := b.steerRecentLocked()
	now := time.Now()
	seen := map[captaincode.Leg]bool{}
	var cands []captaincode.PickCandidate
	for _, l := range legs {
		if seen[l] || l == "" || captaincode.IsFrontier(l) {
			continue
		}
		seen[l] = true
		c := captaincode.PickCandidate{Leg: l, Est: est.Estimate(l, b.currentModel(l), "", d),
			MixDeficit: captaincode.MixDeficit(b.ledger.Steer, recent, l)}
		if spec, ok := captaincode.Spec(l); ok && spec.Subscription {
			c.Pressure = b.burnPressure(l, now)
		}
		cands = append(cands, c)
	}
	if len(cands) < 2 {
		return captaincode.TimePick{}, false
	}
	if b.rng == nil {
		b.rng = rand.New(rand.NewSource(now.UnixNano()))
	}
	mode := captaincode.PickMode()
	tp, ok := captaincode.PickByTime(cands, mode != "on" || b.exploreBudgetLeft(), b.rng)
	tp.Mode = mode
	return tp, ok
}

// currentModel is the model a leg last reported running, else the one its
// configuration names. Caller holds b.mu.
func (b *brain) currentModel(l captaincode.Leg) string {
	for i := len(b.ledger.Events) - 1; i >= 0; i-- {
		if ev := b.ledger.Events[i]; ev.Leg == l && ev.ModelResolved == "observed" && ev.Model != "" {
			return ev.Model
		}
	}
	return captaincode.ModelIDAt(l, "")
}

// exploreBudgetLeft says whether the rule's recent decided picks leave room
// for another draw away from the best. Caller holds b.mu.
func (b *brain) exploreBudgetLeft() bool {
	n, explored := 0, 0
	for i := len(b.ledger.Decisions) - 1; i >= 0 && n < 40; i-- {
		tp := b.ledger.Decisions[i].TimePick
		if tp == nil || tp.Mode != "on" {
			continue
		}
		n++
		if tp.Explored {
			explored++
		}
	}
	return n == 0 || float64(explored)/float64(n) < exploreCap
}

// shadowPick records what the rule would have picked beside a decision it
// did not make. Caller holds b.mu.
func (b *brain) shadowPick(d *captaincode.Decision) {
	if d.TimePick != nil || captaincode.PickMode() == "off" || d.Chosen == "" {
		return
	}
	var legs []captaincode.Leg
	for _, c := range d.Candidates {
		if c.Excluded == "" {
			legs = append(legs, c.Leg)
		}
	}
	if tp, ok := b.timePick(legs, d.Domain); ok {
		d.TimePick = &tp
	}
}

// decidePick lets the rule decide a menu when CAPTAIN_PICK=on; ok is false
// otherwise or with fewer than two legs. Caller holds b.mu.
func (b *brain) decidePick(menu []captaincode.Leg, d captaincode.Domain) (captaincode.TimePick, bool) {
	if captaincode.PickMode() != "on" {
		return captaincode.TimePick{}, false
	}
	return b.timePick(menu, d)
}
