package captaincode

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

var ErrOpenShellAttemptCap = errors.New("openshell: attempt cap exceeded")

type OpenShellAttemptBudget struct {
	Limit    int `json:"limit"`
	Required int `json:"required"`
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
	return budget, nil
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
	if (limits.Strict && limits.MaxCostUSD > 0) ||
		(budget != nil && budget.Mode == BudgetStrict && budget.MaxCostUSD > 0) {
		return nil, nil, errors.New("openshell: strict cost budgets are not supported by the pilot")
	}
	cap, _ := ctx.Value(openShellAttemptLimitKey{}).(int)
	cap = tighterOpenShellLimit(cap, limits.MaxAttempts)
	started := time.Now()
	if budget != nil {
		if budget.MaxAttempts < 0 || budget.SettledAttempts != 0 || budget.ReservedAttempts != 0 {
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
