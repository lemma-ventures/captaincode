package captaincode

// What settles a record (ROADMAP M5.1, and the M3.6 gate that waits on it).
//
// Two records in captain are written with an empty verdict column and were
// only ever filled in by hand:
//
//   - an OutcomeEvidence is opened `pending` for every completed task, and
//     nothing but `captain outcome <id> review` moved it off. On this
//     machine that left 440 outcomes, all pending, 75 of them carrying
//     checks nobody read, and 0 reviews. A column that is 100% one value
//     carries no information, so every reading built on it - the M5.2
//     calibration's outcome labels, the M1 report's acceptance rate - was
//     reading a constant.
//   - a gate screening is a PREDICTION about an action. Nothing captain
//     decided beside it can settle it, so the rows sit uncompared and
//     `captain gate --report` correctly refuses to name a bar.
//
// This file settles both, from evidence captain already holds, and refuses
// to settle what it cannot observe.
//
// Outcomes settle from checks and lifecycle. A failed check rejects at once:
// it is an objective fact and waiting adds nothing. An all-passed set
// accepts only after a settle window in which the user did not come back
// with a correction, a regression or a verdict - because "the director
// graded it acceptable three seconds ago" is not acceptance, and a day of
// silence from the person who asked for the work is the weakest honest
// evidence that the work stood. Every derived status records WHAT settled
// it (DecidedBy), so a check-settled acceptance is never read as a human
// one.
//
// Gate rows settle from a cleanly accepted task, in ONE direction. If the
// user accepted a task with no correction and no regression, then no action
// taken during it destroyed unrecoverable work, wandered out of the
// assignment in a way that mattered, or shipped the machine's contents off
// it - so every screening on that task settles as `false`. The converse
// does NOT hold: a rejected or regressed task says the work was bad, not
// WHICH of its forty actions was the dangerous one, and attributing it to
// all of them would manufacture agreement out of nothing. So the sample the
// gate can calibrate on is one-sided: it bounds FALSE POSITIVES - how often
// a high noul fired on an action that turned out to be fine - and says
// nothing about what the gate misses. A bar read off it is a bar on
// over-refusal, which happens to be the bar `enforce` needs, because the
// cost of enforcing too early is a refused worker, not a missed threat.

import (
	"os"
	"strings"
	"time"
)

// OutcomeSettleEnv overrides how long an all-passed outcome waits before it
// settles as accepted.
const OutcomeSettleEnv = "CAPTAIN_OUTCOME_SETTLE"

const outcomeSettleDefault = 24 * time.Hour

// OutcomeSettleWindow is the silence after which checks alone accept a task.
func OutcomeSettleWindow() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(OutcomeSettleEnv))); err == nil && d > 0 {
		return d
	}
	return outcomeSettleDefault
}

// Settled says the outcome has a verdict and who gave it.
func (o OutcomeEvidence) Settled() bool {
	return o.DecidedBy != "" && o.Status != AcceptancePending && o.Status != ""
}

// CleanlyAccepted is the strong acceptance: the task was accepted and the
// user never came back to it - no correction minutes, no later regression.
// It is what the gate's one-sided settle needs, and deliberately stricter
// than AcceptanceAccepted, which a task that cost the user an hour of
// fixing still qualifies for.
func (o OutcomeEvidence) CleanlyAccepted() bool {
	return o.Status == AcceptanceAccepted && o.Regression == nil && len(o.Corrections) == 0
}

// settle derives one outcome's status from its evidence. It returns the
// status and the decider, or pending and "" when nothing settles it yet.
// A human verdict is never overwritten: it is the only evidence here that
// came from someone who actually wanted the work done.
func (o OutcomeEvidence) settle(ts *TaskState, now time.Time) (AcceptanceStatus, OutcomeDecider) {
	if o.Review != nil {
		return o.Status, DecidedByReviewer
	}
	if o.Regression != nil {
		return AcceptanceRegressed, DecidedByRegression
	}
	// A task that never reached delivery was not accepted, whoever's fault
	// that was. StateCancelled is NOT here: the user stopping a turn is a
	// change of mind about the question, not a verdict on the answer, and
	// counting it as a rejection would charge the leg for the interruption.
	if ts != nil && (ts.State == StateFailed || ts.State == StateExhausted) {
		return AcceptanceRejected, DecidedByLifecycle
	}
	for _, c := range o.Checks {
		if !c.Passed {
			return AcceptanceRejected, DecidedByChecks
		}
	}
	// Objective signals captain observes itself (stage 1). A commit that
	// touched the worker's files is the user keeping the work: accepted at
	// once, whatever else is on the record. A corrective re-prompt inside
	// the window is the user sending it back: rejected at once.
	if o.Commit != nil {
		return AcceptanceAccepted, DecidedByCommit
	}
	if o.Reprompt != nil {
		return AcceptanceRejected, DecidedByReprompt
	}
	if len(o.Corrections) > 0 {
		// The checks passed (or there were none) and the user still spent
		// minutes fixing it. The checks were not sufficient, which is a
		// statement about the checks, not an acceptance - and not a
		// rejection either, since the user kept the work. Left pending for a
		// human to call.
		return AcceptancePending, ""
	}
	if now.Sub(o.deliveredAt()) < OutcomeSettleWindow() {
		return AcceptancePending, ""
	}
	if len(o.Checks) > 0 {
		return AcceptanceAccepted, DecidedByChecks
	}
	if o.Delivered() {
		// Nothing was checked and nothing came back: the weakest honest
		// acceptance, labelled so nobody reads it as a passed test.
		return AcceptanceAccepted, DecidedBySilence
	}
	return AcceptancePending, ""
}

// SettleOutcomes sweeps every pending outcome and settles the ones their
// evidence decides, returning how many changed. Idempotent: a settled
// outcome is only revisited by a regression or a human verdict, both of
// which write the record themselves.
func (l *Ledger) SettleOutcomes(now time.Time) int {
	changed := 0
	for i := range l.Outcomes {
		o := &l.Outcomes[i]
		if o.Settled() {
			continue
		}
		status, by := o.settle(l.TaskStateFor(o.TaskID), now)
		if by == "" || (status == o.Status && by == o.DecidedBy) {
			continue
		}
		o.Status, o.DecidedBy, o.SettledAt, o.UpdatedAt = status, by, now, now
		switch status {
		case AcceptanceAccepted:
			o.AcceptedAt = now
		case AcceptanceRejected:
			o.RejectedAt = now
		}
		changed++
		l.journal(RoutingRecord{Kind: RoutingKindOutcome, TaskID: o.TaskID, Outcome: o})
	}
	return changed
}

// GateSettledBy is what a gate row's settle is attributed to: the task's
// clean acceptance, not a choice captain made beside the screening.
const GateSettledBy = "accepted-clean"

// SettleGateRows stamps the screenings a cleanly accepted task settles, and
// reports how many rows it reached. The stamp is DERIVED, applied to the
// rows in memory at read time: the gate log is appended to by every process
// that runs a tool, so nothing here rewrites it.
//
// Only rows carrying a task identity can be settled at all. The transports
// that spawn one process per worker set CAPTAIN_TASK_ID (gate.go); the
// opencode workers share one `opencode serve` and their screenings carry no
// task, so they stay uncompared however many outcomes settle.
func SettleGateRows(rows []ShadowRecord, outcomes []OutcomeEvidence) int {
	clean := make(map[string]bool, len(outcomes))
	for _, o := range outcomes {
		if o.CleanlyAccepted() {
			clean[o.TaskID] = true
		}
	}
	if len(clean) == 0 {
		return 0
	}
	settled := 0
	for i := range rows {
		r := &rows[i]
		if r.TaskID == "" || !clean[r.TaskID] || r.Err != "" {
			continue
		}
		touched := false
		for _, p := range GatePoints {
			if _, ok := r.Answers[p]; !ok {
				continue
			}
			StampGateOutcome(r, p, "false", GateSettledBy)
			touched = true
		}
		if touched {
			settled++
		}
	}
	return settled
}
