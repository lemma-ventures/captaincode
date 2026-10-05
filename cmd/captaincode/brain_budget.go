package main

// The run budget: how many turns one typed prompt may start, shared by every
// /repeat round and chain step under it however they nest. Without it,
// "/repeat /repeat /repeat x" ran 100 × 100 × 100 rounds: each level is
// bounded, the product is not (formal/CommandSafety/Execution.lean,
// turnsNow_unbounded). With it, the turns run never exceed the budget
// (run_turns_le). A thread reads the budget from the context of the turn
// that started it, so a loop started inside a round draws on its parent's.

import (
	"context"
	"os"
	"strconv"
	"sync"
)

// runBudgetSize is CAPTAIN_RUN_BUDGET, default 200: a full /repeat (100
// rounds) fits twice.
func runBudgetSize() int {
	if n, err := strconv.Atoi(os.Getenv("CAPTAIN_RUN_BUDGET")); err == nil && n > 0 {
		return n
	}
	return 200
}

type runBudget struct {
	mu          sync.Mutex
	left, total int
}

type budgetKey struct{}

// budgetFor is the budget of the turn ctx belongs to: its parent thread's,
// or a fresh one for a typed prompt.
func budgetFor(ctx context.Context) *runBudget {
	if b, ok := ctx.Value(budgetKey{}).(*runBudget); ok && b != nil {
		return b
	}
	n := runBudgetSize()
	return &runBudget{left: n, total: n}
}

// withBudget is a thread's context: its rounds' turns carry the budget.
func withBudget(ctx context.Context, b *runBudget) context.Context {
	return context.WithValue(ctx, budgetKey{}, b)
}

// take spends one turn; false when the budget is spent. A nil budget (a
// thread built by a test) is unlimited.
func (b *runBudget) take() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.left <= 0 {
		return false
	}
	b.left--
	return true
}

func (b *runBudget) size() int {
	if b == nil {
		return 0
	}
	return b.total
}
