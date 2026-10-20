package ports

import (
	"context"
	"time"
)

type spawnRequestBudgetKey struct{}

// WithSpawnRequestBudget marks ctx as a REST spawn that runs under a wide outer
// ceiling instead of the REST layer's flat deadline: the spawn bounds itself at
// budget plus its project's Setup step time limits, so a Setup step longer than
// one request is not cut short.
func WithSpawnRequestBudget(ctx context.Context, budget time.Duration) context.Context {
	return context.WithValue(ctx, spawnRequestBudgetKey{}, budget)
}

// SpawnRequestBudget is the budget WithSpawnRequestBudget recorded, if any.
func SpawnRequestBudget(ctx context.Context) (time.Duration, bool) {
	budget, ok := ctx.Value(spawnRequestBudgetKey{}).(time.Duration)
	return budget, ok && budget > 0
}
