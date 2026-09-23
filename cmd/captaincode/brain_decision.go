package main

// Decision evidence in the brain (ROADMAP M2.1).
//
// The ranking that chooses a leg exists for a few microseconds inside
// decideLeg and was then thrown away, leaving one sentence of rationale
// behind. `captain why` could therefore answer "which leg ran" but not "what
// else was considered", "why was the leg I expected not eligible", "how much
// evidence stands behind that quality number" or "under which tunables".
//
// The record is assembled where the choice is made and parked here until the
// turn that executes it resolves a task identity - the same identity its
// charges hang off (M1.2), so the decision and its bill are one row apart. A
// decision is deliberately NOT allowed to mint that identity: a turn the
// deterministic triage fast-paths asks no model, and must not leave a task
// row claiming it spent something.

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// pendingDecision is a decision waiting for the task identity to stamp on it.
type pendingDecision struct {
	dec captaincode.Decision
	at  time.Time
}

// decisionTTL bounds the wait for the same reason routeTurnTTL does: the
// wrapper turn follows its route within seconds, and adopting an older
// decision would explain this turn with the last one's ranking.
const decisionTTL = routeTurnTTL

// recordDecision parks the evidence behind a routing choice. Keyed by the same
// truncated task text the route-time identity and the exploration tag use, so
// all three resolve from the wrapper's flattened prompt.
func (b *brain) recordDecision(task string, d captaincode.Decision) {
	d.Task = truncate(task, 120)
	d.At = time.Now()
	b.rtmu.Lock()
	defer b.rtmu.Unlock()
	if b.pendingDecisions == nil {
		b.pendingDecisions = map[string]pendingDecision{}
	}
	for k, p := range b.pendingDecisions { // opportunistic expiry
		if time.Since(p.at) > decisionTTL {
			delete(b.pendingDecisions, k)
		}
	}
	b.pendingDecisions[d.Task] = pendingDecision{dec: d, at: time.Now()}
}

// stampTriage puts the triage verdict on the record: who settled the class,
// how sure, and the two extra answers (stage 1). Attempt 1 - the repair and
// escalation attempts re-stamp their own.
func stampTriage(d *captaincode.Decision, tr captaincode.TriageResult) {
	d.TriageBy, d.Confidence = tr.By, tr.Confidence
	d.Irreversible, d.MidTierP = tr.Irreversible, tr.MidTierP
	if d.Attempt == 0 {
		d.Attempt = 1
	}
}

// stampPendingEffort writes the effort decideRoute settled onto the parked
// decision, which was recorded by decideLeg before the effort existed.
func (b *brain) stampPendingEffort(task string, e captaincode.Effort) {
	if e == "" {
		return
	}
	b.rtmu.Lock()
	defer b.rtmu.Unlock()
	if p, ok := b.pendingDecisions[truncate(task, 120)]; ok {
		p.dec.Effort = e
		b.pendingDecisions[truncate(task, 120)] = p
	}
}

// deliveredTask is what a workspace last delivered, for the follow-up read.
type deliveredTask struct {
	taskID string
	at     time.Time
}

// noteDelivered remembers the task a workspace just delivered. Caller
// holds b.mu.
func (b *brain) noteDelivered(dir, taskID string) {
	if dir == "" || taskID == "" {
		return
	}
	if b.lastDelivered == nil {
		b.lastDelivered = map[string]deliveredTask{}
	}
	b.lastDelivered[dir] = deliveredTask{taskID: taskID, at: time.Now()}
}

// noteFollowUp reads the next prompt on a workspace against what it last
// delivered: a corrective opener inside the settle window is the user
// sending the work back, and settles that task as rejected (settle.go).
// Any other prompt is a new task and says nothing.
func (b *brain) noteFollowUp(dir, text string) {
	if dir == "" || strings.TrimSpace(text) == "" || !captaincode.CorrectiveReprompt(text) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.lastDelivered[dir]
	if !ok || time.Since(d.at) > captaincode.OutcomeSettleWindow() {
		return
	}
	delete(b.lastDelivered, dir)
	b.ledger.RecordReprompt(d.taskID, truncate(text, 160), time.Now())
	b.ledger.SettleOutcomes(time.Now())
	if err := b.ledger.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "captain brain: save reprompt: %v\n", err)
	}
	fmt.Printf("captain brain: follow-up read as a correction of task %s (%q)\n", d.taskID, promptPeek(text))
}

// attachDecision stamps the task identity onto this turn's parked decision and
// writes it to the ledger. Consumed rather than read, like the route-time
// identity: a second turn of the same prompt text is a new decision, and
// re-attaching this one would credit it with a ranking it never ran.
// Caller holds b.mu; rtmu is taken inside, matching adoptRouteTurn's order.
func (b *brain) attachDecision(taskID, task string) {
	key := truncate(task, 120)
	b.rtmu.Lock()
	p, ok := b.pendingDecisions[key]
	delete(b.pendingDecisions, key)
	b.rtmu.Unlock()
	if !ok || time.Since(p.at) > decisionTTL {
		return
	}
	p.dec.TaskID = taskID
	b.ledger.RecordDecision(p.dec)
}

// valueDecision builds the record behind a cheap-path choice: the ranked
// field, the hard exclusions with their reasons, and the policy snapshot the
// ranking is only reproducible alongside. Caller holds b.mu.
func (b *brain) valueDecision(tr captaincode.TriageResult, chosen captaincode.Leg, rows []captaincode.Scored, decidedMs int64, rationale string) captaincode.Decision {
	d := captaincode.Decision{
		Class: tr.Class, Domain: tr.Domain, Chosen: chosen, Rationale: rationale,
		Path:       captaincode.PathValue,
		Shape:      captaincode.ShapeSolo,
		DecidedMs:  decidedMs,
		Policy:     captaincode.PolicyFor(tr.Class, estTokensFor(tr.Class), exploreRate(tr.Class)),
		Candidates: rows,
	}
	eligible := captaincode.Eligible(rows)
	// Value routing off, or nothing cleared τ: the leg came off the
	// hand-written FastLadder instead, and calling that a value decision
	// would attribute the choice to a ranking that did not make it. The
	// exclusions stay - they are exactly why the fallback happened.
	if !valueRoutingEnabled() || len(eligible) == 0 {
		d.Path, d.Policy.Name = captaincode.PathLadder, "ladder"
		return d
	}
	// Exploration is the one case where the chosen leg is not the top of the
	// ranking. Naming what it passed over keeps that legible as a deliberate
	// departure rather than a ranking nobody can reproduce.
	if eligible[0].Leg != chosen {
		d.Explored, d.PassedOver = true, eligible[0].Leg
	}
	return d
}

// menuDecision builds the record behind a director, forced or ladder choice.
// Those paths do not produce a value ranking, so the menu the leg came from is
// scored here with the same terms - not to claim the ranking chose it, but so
// the field the choice was made against is on the record beside it. A forced
// leg has no menu at all, which is itself the answer to "why this leg".
// Caller holds b.mu.
func (b *brain) menuDecision(c captaincode.Class, d captaincode.Domain, path string, chosen captaincode.Leg, menu []captaincode.Leg, decidedMs int64, rationale string) captaincode.Decision {
	dec := captaincode.Decision{
		Class: c, Domain: d, Chosen: chosen, Path: path, Rationale: rationale, DecidedMs: decidedMs,
		Shape:  captaincode.ShapeSolo,
		Policy: captaincode.PolicyFor(c, estTokensFor(c), 0),
	}
	dec.Policy.Name = path
	if len(menu) == 0 {
		return dec
	}
	_, excluded := b.valueCandidates(false)
	offered := map[captaincode.Leg]bool{}
	for _, l := range menu {
		offered[l] = true
	}
	// τ does not gate these paths: a leg the director was offered was eligible
	// for it, whatever the cheap path's threshold would have said. Clearing the
	// exclusion keeps the record honest about which bar actually applied.
	rows := captaincode.ValueRank(c, d, menu, b.ledger.Stats(), estTokensFor(c), b.pressure)
	for i := range rows {
		rows[i].Excluded = ""
		rows[i].Calibration = captaincode.Calibrate(rows[i].Leg, d, b.ledger.Events, time.Now())
	}
	// ValueRank sorted the sub-τ rows to the back as exclusions; with the bar
	// lifted they are ordinary candidates again and must sit at their value.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Value > rows[j].Value })
	for _, e := range excluded {
		if !offered[e.Leg] {
			rows = append(rows, e)
		}
	}
	dec.Candidates = rows
	return dec
}
