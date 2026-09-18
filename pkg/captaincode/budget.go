package captaincode

// Shared resource controller (ROADMAP M2.4). The per-path bounds that existed
// before — two reroute hops, one gate retry, one narration nudge — are each
// independently capped with no aggregate limit. A workflow with three stages
// of two workers, each with a gate retry and a narration nudge, could make
// twelve provider calls with no aggregate ceiling. The resource controller
// gives one root task one budget that every nested call draws from: provider
// retries, gate repair, narration nudges, director plans, reviews and
// synthesis all reserve before dispatch and reconcile after, so a task that
// has already spent its attempts cannot multiply them by rerouting.
//
// Three invariants the controller enforces:
//
//   - reserve before dispatch, reconcile after. A call that has not reserved
//     must not run; a reservation that was never reconciled is a leak the
//     next reserve sees as outstanding.
//   - settled + reserved must fit the cap. Before concurrent dispatch, the
//     worst-case (every in-flight call succeeds) must leave room; a second
//     team worker that would push outstanding past the cap is refused.
//   - nested routers do not multiply retry allowances. A workflow's gate
//     repair and a solo turn's narration nudge draw from the SAME root task
//     budget, not separate per-stage pools.
//
// A hard dollar ceiling is offered only when the adapter can enforce an upper
// bound across the entire delegated run (ROADMAP M2.4). No current adapter can,
// so cost is tracked for visibility and not enforced. The attempt cap and
// wall-time cap are the enforceable limits.

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BudgetVersion is stamped on every budget. Readers reject what they do not
// understand, the same contract AccountingVersion and QuotaVersion carry.
const BudgetVersion = 1

// BudgetMode controls how a cost cap is treated (ROADMAP M2.4 remaining).
//
// In admission mode (the default) a cost cap is tracked but not enforced:
// no current adapter can bound the cost of an entire delegated run including
// internal tool turns and late charges, so the dollar figure is a report
// column, not a gate. The budget still records what was spent.
//
// In strict mode a cost cap is a hard requirement: legs whose transport
// cannot report per-turn cost (CapCost != yes) are rejected at the routing
// gate, because a cap the adapter cannot measure is a cap the system cannot
// enforce. The attempt cap and wall-time cap are enforced in both modes.
const (
	BudgetAdmission = "admission"
	BudgetStrict    = "strict"
)

// Stopping reasons (ROADMAP M2.5). A budget that refused further dispatch
// records WHY, so `captain budget` and `captain why` can say "stopped after
// 8 attempts" rather than "stopped". The reason is a machine-readable token;
// the human-readable form is StopReasonText.
const (
	StopAttemptsExhausted = "attempts_exhausted"
	StopCostExhausted     = "cost_exhausted"
	StopTimeExhausted     = "time_exhausted"
	StopAllLegsFailed     = "all_legs_failed"
	StopObjectiveMet      = "objective_met"
	StopObjectiveFailed   = "objective_failed"
	StopUserCancelled     = "user_cancelled"
)

// StopReasonText maps a stopping token to a human-readable sentence.
func StopReasonText(reason string) string {
	switch reason {
	case StopAttemptsExhausted:
		return "attempt cap reached"
	case StopCostExhausted:
		return "cost cap reached"
	case StopTimeExhausted:
		return "wall-time cap reached"
	case StopAllLegsFailed:
		return "all legs failed or cooled down"
	case StopObjectiveMet:
		return "objective check passed"
	case StopObjectiveFailed:
		return "objective check failed after bounded retries"
	case StopUserCancelled:
		return "user cancelled"
	}
	return reason
}

// Budget is one root task's shared resource envelope. It lives in the ledger
// (persisted) and is guarded by budgetMu on the brain for in-flight mutations.
// After a restart, outstanding reservations are what they were when last saved
// — M2 does not automatically continue; M3 adds safe recovery (ROADMAP).
type Budget struct {
	Version          int       `json:"version"`
	TaskID           string    `json:"task_id"`
	Label            string    `json:"label,omitempty"`
	Mode             string    `json:"mode,omitempty"`
	MaxAttempts      int       `json:"max_attempts"` // 0 = unlimited
	MaxCostUSD       float64   `json:"max_cost_usd"` // 0 = no cap (not enforced without adapter support)
	MaxWallMs        int64     `json:"max_wall_ms"`  // 0 = unlimited
	SettledAttempts  int       `json:"settled_attempts"`
	SettledCostUSD   float64   `json:"settled_cost_usd"`
	ReservedAttempts int       `json:"reserved_attempts"`
	ReservedCostUSD  float64   `json:"reserved_cost_usd"`
	StartedAt        time.Time `json:"started_at"`
	StoppedAt        time.Time `json:"stopped_at,omitempty"`
	StopReason       string    `json:"stop_reason,omitempty"`
}

// BudgetOpts configures a new budget from environment defaults.
type BudgetOpts struct {
	MaxAttempts int
	MaxCostUSD  float64
	MaxWallMs   int64
	Strict      bool
}

// DefaultBudgetOpts reads the environment for budget limits. Zero means
// unlimited (the default): the controller tracks everything but enforces
// nothing until the operator sets a cap. This is the conservative deployment:
// add the tracking, make it visible via `captain budget`, turn on enforcement
// after seeing what typical tasks cost.
func DefaultBudgetOpts() BudgetOpts {
	return BudgetOpts{
		MaxAttempts: envIntDefault("CAPTAIN_MAX_ATTEMPTS", 0),
		MaxCostUSD:  envFloatDefault("CAPTAIN_MAX_COST", 0),
		MaxWallMs:   envDurationMsDefault("CAPTAIN_MAX_WALLTIME", 0),
		Strict:      os.Getenv("CAPTAIN_STRICT") == "1" || os.Getenv("CAPTAIN_STRICT") == "true",
	}
}

// NewBudget creates a budget for a root task.
func NewBudget(taskID, label string, opts BudgetOpts) *Budget {
	mode := BudgetAdmission
	if opts.Strict && opts.MaxCostUSD > 0 {
		mode = BudgetStrict
	}
	return &Budget{
		Version:     BudgetVersion,
		TaskID:      taskID,
		Label:       label,
		Mode:        mode,
		MaxAttempts: opts.MaxAttempts,
		MaxCostUSD:  opts.MaxCostUSD,
		MaxWallMs:   opts.MaxWallMs,
		StartedAt:   time.Now(),
	}
}

// Reserve atomically checks whether settled + reserved + n fits the attempt
// cap, and if so adds n to the reserved count. Returns false when the cap
// would be exceeded — the caller must NOT dispatch. A budget with
// MaxAttempts=0 (unlimited) always reserves.
//
// The check is settled + reserved + n, not settled + n, because outstanding
// reservations represent in-flight work that has not yet reconciled. Dropping
// them from the check would let two concurrent workers each see "room for one
// more" and both dispatch past the cap.
//
// The caller must hold the brain's budgetMu (or be in a single-goroutine
// context). Reserve does not take its own lock.
func (b *Budget) Reserve(n int) bool {
	if n <= 0 {
		return true
	}
	if b.isStopped() && b.wallTimeExceeded() {
		b.stop(StopTimeExhausted)
	}
	if b.isStopped() {
		return false
	}
	if b.MaxAttempts > 0 && b.SettledAttempts+b.ReservedAttempts+n > b.MaxAttempts {
		return false
	}
	b.ReservedAttempts += n
	return true
}

// Reconcile settles one reservation against actual usage: the reserved
// attempts move to settled, and the actual cost is added to the settled total.
// A reservation that was never made is a bug — but reconcile handles it by
// treating the call as settled directly, because the money was real
// regardless of whether the bookkeeping was correct. Duplicate reconciles
// for the same reservation are idempotent: reserved is only decremented once.
//
// The caller must hold the brain's budgetMu.
func (b *Budget) Reconcile(reservedAttempts int, actualCostUSD float64) {
	if reservedAttempts <= 0 && actualCostUSD == 0 {
		return
	}
	move := reservedAttempts
	if move > b.ReservedAttempts {
		move = b.ReservedAttempts
	}
	b.ReservedAttempts -= move
	b.SettledAttempts += reservedAttempts
	b.SettledCostUSD += actualCostUSD
	if b.MaxCostUSD > 0 && b.SettledCostUSD >= b.MaxCostUSD {
		b.stop(StopCostExhausted)
	}
	if b.MaxAttempts > 0 && b.SettledAttempts+b.ReservedAttempts >= b.MaxAttempts && b.ReservedAttempts == 0 {
		b.stop(StopAttemptsExhausted)
	}
}

// Stop records a stopping reason and timestamp. A budget that was already
// stopped keeps its first reason: the original cause is what the report needs.
func (b *Budget) Stop(reason string) {
	b.stop(reason)
}

func (b *Budget) stop(reason string) {
	if !b.StoppedAt.IsZero() {
		return
	}
	b.StoppedAt = time.Now()
	b.StopReason = reason
}

// Exhausted reports whether this budget refuses further dispatch. A stopped
// budget is exhausted; a budget whose wall-time cap has been reached is
// exhausted even if Stop was not explicitly called.
//
// The caller must hold the brain's budgetMu.
func (b *Budget) Exhausted() bool {
	if b.isStopped() {
		return true
	}
	if b.wallTimeExceeded() {
		b.stop(StopTimeExhausted)
		return true
	}
	if b.MaxAttempts > 0 && b.SettledAttempts+b.ReservedAttempts >= b.MaxAttempts {
		return true
	}
	return false
}

// CanReserve reports whether n more attempts can be reserved without actually
// reserving them. Useful for pre-flight checks ("would this dispatch fit?").
//
// The caller must hold the brain's budgetMu.
func (b *Budget) CanReserve(n int) bool {
	if n <= 0 {
		return true
	}
	if b.isStopped() {
		return false
	}
	if b.wallTimeExceeded() {
		return false
	}
	if b.MaxAttempts > 0 && b.SettledAttempts+b.ReservedAttempts+n > b.MaxAttempts {
		return false
	}
	return true
}

// Remaining returns how many attempts are left under the cap, or -1 when
// unlimited.
func (b *Budget) Remaining() int {
	if b.MaxAttempts == 0 {
		return -1
	}
	r := b.MaxAttempts - b.SettledAttempts - b.ReservedAttempts
	if r < 0 {
		r = 0
	}
	return r
}

// Summary returns a one-line status for `captain budget`.
func (b *Budget) Summary() string {
	outstanding := b.ReservedAttempts
	settled := b.SettledAttempts
	cost := b.SettledCostUSD
	var cap string
	if b.MaxAttempts > 0 {
		cap = fmt.Sprintf("/%d", b.MaxAttempts)
	}
	status := "active"
	if !b.StoppedAt.IsZero() {
		status = StopReasonText(b.StopReason)
	} else if b.wallTimeExceeded() {
		status = StopReasonText(StopTimeExhausted)
	}
	mode := b.Mode
	if mode == "" {
		mode = BudgetAdmission
	}
	age := time.Since(b.StartedAt).Round(time.Second)
	return fmt.Sprintf("%s: %d%s attempts (%d in flight), $%.4f settled, %s, %s, started %s ago",
		b.TaskID, settled, cap, outstanding, cost, status, mode, age)
}

func (b *Budget) isStopped() bool {
	return !b.StoppedAt.IsZero()
}

func (b *Budget) wallTimeExceeded() bool {
	if b.MaxWallMs <= 0 {
		return false
	}
	return time.Since(b.StartedAt).Milliseconds() > b.MaxWallMs
}

// IsStrict reports whether this budget enforces a hard cost cap. In strict
// mode, legs whose transport cannot report per-turn cost are rejected at the
// routing gate (CostEnforceable returns a non-empty reason).
func (b *Budget) IsStrict() bool { return b.Mode == BudgetStrict }

// CostEnforceable returns "" when a leg's transport can report per-turn cost
// (CapCost == yes), or a human-readable rejection reason when it cannot. In
// strict mode, a leg that cannot report cost cannot be held to a dollar cap,
// so it is rejected before dispatch rather than discovered after.
func CostEnforceable(l Leg) string {
	if SupportsCap(l, CapCost) == SupportYes {
		return ""
	}
	caps := CapabilitiesFor(l)
	return fmt.Sprintf("cost reporting %s on transport %s — cannot enforce a hard cost cap", caps.Supports(CapCost), caps.Transport)
}

// ---- ledger integration ----

// maxBudgets caps the budget log the same way maxCharges caps the accounting
// log. One per task: a few dozen is weeks of use.
const maxBudgets = 200

// RecordBudget stores or updates a budget in the ledger. The task ID is the
// key: one budget per root task, updated in place as reservations reconcile.
func (l *Ledger) RecordBudget(b *Budget) {
	if b == nil {
		return
	}
	b.Version = BudgetVersion
	for i, old := range l.Budgets {
		if old.TaskID == b.TaskID {
			l.Budgets[i] = *b
			return
		}
	}
	l.Budgets = append(l.Budgets, *b)
	if len(l.Budgets) > maxBudgets {
		l.Budgets = l.Budgets[len(l.Budgets)-maxBudgets:]
	}
}

// BudgetFor returns the budget for a task, or nil when none exists.
func (l *Ledger) BudgetFor(taskID string) *Budget {
	for i, b := range l.Budgets {
		if b.TaskID == taskID {
			return &l.Budgets[i]
		}
	}
	return nil
}

// RecentBudgets returns budgets newest-first, for `captain budget`.
func (l *Ledger) RecentBudgets() []Budget {
	out := make([]Budget, len(l.Budgets))
	copy(out, l.Budgets)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// BudgetCoverage returns aggregate counts for `captain stats`: how many tasks
// have budgets, how many are stopped, and the total settled cost across all.
func (l *Ledger) BudgetCoverage() (total, stopped int, settledCost float64) {
	for _, b := range l.Budgets {
		total++
		if !b.StoppedAt.IsZero() {
			stopped++
		}
		settledCost += b.SettledCostUSD
	}
	return
}

// FormatBudget renders one budget for `captain budget`.
func FormatBudget(b Budget) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  %s", b.TaskID)
	if b.Label != "" {
		fmt.Fprintf(&sb, " (%s)", truncateStr(b.Label, 60))
	}
	sb.WriteString("\n")
	mode := b.Mode
	if mode == "" {
		mode = BudgetAdmission
	}
	fmt.Fprintf(&sb, "    mode:     %s\n", mode)
	cap := "unlimited"
	if b.MaxAttempts > 0 {
		cap = fmt.Sprintf("%d", b.MaxAttempts)
	}
	status := "active"
	if !b.StoppedAt.IsZero() {
		status = StopReasonText(b.StopReason)
	}
	fmt.Fprintf(&sb, "    attempts: %d settled + %d reserved (cap %s)\n",
		b.SettledAttempts, b.ReservedAttempts, cap)
	fmt.Fprintf(&sb, "    cost:     $%.4f settled", b.SettledCostUSD)
	if b.MaxCostUSD > 0 {
		fmt.Fprintf(&sb, " / $%.4f cap", b.MaxCostUSD)
		if b.Mode == BudgetStrict {
			sb.WriteString(" (strict: cost-reporting legs only)")
		} else {
			sb.WriteString(" (admission: tracked, not enforced)")
		}
	}
	sb.WriteString("\n")
	if b.MaxWallMs > 0 {
		fmt.Fprintf(&sb, "    wall:     %s cap\n", time.Duration(b.MaxWallMs)*time.Millisecond)
	}
	fmt.Fprintf(&sb, "    status:   %s\n", status)
	if !b.StoppedAt.IsZero() {
		fmt.Fprintf(&sb, "    stopped:  %s\n", b.StoppedAt.Format("15:04:05"))
	}
	fmt.Fprintf(&sb, "    started:  %s\n", b.StartedAt.Format("15:04:05"))
	return sb.String()
}

// ---- env helpers (budget.go owns them to keep the package self-contained) ----

func envIntDefault(name string, def int) int {
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

func envFloatDefault(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func envDurationMsDefault(name string, def int64) int64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d.Milliseconds()
}
