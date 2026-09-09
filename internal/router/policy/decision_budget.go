package policy

import (
	"context"
	"sync"
	"time"
)

type decisionBudgetKey struct{}

type decisionBudget struct {
	mu       sync.Mutex
	deadline time.Time
}

// WithDecisionBudget shares the first classifier call's time budget across
// retries and reroutes without shortening the parent inference request.
func WithDecisionBudget(ctx context.Context) context.Context {
	if _, exists := ctx.Value(decisionBudgetKey{}).(*decisionBudget); exists {
		return ctx
	}
	return context.WithValue(ctx, decisionBudgetKey{}, &decisionBudget{})
}

// DecisionContext gives a classifier call the remaining shared policy budget.
func DecisionContext(ctx context.Context, now time.Time, timeout time.Duration) (context.Context, context.CancelFunc) {
	deadline := now.Add(timeout)
	if budget, ok := ctx.Value(decisionBudgetKey{}).(*decisionBudget); ok {
		budget.mu.Lock()
		if budget.deadline.IsZero() || deadline.Before(budget.deadline) {
			budget.deadline = deadline
		}
		deadline = budget.deadline
		budget.mu.Unlock()
	}
	return context.WithDeadline(ctx, deadline)
}
