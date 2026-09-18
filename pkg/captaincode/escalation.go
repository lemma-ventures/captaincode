package captaincode

// Bounded escalation (ROADMAP M2.5). When a worker SUCCEEDS (provider didn't
// fail) but an OBJECTIVE CHECK fails — a gate, an acceptance check, or an
// explicit rejection — the brain may try one repair (rerun the same leg with
// the failure output) and one escalation (rerun on a stronger leg), then stop.
//
// This is distinct from provider-failure rerouting (runWorkerRerouted), which
// handles legs that went DOWN or were RATE-LIMITED: those are the provider's
// fault, and the task moves to the next open leg without consuming an
// escalation. Escalation is for the case where the worker RAN and produced
// output that the objective check rejected — the work was wrong, not the
// provider.
//
// The policy is bounded: MaxRepairs retries on the same leg, MaxEscalations
// moves to a stronger leg, each drawing from the same root task budget (M2.4).
// When the bounds are exhausted, the task stops with StopObjectiveFailed. When
// the budget refuses further dispatch, it stops with the budget's reason
// (attempts_exhausted, cost_exhausted, time_exhausted).
//
// Defaults are conservative: MaxRepairs=1, MaxEscalations=1. Set
// CAPTAIN_MAX_REPAIRS and CAPTAIN_MAX_ESCALATIONS to adjust. Zero means "no
// repairs/escalations" — the worker's first objective failure is final.

import (
	"os"
	"strconv"
	"time"
)

// EscalationVersion is stamped on every policy, the same contract
// AccountingVersion, QuotaVersion and BudgetVersion carry.
const EscalationVersion = 1

// EscalationPolicy bounds how many times a worker's objective failure can
// trigger a retry before the task stops. It lives in the budget's shadow: the
// budget controls the aggregate ceiling, the policy controls the per-failure
// sequence.
type EscalationPolicy struct {
	Version        int `json:"version"`
	MaxRepairs     int `json:"max_repairs"`     // retries on the same leg (0 = none)
	MaxEscalations int `json:"max_escalations"` // moves to a stronger leg (0 = none)
}

// DefaultEscalationPolicy reads the environment for escalation limits.
// Zero means "no retries of this kind" — the worker's first objective failure
// is final. The conservative default is one repair + one escalation.
func DefaultEscalationPolicy() EscalationPolicy {
	return EscalationPolicy{
		Version:        EscalationVersion,
		MaxRepairs:     envIntEscalation("CAPTAIN_MAX_REPAIRS", 1),
		MaxEscalations: envIntEscalation("CAPTAIN_MAX_ESCALATIONS", 1),
	}
}

// CanRepair reports whether a repair (retry on the same leg) is still
// available under the policy. repairsUsed is how many repairs have already
// been consumed for this objective failure.
func (p EscalationPolicy) CanRepair(repairsUsed int) bool {
	return p.MaxRepairs > 0 && repairsUsed < p.MaxRepairs
}

// CanEscalate reports whether an escalation (move to a stronger leg) is still
// available under the policy. escalationsUsed is how many escalations have
// already been consumed.
func (p EscalationPolicy) CanEscalate(escalationsUsed int) bool {
	return p.MaxEscalations > 0 && escalationsUsed < p.MaxEscalations
}

// NextEscalation returns the next stronger leg to try after failed has failed
// its objective check, skipping legs already tried. "Stronger" is the perf
// ranking: the next leg up from failed that is not in tried. Returns false
// when no stronger leg is available (the failed leg was already the strongest,
// or every stronger leg has been tried).
//
// The frontier chain (FrontierChain) is the ranking used: it orders by perf
// index, frontier legs first, so an escalation from a cheap leg moves up to
// a frontier-class leg rather than the next rung on the cheap ladder.
func (p EscalationPolicy) NextEscalation(failed Leg, tried []Leg, allowed map[Leg]bool, cooldowns map[Leg]time.Time, now time.Time) (Leg, bool) {
	chain := FrontierChain(failed)
	triedSet := map[Leg]bool{failed: true}
	for _, l := range tried {
		triedSet[l] = true
	}
	for _, l := range chain {
		if triedSet[l] {
			continue
		}
		if len(allowed) > 0 && !allowed[l] {
			continue
		}
		if until, ok := cooldowns[l]; ok && now.Before(until) {
			continue
		}
		return l, true
	}
	return "", false
}

// EscalationOutcome records what happened during a bounded escalation
// sequence, for `captain why` and the budget's stopping reason.
type EscalationOutcome struct {
	RepairsUsed     int  `json:"repairs_used"`
	EscalationsUsed int  `json:"escalations_used"`
	Repaired        bool `json:"repaired,omitempty"`
	Escalated       bool `json:"escalated,omitempty"`
	EscalatedTo     Leg  `json:"escalated_to,omitempty"`
	ObjectiveMet    bool `json:"objective_met"`
}

// StopReasonFor returns the stopping reason for an escalation sequence that
// did not meet its objective. The first-wins rule from Budget.stop applies:
// if the budget already stopped for another reason (attempts_exhausted,
// cost_exhausted), that reason stays.
func (o EscalationOutcome) StopReason() string {
	if o.ObjectiveMet {
		return StopObjectiveMet
	}
	return StopObjectiveFailed
}

// envIntEscalation reads an int from the environment, returning def when unset
// or invalid. This is the same logic as envIntDefault in budget.go, but local
// to escalation.go for the policy's own env vars.
func envIntEscalation(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
