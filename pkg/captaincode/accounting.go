package captaincode

// Task and call accounting (ROADMAP M1.2). The ledger's Event is one row per
// worker run: good enough to rank legs, useless as a bill. A turn can burn a
// classification call, a director plan, several workers, a review and a
// repair, and the only number that survived was the last worker's token
// count. M1.2 adds the missing spine: every provider call gets a versioned
// record with a stable identity, a parent, and an explicit statement of how
// well its usage is known.
//
// Two invariants the report depends on:
//
//   - zero is not missing. A subscription leg that costs no marginal dollar
//     records $0 measured; a leg that reported nothing records unknown. They
//     must never add up to the same "total spend".
//   - a parent is not billed on top of its children. Totals sum LEAVES only;
//     a task/stage/attempt row carries identity and timing, not a second copy
//     of the money.

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"time"
)

// AccountingVersion is stamped on every charge. Readers reject what they do
// not understand rather than silently mis-summing a newer shape.
const AccountingVersion = 1

// UsageStatus is how well a usage/charge figure is known. Every record states
// one; there is no implicit default.
type UsageStatus string

const (
	UsageMeasured  UsageStatus = "measured"  // the runtime reported it
	UsageEstimated UsageStatus = "estimated" // derived from registry price or a token split
	UsageUnknown   UsageStatus = "unknown"   // nothing was reported and nothing can be derived
)

// ChargeKind places a record in the task tree. Only KindCall is billable.
type ChargeKind string

const (
	KindTask    ChargeKind = "task"    // one user-visible unit of work
	KindStage   ChargeKind = "stage"   // a workflow stage, fan-out, review pass
	KindAttempt ChargeKind = "attempt" // one worker's try at a stage (retry/repair/escalation = new attempt)
	KindCall    ChargeKind = "call"    // one provider call - the only row that carries money
)

// Usage is one call's consumption. Cached and Reasoning are provider SUBSETS
// of the billed counts, kept for provenance and never added into Total - the
// double-counting M1.2 exists to prevent.
type Usage struct {
	Status     UsageStatus `json:"status"`
	Input      int         `json:"input,omitempty"`
	Output     int         `json:"output,omitempty"`
	Cached     int         `json:"cached,omitempty"`
	Reasoning  int         `json:"reasoning,omitempty"`
	Total      int         `json:"total,omitempty"`
	CostUSD    float64     `json:"cost_usd"`
	CostStatus UsageStatus `json:"cost_status"`
	// PriceSource is where the money came from: "runtime" when the worker
	// reported dollars, "registry:<leg>" when we priced its tokens ourselves,
	// "" when neither. Without it an estimate is indistinguishable from a bill.
	PriceSource string `json:"price_source,omitempty"`
	// Raw keeps the runtime's own fields verbatim beside the normalized ones,
	// so a later correction can be re-derived instead of re-run.
	Raw map[string]any `json:"raw,omitempty"`
}

// Charge is one node of the accounting tree.
type Charge struct {
	Version    int        `json:"version"`
	ID         string     `json:"id"`
	Parent     string     `json:"parent,omitempty"`
	TaskID     string     `json:"task_id"`
	Kind       ChargeKind `json:"kind"`
	Leg        Leg        `json:"leg,omitempty"`
	Label      string     `json:"label,omitempty"` // "classify", "director", "worker", "review", "repair"
	At         time.Time  `json:"at"`
	DurationMs int64      `json:"duration_ms,omitempty"`
	Usage      Usage      `json:"usage"`
}

// NewChargeID mints an identity for a charge node. Random rather than
// sequential: two brains sharing one ledger must not collide.
func NewChargeID(kind ChargeKind) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return string(kind[:1]) + "-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return string(kind[:1]) + "-" + hex.EncodeToString(b)
}

// CallHook is fired once per provider call an internal code path makes - a
// classification, a director plan, a review - so the brain can charge it to
// the task that caused it (ROADMAP M1.2). It fires for FAILED calls too:
// quota was spent producing nothing, and a report that counts only successes
// understates what routing costs. label names what the call was for.
type CallHook func(leg Leg, label string, res Result, err error)

// NormalizeUsage fills what can be derived and states what cannot. It never
// invents a total from a subset and never lets an empty struct read as $0
// measured.
func NormalizeUsage(u Usage) Usage {
	if u.Total == 0 && u.Input+u.Output > 0 {
		u.Total = u.Input + u.Output
	}
	// A runtime reporting only a total (most CLI legs) keeps that total; the
	// in/out split stays absent rather than being guessed into the record.
	if u.Status == "" {
		u.Status = UsageUnknown
		if u.Total > 0 {
			u.Status = UsageMeasured
		}
	}
	if u.CostStatus == "" {
		u.CostStatus = UsageUnknown
	}
	if u.CostStatus != UsageUnknown && u.PriceSource == "" {
		u.PriceSource = "runtime"
	}
	return u
}

// CallUsage builds the normalized usage for one finished provider call from
// what the runtime reported. A measured dollar figure wins; otherwise the
// registry prices the tokens and the record says so; a subscription leg is a
// measured $0 marginal charge, not an unknown one.
func CallUsage(leg Leg, tokens int, costUSD float64, raw map[string]any) Usage {
	u := Usage{Total: tokens, Raw: raw}
	switch {
	case costUSD > 0:
		u.CostUSD, u.CostStatus, u.PriceSource = costUSD, UsageMeasured, "runtime"
	case tokens > 0:
		if est := EstimateCost(leg, tokens); est > 0 {
			u.CostUSD, u.CostStatus, u.PriceSource = est, UsageEstimated, "registry:"+string(leg)
		} else if s, ok := Spec(leg); ok && s.Subscription {
			// No marginal dollar by construction: the fee was paid upstream.
			// Quota consumption is a separate column (M1 metric table).
			u.CostStatus, u.PriceSource = UsageMeasured, "subscription:"+string(leg)
		}
	}
	return NormalizeUsage(u)
}

// Totals is a billed roll-up plus the accounting-coverage counts M1 reports
// beside it: a total built from 4 measured and 9 unknown calls is not a bill.
type Totals struct {
	CostUSD   float64
	Tokens    int
	Calls     int
	Measured  int
	Estimated int
	Unknown   int
}

// Complete reports whether every call in the roll-up was measured - the only
// case in which the total may be presented as a billed figure.
func (t Totals) Complete() bool { return t.Calls > 0 && t.Measured == t.Calls }

// maxCharges caps the accounting log the same way maxEvents caps the decision
// log. Three rows per turn plus per-call rows: a few thousand is weeks of use.
const maxCharges = 4000

// RecordCharge appends a charge, or reconciles an existing one idempotently.
// Late usage (a runtime that reports its bill after the stream closed) must
// land on the ORIGINAL attempt, not as a second charge - and must never
// downgrade what was already measured. Returns true when it updated in place.
func (l *Ledger) RecordCharge(c Charge) bool {
	c.Version = AccountingVersion
	if c.ID == "" {
		c.ID = NewChargeID(c.Kind)
	}
	if c.At.IsZero() {
		c.At = time.Now()
	}
	c.Usage = NormalizeUsage(c.Usage)
	for i, old := range l.Charges {
		if old.ID != c.ID {
			continue
		}
		l.Charges[i] = reconcile(old, c)
		return true
	}
	l.Charges = append(l.Charges, c)
	if len(l.Charges) > maxCharges {
		l.Charges = l.Charges[len(l.Charges)-maxCharges:]
	}
	return false
}

// reconcile merges a late report into a stored charge: better knowledge wins,
// worse knowledge is discarded, and identity/parentage never moves.
func reconcile(old, in Charge) Charge {
	out := old
	if in.DurationMs > 0 {
		out.DurationMs = in.DurationMs
	}
	if in.Leg != "" {
		out.Leg = in.Leg
	}
	if in.Label != "" {
		out.Label = in.Label
	}
	// Better knowledge wins; equal knowledge refines (a runtime that reports
	// its final token count after the stream closed is still measured).
	if rank(in.Usage.Status) > rank(old.Usage.Status) || (rank(in.Usage.Status) == rank(old.Usage.Status) && in.Usage.Total > 0) {
		out.Usage.Status = in.Usage.Status
		out.Usage.Input, out.Usage.Output = in.Usage.Input, in.Usage.Output
		out.Usage.Cached, out.Usage.Reasoning = in.Usage.Cached, in.Usage.Reasoning
		out.Usage.Total = in.Usage.Total
	}
	if rank(in.Usage.CostStatus) > rank(old.Usage.CostStatus) || (rank(in.Usage.CostStatus) == rank(old.Usage.CostStatus) && in.Usage.CostUSD > 0) {
		out.Usage.CostUSD, out.Usage.CostStatus = in.Usage.CostUSD, in.Usage.CostStatus
		out.Usage.PriceSource = in.Usage.PriceSource
	}
	if len(in.Usage.Raw) > 0 {
		out.Usage.Raw = in.Usage.Raw
	}
	return out
}

func rank(s UsageStatus) int {
	switch s {
	case UsageMeasured:
		return 2
	case UsageEstimated:
		return 1
	}
	return 0
}

// TaskTotals bills one task. Only KindCall rows carry money, so a stage or
// attempt row that also happens to hold a summary figure cannot be added on
// top of the calls beneath it.
func (l *Ledger) TaskTotals(taskID string) Totals { return totalCalls(l.Charges, taskID) }

// ChargesFor returns all charges belonging to a task, in insertion order.
func (l *Ledger) ChargesFor(taskID string) []Charge {
	var out []Charge
	for _, c := range l.Charges {
		if c.TaskID == taskID {
			out = append(out, c)
		}
	}
	return out
}

// LedgerTotals bills every task the ledger still holds, for the accounting
// coverage line in `captain stats`. Same leaves-only rule.
func (l *Ledger) LedgerTotals() Totals { return totalCalls(l.Charges, "") }

// totalCalls sums the call rows of one task, or of every task when taskID is
// empty.
func totalCalls(charges []Charge, taskID string) Totals {
	var t Totals
	for _, c := range charges {
		if c.Kind != KindCall || (taskID != "" && c.TaskID != taskID) {
			continue
		}
		t.Calls++
		t.Tokens += c.Usage.Total
		t.CostUSD += c.Usage.CostUSD
		switch c.Usage.CostStatus {
		case UsageMeasured:
			t.Measured++
		case UsageEstimated:
			t.Estimated++
		default:
			t.Unknown++
		}
	}
	return t
}

// ChargeTree returns a task's rows in tree order (parents before children,
// siblings oldest first) for `captain why` and the M1 report export.
func ChargeTree(charges []Charge, taskID string) []Charge {
	byParent := map[string][]Charge{}
	for _, c := range charges {
		if c.TaskID == taskID {
			byParent[c.Parent] = append(byParent[c.Parent], c)
		}
	}
	for k := range byParent {
		rows := byParent[k]
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.Before(rows[j].At) })
	}
	var out []Charge
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		if depth > 32 {
			return // a corrupt parent cycle must not hang the report
		}
		for _, c := range byParent[parent] {
			out = append(out, c)
			walk(c.ID, depth+1)
		}
	}
	walk("", 0)
	return out
}
