package captaincode

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	ErrOpenShellAttemptCap = errors.New("openshell: attempt cap exceeded")
	ErrOpenShellCostCap    = errors.New("openshell: strict cost cap")
)

// openShellPricedProfiles are the pilot's hand-kept OpenRouter lanes
// (profiles.py LANES): every response carries the provider's bill and each
// lane has a price ceiling, so Shield can hold a worker to a dollar
// allocation. NIM returns no price. The registry-generated profiles in the
// pilot's catalog.json are priced the same way (openShellCatalogPriced).
var openShellPricedProfiles = map[string]bool{"cerebras": true, "sambanova": true, "together": true,
	"deepinfra": true, "crusoe": true, "parasail": true}

// openShellMaxCostUSD bounds a strict cap; Shield refuses larger allocations.
const openShellMaxCostUSD = 1000

// openShellDirectorCallUSD is the most one tool-less director call may
// reserve under a strict cap. The subscription returns no bill, so the
// reservation is the charge. CAPTAIN_OPENSHELL_DIRECTOR_USD overrides it.
const openShellDirectorCallUSD = 0.05

// OpenShellCostBudget is a strict dollar cap's admission: the cap split evenly
// across every worker the plan can start, rounded down to a micro-dollar, so
// the allocations never add up to more than the cap. Each worker's Shield
// forwards a request only if its worst case fits the worker's share. Unused
// shares are not reassigned.
type OpenShellCostBudget struct {
	LimitUSD       float64 `json:"limit_usd"`
	WorkerUSD      float64 `json:"worker_usd"`
	Workers        int     `json:"workers"`
	DirectorUSD    float64 `json:"director_usd,omitempty"`
	ReviewReserved bool    `json:"review_reserved,omitempty"`
}

type openShellCostLimitKey struct{}

// OpenShellCostLimit is the strict dollar cap OpenShellBudgetContext put in
// ctx, or zero.
func OpenShellCostLimit(ctx context.Context) float64 {
	limit, _ := ctx.Value(openShellCostLimitKey{}).(float64)
	return limit
}

// openShellStrictCost is the strict cap in force for a call that may happen
// before OpenShellBudgetContext: the context's, else the environment's.
func openShellStrictCost(ctx context.Context) float64 {
	if limit := OpenShellCostLimit(ctx); limit > 0 {
		return limit
	}
	if limits := DefaultBudgetOpts(); limits.Strict && limits.MaxCostUSD > 0 {
		return limits.MaxCostUSD
	}
	return 0
}

// costBudget admits a plan under a strict cap. Every worker must run on a
// priced lane, and no host call may go unpriced: a conflict ruling is a
// subscription call, so each one a stage could need reserves
// openShellDirectorUSD before the workers split the rest. A runner that
// already holds a share (a sequence's stage) keeps it rather than splitting
// the cap again.
func (r *OpenShellRunner) costBudget(ctx context.Context, teams []OpenShellTeam) (*OpenShellCostBudget, error) {
	limit := OpenShellCostLimit(ctx)
	if limit == 0 && r.WorkerCostUSD == 0 {
		return nil, nil
	}
	budget := &OpenShellCostBudget{LimitUSD: limit, WorkerUSD: r.WorkerCostUSD}
	if r.WorkerCostUSD > 0 {
		budget.LimitUSD = r.MaxCostUSD
	}
	directorCalls := 0
	for _, team := range teams {
		edits := 0
		for _, task := range team.Tasks {
			if !openShellPricedProfiles[task.Profile] && !openShellCatalogPriced(r.Pilot, task.Profile) {
				return nil, fmt.Errorf("%w: profile %s returns no price; use an OpenRouter lane such as cerebras", ErrOpenShellCostCap, task.Profile)
			}
			if task.Mode != "review" {
				edits++
			}
		}
		if r.Director != nil && r.WorkerCostUSD == 0 && edits > 1 {
			directorCalls += 2 * (edits / 2)
		}
		budget.Workers += len(team.Tasks)
	}
	callUSD := openShellDirectorUSD()
	if directorCalls > 0 && callUSD < 0 {
		return nil, fmt.Errorf("%w: CAPTAIN_OPENSHELL_DIRECTOR_USD must be a dollar amount below %d", ErrOpenShellCostCap, openShellMaxCostUSD)
	}
	budget.DirectorUSD = float64(directorCalls) * callUSD
	split := budget.LimitUSD - budget.DirectorUSD
	if budget.WorkerUSD == 0 {
		if !(split > 0) {
			return nil, fmt.Errorf("%w: director reservation $%g does not fit in $%g", ErrOpenShellCostCap, budget.DirectorUSD, budget.LimitUSD)
		}
		if !advisoryHostReviewDisabled() && callUSD > 0 && split-callUSD > 0 {
			shareWithReview := math.Floor((split-callUSD)/float64(budget.Workers)*1e6) / 1e6
			if shareWithReview > 0 && float64(budget.Workers)*shareWithReview+budget.DirectorUSD+callUSD <= budget.LimitUSD+1e-9 {
				budget.ReviewReserved = true
				budget.WorkerUSD = shareWithReview
			} else {
				budget.WorkerUSD = math.Floor(split/float64(budget.Workers)*1e6) / 1e6
			}
		} else {
			budget.WorkerUSD = math.Floor(split/float64(budget.Workers)*1e6) / 1e6
		}
	} else if r.WorkerCostUSD > 0 {
		if !advisoryHostReviewDisabled() && callUSD > 0 && budget.LimitUSD-(budget.WorkerUSD*float64(budget.Workers)+budget.DirectorUSD) >= callUSD {
			budget.ReviewReserved = true
		}
	}
	if !(budget.WorkerUSD > 0 && float64(budget.Workers)*budget.WorkerUSD+budget.DirectorUSD <= budget.LimitUSD+1e-9) {
		return nil, fmt.Errorf("%w: $%g cannot cover %d workers and a $%g director reservation", ErrOpenShellCostCap, budget.LimitUSD, budget.Workers, budget.DirectorUSD)
	}
	return budget, nil
}

func openShellTasksCommitted(tasks []*OpenShellResult) (float64, bool) {
	var usd float64
	for _, t := range tasks {
		if t == nil || t.Report == nil {
			continue
		}
		if t.NotStarted || (t.Report.WorkerAttempts != nil && *t.Report.WorkerAttempts == 0 && t.Report.Shield == nil) {
			continue
		}
		if t.Report.Shield == nil || t.Report.Shield.Budget == nil {
			return 0, false
		}
		c := t.Report.Shield.Budget.CommittedUSD
		if c < 0 || math.IsNaN(c) || math.IsInf(c, 0) {
			return 0, false
		}
		usd += c
	}
	return usd, true
}

func openShellStageCommitted(run *OpenShellRun) (float64, bool) {
	if run == nil {
		return 0, false
	}
	return openShellTasksCommitted(run.Tasks)
}

func openShellRunCommitted(run *OpenShellRun) (float64, bool) {
	usd, ok := openShellTasksCommitted(run.Tasks)
	if !ok {
		return 0, false
	}
	for _, s := range run.SetAside {
		if !s.CommittedKnown {
			return 0, false
		}
		usd += s.CommittedUSD
	}
	return usd, true
}

// openShellRecoverySpent is what a resume must subtract from the cap.
// A stopped stage with no Shield budget counts as its full worker share,
// so an unknown bill cannot free that money for a second run.
func openShellRecoverySpent(recovered []*OpenShellRun, aside []OpenShellSetAside, share float64) (float64, bool) {
	var sum float64
	for _, stage := range recovered {
		usd, ok := openShellStageCommitted(stage)
		if !ok {
			return 0, false
		}
		sum += usd
	}
	for _, s := range aside {
		if s.CommittedKnown {
			sum += s.CommittedUSD
			continue
		}
		if !(share > 0) {
			return 0, false
		}
		sum += share
	}
	return sum, true
}

func openShellDirectorUSD() float64 {
	raw := strings.TrimSpace(os.Getenv("CAPTAIN_OPENSHELL_DIRECTOR_USD"))
	if raw == "" {
		return openShellDirectorCallUSD
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || !(v > 0 && v < openShellMaxCostUSD) {
		return -1
	}
	return v
}

// BilledUSD is the amount the ledger charges: the provider bill, or Shield's
// committed amount when that bill is incomplete.
func (r Result) BilledUSD() float64 {
	if r.CostUSD > 0 {
		return r.CostUSD
	}
	return r.CostCommitted
}

// OpenShellUsage is the ledger row for one sandbox result. A complete Shield
// bill is measured. An incomplete bill charges the committed reservation and
// says so. Neither path prices the sandbox as a free model.
func OpenShellUsage(res Result) Usage {
	return BilledUsage(LegOpenShell, res)
}

// BilledUsage is the ledger row for the amount BilledUSD charges. A provider
// bill is measured; a committed amount with no bill is an estimate, never a
// measured cost.
func BilledUsage(leg Leg, res Result) Usage {
	if res.CostUSD > 0 {
		return CallUsage(leg, res.Tokens, res.CostUSD, nil)
	}
	if res.CostCommitted > 0 {
		return NormalizeUsage(Usage{Total: res.Tokens, CostUSD: res.CostCommitted, CostStatus: UsageEstimated, PriceSource: "shield-committed"})
	}
	return CallUsage(leg, res.Tokens, 0, nil)
}

// checkCostReport confirms a worker's Shield enforced its share: a report
// without the budget, with another limit, or with a breached bound fails the
// worker rather than being taken on trust.
func (r *OpenShellRunner) checkCostReport(report *OpenShellReport) error {
	if r.WorkerCostUSD == 0 {
		return nil
	}
	if report == nil || report.Shield == nil || report.Shield.Budget == nil {
		return fmt.Errorf("%w: the worker's Shield reported no budget", ErrOpenShellCostCap)
	}
	b := report.Shield.Budget
	switch {
	case b.LimitUSD != r.WorkerCostUSD:
		return fmt.Errorf("%w: the worker's Shield enforced $%g, not its $%g share", ErrOpenShellCostCap, b.LimitUSD, r.WorkerCostUSD)
	case b.Breached:
		return fmt.Errorf("%w: a provider bill exceeded its reservation", ErrOpenShellCostCap)
	case !(b.CommittedUSD >= 0 && b.CommittedUSD <= b.LimitUSD):
		return fmt.Errorf("%w: the worker's Shield committed $%g of a $%g share", ErrOpenShellCostCap, b.CommittedUSD, b.LimitUSD)
	}
	return nil
}

type OpenShellAttemptUsage struct {
	Workers    int `json:"workers"`
	Repairs    int `json:"repairs"`
	Directors  int `json:"directors"`
	Unmeasured int `json:"unmeasured"`
}

func (u OpenShellAttemptUsage) Total() int {
	return u.Workers + u.Repairs + u.Directors
}

func (u OpenShellAttemptUsage) Validate() error {
	limit := openShellMaxTasks * MaxWorkflowStages
	if u.Workers < 0 || u.Workers > limit || u.Repairs < 0 || u.Repairs > u.Workers ||
		u.Directors < 0 || u.Directors > limit || u.Unmeasured < 0 || u.Unmeasured > 2*limit {
		return errors.New("openshell: invalid attempt usage")
	}
	return nil
}

// openShellRefused is the result of a refusal before any sandbox started. No
// model was called, so the count is a known zero; a nil count means unknown.
func openShellRefused(err error) (Result, error) {
	return Result{OpenShellAttempts: &OpenShellAttemptUsage{}}, err
}

func (u *OpenShellAttemptUsage) add(other *OpenShellAttemptUsage) {
	if other == nil {
		u.Unmeasured++
		return
	}
	u.Workers += other.Workers
	u.Repairs += other.Repairs
	u.Directors += other.Directors
	u.Unmeasured += other.Unmeasured
}

func (run *OpenShellRun) measuredAttempts(tasks ...OpenShellTask) *OpenShellAttemptUsage {
	usage := &OpenShellAttemptUsage{}
	for _, task := range run.Tasks {
		if task != nil && task.NotStarted && task.Report == nil {
			continue
		}
		if task == nil || task.Report == nil || task.Report.WorkerAttempts == nil {
			usage.Unmeasured++
			continue
		}
		n := *task.Report.WorkerAttempts
		maxAttempts := 1 + task.task.RepairAttempts
		for _, spec := range tasks {
			if spec.ID == task.Task {
				maxAttempts = 1 + spec.RepairAttempts
				break
			}
		}
		if n < 0 || n > maxAttempts {
			usage.Unmeasured++
			continue
		}
		if n > 0 {
			usage.Workers++
			usage.Repairs += n - 1
		}
	}
	for _, ruling := range run.Rulings {
		if ruling.Attempts == nil || *ruling.Attempts < 0 || *ruling.Attempts > 2 {
			usage.Unmeasured++
		} else {
			usage.Directors += *ruling.Attempts
		}
	}
	return usage
}

func (l *Ledger) ReconcileOpenShellAttempts(attemptID string, usage *OpenShellAttemptUsage) error {
	return l.ReconcileOpenShellSpend(attemptID, usage, 0)
}

// ReconcileOpenShellSpend settles attempt counts and adds costUSD to the
// task budget. costUSD is the provider bill, or Shield's committed amount
// when that bill is incomplete. A second call with the same counts does not
// add the cost again.
func (l *Ledger) ReconcileOpenShellSpend(attemptID string, usage *OpenShellAttemptUsage, costUSD float64) error {
	if costUSD < 0 || math.IsNaN(costUSD) || math.IsInf(costUSD, 0) {
		return errors.New("openshell: invalid committed spend")
	}
	as := l.AttemptStateFor(attemptID)
	if as == nil || as.Leg != LegOpenShell {
		return errors.New("openshell: attempt usage requires a sandbox attempt")
	}
	if usage == nil {
		usage = &OpenShellAttemptUsage{Unmeasured: 1}
	}
	if err := usage.Validate(); err != nil {
		return err
	}
	if as.OpenShellAttempts != nil {
		if *as.OpenShellAttempts != *usage {
			return errors.New("openshell: settled attempt usage changed")
		}
		return nil
	}
	budget := l.BudgetFor(as.TaskID)
	if budget == nil {
		budget = NewBudget(as.TaskID, "sandbox", BudgetOpts{})
		if task := l.TaskStateFor(as.TaskID); task != nil {
			budget.StartedAt, budget.Label = task.StartedAt, task.Label
		}
		l.RecordBudget(budget)
		budget = l.BudgetFor(as.TaskID)
	}
	if budget.ReservedAttempts != 0 {
		return errors.New("openshell: cannot settle usage against unrelated reservations")
	}
	budget.Reconcile(usage.Total(), costUSD)
	budget.UnmeasuredExecutions += usage.Unmeasured
	if usage.Unmeasured > 0 {
		budget.Stop(StopAttemptUsageUnknown)
	}
	copy := *usage
	as.OpenShellAttempts = &copy
	as.UpdatedAt = time.Now()
	return nil
}

type openShellDirectorAttemptsKey struct{}

type openShellDirectorAttempts struct {
	count int
	known bool
}

type OpenShellAttemptBudget struct {
	Limit          int  `json:"limit"`
	Required       int  `json:"required"`
	ReviewReserved bool `json:"review_reserved,omitempty"`
}

type openShellAttemptLimitKey struct{}

// openShellRootDeadlineKey carries the task budget's deadline, the only one a
// sequence plan saves. A worker timeout also bounds the context, but it limits
// one dispatch: saving it would refuse recovery once that timeout had passed.
type openShellRootDeadlineKey struct{}

func tighterOpenShellLimit(a, b int) int {
	if a == 0 || (b > 0 && b < a) {
		return b
	}
	return a
}

func (r *OpenShellRunner) attemptBudget(ctx context.Context, teams []OpenShellTeam) (*OpenShellAttemptBudget, error) {
	limit, _ := ctx.Value(openShellAttemptLimitKey{}).(int)
	if r.MaxAttempts < 0 {
		return nil, errors.New("openshell: invalid saved attempt cap")
	}
	budget := &OpenShellAttemptBudget{Limit: tighterOpenShellLimit(limit, r.MaxAttempts)}
	for _, team := range teams {
		if err := team.Validate(); err != nil {
			return nil, err
		}
		edits := 0
		for _, task := range team.Tasks {
			budget.Required += 1 + task.RepairAttempts
			if task.Mode != "review" {
				edits++
			}
		}
		if r.Director != nil {
			budget.Required += 2 * (edits / 2)
		}
	}
	if budget.Limit > 0 && budget.Required > budget.Limit {
		return budget, fmt.Errorf("%w: plan requires %d attempt slots; cap is %d", ErrOpenShellAttemptCap, budget.Required, budget.Limit)
	}
	if !advisoryHostReviewDisabled() {
		if budget.Limit == 0 || budget.Required+1 <= budget.Limit {
			budget.ReviewReserved = true
			budget.Required++
		} else {
			budget.ReviewReserved = false
		}
	}
	return budget, nil
}

// rerunFits admits a recovery that runs a stopped stage again only if the
// attempts already used, every set-aside run included, plus the worst case of
// the stages left fit the cap. An unknown count cannot be shown to fit.
func (r *OpenShellRunner) rerunFits(ctx context.Context, teams []OpenShellTeam, recovered []*OpenShellRun, setAside []OpenShellSetAside, pending *OpenShellSetAside) error {
	used := &OpenShellAttemptUsage{}
	for _, stage := range recovered {
		used.add(stage.AttemptUsage)
	}
	for i := range setAside {
		used.add(setAside[i].Attempts)
	}
	if pending != nil {
		used.add(pending.Attempts)
	}
	left, err := r.attemptBudget(ctx, teams[len(recovered):])
	if err != nil {
		return err
	}
	if left.Limit > 0 && (used.Unmeasured > 0 || used.Total()+left.Required > left.Limit) {
		return fmt.Errorf("%w: resuming at stage %d needs %d more attempt slots after %d used (%d unknown); cap is %d",
			ErrOpenShellAttemptCap, len(recovered)+1, left.Required, used.Total(), used.Unmeasured, left.Limit)
	}
	return nil
}

func OpenShellBudgetContext(ctx context.Context, budget *Budget) (context.Context, context.CancelFunc, error) {
	limits := DefaultBudgetOpts()
	if raw := os.Getenv("CAPTAIN_MAX_ATTEMPTS"); raw != "" {
		if n, err := strconv.Atoi(raw); err != nil || n < 0 {
			return nil, nil, errors.New("openshell: CAPTAIN_MAX_ATTEMPTS must be a non-negative integer")
		}
	}
	if raw := os.Getenv("CAPTAIN_MAX_WALLTIME"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 || (d > 0 && d < time.Millisecond) {
			return nil, nil, errors.New("openshell: CAPTAIN_MAX_WALLTIME must be zero or a duration of at least 1ms")
		}
	}
	// A strict dollar cap is enforced per request at each worker's Shield.
	// An unreadable one refuses: running without it would spend unbounded.
	cost := OpenShellCostLimit(ctx)
	if limits.Strict {
		if raw := os.Getenv("CAPTAIN_MAX_COST"); raw != "" {
			if v, err := strconv.ParseFloat(raw, 64); err != nil || !(v >= 0 && v < openShellMaxCostUSD) {
				return nil, nil, fmt.Errorf("openshell: CAPTAIN_MAX_COST must be a dollar amount below %d", openShellMaxCostUSD)
			}
		}
		if limits.MaxCostUSD > 0 && (cost == 0 || limits.MaxCostUSD < cost) {
			cost = limits.MaxCostUSD
		}
	}
	if budget != nil && budget.Mode == BudgetStrict {
		if !(budget.MaxCostUSD > 0 && budget.MaxCostUSD < openShellMaxCostUSD) {
			return nil, nil, errors.New("openshell: invalid strict cost budget")
		}
		if cost == 0 || budget.MaxCostUSD < cost {
			cost = budget.MaxCostUSD
		}
	}
	if cost > 0 {
		ctx = context.WithValue(ctx, openShellCostLimitKey{}, cost)
	}
	cap, _ := ctx.Value(openShellAttemptLimitKey{}).(int)
	cap = tighterOpenShellLimit(cap, limits.MaxAttempts)
	started := time.Now()
	if budget != nil {
		if budget.MaxAttempts < 0 || budget.SettledAttempts != 0 || budget.ReservedAttempts != 0 || budget.UnmeasuredExecutions != 0 {
			return nil, nil, errors.New("openshell: plan admission needs an unused root attempt budget")
		}
		cap = tighterOpenShellLimit(cap, budget.MaxAttempts)
		if budget.StartedAt.IsZero() || budget.StartedAt.After(started) || budget.MaxWallMs < 0 {
			return nil, nil, errors.New("openshell: invalid budget start time")
		}
		if budget.StopReason != "" || !budget.StoppedAt.IsZero() {
			return nil, nil, fmt.Errorf("openshell: task budget stopped: %s", budget.StopReason)
		}
		started = budget.StartedAt
	}
	ctx = context.WithValue(ctx, openShellAttemptLimitKey{}, cap)
	wall := limits.MaxWallMs
	if budget != nil && budget.MaxWallMs > 0 && (wall <= 0 || budget.MaxWallMs < wall) {
		wall = budget.MaxWallMs
	}
	var cancel context.CancelFunc
	if wall > 0 {
		if wall > math.MaxInt64/int64(time.Millisecond) {
			return nil, nil, errors.New("openshell: wall-time limit is too large")
		}
		deadline := started.Add(time.Duration(wall) * time.Millisecond)
		if root, ok := ctx.Value(openShellRootDeadlineKey{}).(time.Time); ok && root.Before(deadline) {
			deadline = root
		}
		ctx = context.WithValue(ctx, openShellRootDeadlineKey{}, deadline)
		ctx, cancel = context.WithDeadline(ctx, deadline)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("openshell: task budget unavailable: %w", err)
	}
	return ctx, cancel, nil
}

func (r *OpenShellRunner) deadlineContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if root, ok := ctx.Value(openShellRootDeadlineKey{}).(time.Time); ok && (r.DeadlineAt.IsZero() || root.Before(r.DeadlineAt)) {
		r.DeadlineAt = root.UTC()
	}
	if !r.DeadlineAt.IsZero() {
		ctx = context.WithValue(ctx, openShellRootDeadlineKey{}, r.DeadlineAt)
		return context.WithDeadline(ctx, r.DeadlineAt)
	}
	return context.WithCancel(ctx)
}
