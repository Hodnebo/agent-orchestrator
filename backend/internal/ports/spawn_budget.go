package ports

import (
	"context"
	"time"
)

type spawnRequestBudgetKey struct{}

type spawnRequestBudget struct {
	base   time.Duration
	extend func(time.Duration)
}

// WithSpawnRequestBudget marks ctx as a REST request that may run the
// project's Setup (a spawn, an orchestrator start, a restore). The request is
// held to base until the operation knows its project; extend then moves the
// request's deadline to that many from now, so a Setup step longer than one
// request is not cut short while a slow request body still is.
func WithSpawnRequestBudget(ctx context.Context, base time.Duration, extend func(time.Duration)) context.Context {
	return context.WithValue(ctx, spawnRequestBudgetKey{}, spawnRequestBudget{base: base, extend: extend})
}

// ExtendSpawnRequestBudget moves a WithSpawnRequestBudget request's deadline
// to base plus setup from now and returns ctx bounded by it. Outside such a
// request it returns ctx unchanged.
func ExtendSpawnRequestBudget(ctx context.Context, setup time.Duration) (context.Context, context.CancelFunc) {
	budget, ok := ctx.Value(spawnRequestBudgetKey{}).(spawnRequestBudget)
	if !ok || budget.base <= 0 || budget.extend == nil {
		return ctx, func() {}
	}
	total := budget.base + setup
	// The request's own stop comes just after the operation's deadline, so
	// the operation sees context.DeadlineExceeded and reports it.
	budget.extend(total + time.Second)
	return context.WithTimeout(ctx, total)
}
