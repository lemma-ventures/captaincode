package main

// The learning side of routing in the brain (stages 2, 3 and 5): which
// policy ranks the cheap path, the success estimator the expected-cost and
// bandit policies read, and the burn-rate pressure that replaces the
// rate-limit flag as the quota term.

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// Routing policies for the cheap path. CAPTAIN_ROUTING_POLICY:
//
//	value     the quality−cost−latency ranking (value.go), the default until
//	          the history holds enough labelled outcomes
//	expected  expected cost per successful task over (leg, effort) arms
//	          (expected.go), used once CAPTAIN_EXPECTED_MIN_LABELED outcomes
//	          are settled, value otherwise - and the decision says which
//	bandit    Thompson sampling over the same arms (bandit.go), gated by
//	          CAPTAIN_BANDIT_MIN_LABELED; falls back to expected, then value
//
// Unset means expected: the estimate degrades to the feed prior when the
// history is thin, which is exactly the value ranking's own quality prior,
// so a fresh install is not worse for it - and it starts learning on day
// one instead of never.
const (
	policyValue    = "value"
	policyExpected = "expected"
	policyBandit   = "bandit"
)

func routingPolicy() string {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("CAPTAIN_ROUTING_POLICY"))); v {
	case policyValue, policyExpected, policyBandit:
		return v
	}
	return policyExpected
}

// expectedMinLabeled is how many settled outcomes the expected-cost ranking
// needs before it is allowed to reorder the value ranking. Below it the
// arms are still scored and recorded (the effort choice reads them), but
// the value order runs. CAPTAIN_EXPECTED_MIN_LABELED, default 50.
func expectedMinLabeled() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CAPTAIN_EXPECTED_MIN_LABELED"))); err == nil && v >= 0 {
		return v
	}
	return 50
}

// estimatorTTL is how long a built estimator serves before the history is
// re-read: a settled outcome takes at most this long to start counting.
const estimatorTTL = 5 * time.Minute

// estimator returns the success estimator, rebuilt from the routing history
// when stale. Caller holds b.mu.
func (b *brain) estimator() *captaincode.SuccessEstimator {
	if b.estimatorFn != nil {
		e := b.estimatorFn()
		b.labeled = e.Samples()
		return e
	}
	if b.est != nil && time.Since(b.estAt) < estimatorTTL {
		return b.est
	}
	hist := captaincode.RoutingHistory(b.ledger)
	b.est, b.estAt, b.labeled = captaincode.NewSuccessEstimator(hist), time.Now(), captaincode.LabeledCount(hist)
	return b.est
}

// labeledCount is the settled-outcome count the policy gates read. Caller
// holds b.mu.
func (b *brain) labeledCount() int {
	b.estimator()
	return b.labeled
}

// effortOptions are the rungs an arm may run at for a class: the decided
// rung and one up, so the ranking can prefer a cheaper leg at a higher
// effort over a dearer leg at a lower one. The frontier ceiling applies.
func effortOptions(c captaincode.Class, irreversible bool) func(captaincode.Leg) []captaincode.Effort {
	return func(l captaincode.Leg) []captaincode.Effort {
		base := captaincode.DecideEffort("", c, l, irreversible, 1)
		if !captaincode.HasEffortKnob(l) {
			return []captaincode.Effort{base}
		}
		if up := captaincode.NextEffort(base); up != base && captaincode.EffortRank(up) <= captaincode.EffortRank(captaincode.EffortCeiling()) {
			return []captaincode.Effort{base, up}
		}
		return []captaincode.Effort{base}
	}
}

// expectedArms scores the eligible legs as (leg, effort) arms for a triaged
// task. Caller holds b.mu.
func (b *brain) expectedArms(tr captaincode.TriageResult, task string, legs []captaincode.Leg) []captaincode.ExpectedRow {
	return captaincode.ExpectedRank(captaincode.ExpectedInput{
		Task: task, Class: tr.Class, Domain: tr.Domain, Legs: legs,
		Efforts:   effortOptions(tr.Class, tr.Irreversible),
		Estimator: b.estimator(), Stats: b.ledger.Stats(), Tokens: estTokensFor(tr.Class),
		Pressure: b.pressure, MidTierP: tr.MidTierP, Now: time.Now(),
		BaseEffort: captaincode.DecideEffort("", tr.Class, "", tr.Irreversible, 1),
	})
}

// choosePolicy picks the arm the cheap path runs, under the configured
// policy and its data gate. It returns the arm, the path that chose it, and
// a rationale fragment; ok is false when the value order should run instead
// (and says why in the fragment).
func (b *brain) choosePolicy(arms []captaincode.ExpectedRow) (captaincode.ExpectedRow, string, string, bool) {
	eligible := captaincode.EligibleExpected(arms)
	if len(eligible) == 0 {
		return captaincode.ExpectedRow{}, "", "no arm clears the success floor", false
	}
	labeled := b.labeledCount()
	switch routingPolicy() {
	case policyBandit:
		if b.rng == nil {
			b.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
		}
		pick := captaincode.ThompsonPick(arms, labeled, b.rng)
		if pick.Refused == "" {
			return pick.Row, captaincode.PathBandit, fmt.Sprintf("bandit: sampled p=%.2f over %d labelled outcomes", pick.Sampled, labeled), true
		}
		fmt.Printf("captain brain: %s - expected-cost ranking instead\n", pick.Refused)
		fallthrough
	case policyExpected:
		if min := expectedMinLabeled(); labeled < min {
			return eligible[0], captaincode.PathExpected, fmt.Sprintf("expected-cost gate: %d labelled outcomes, needs %d - value order runs", labeled, min), false
		}
		return eligible[0], captaincode.PathExpected, fmt.Sprintf("expected $%.3f per successful task (p=%.2f, %d labelled outcomes)", eligible[0].Expected, eligible[0].P, labeled), true
	}
	return eligible[0], captaincode.PathValue, "value policy", false
}

// burnWindow is the trailing window burn-rate pressure is measured over.
const burnWindow = 5 * time.Hour

// burnPressure is a subscription leg's window pressure from what it has
// answered inside the trailing window against the assumed allowance
// (expected.go). Caller holds b.mu.
func (b *brain) burnPressure(l captaincode.Leg, now time.Time) float64 {
	return captaincode.BurnPressure(b.ledger.Events, l, now, burnWindow, captaincode.WindowAllowance())
}
