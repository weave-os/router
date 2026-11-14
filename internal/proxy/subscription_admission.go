package proxy

import (
	"context"
	"time"
)

// Shared across account and model walks; provider inference never consumes it.
type subscriptionAdmissionBudget struct {
	remaining time.Duration
}

func withSubscriptionAdmissionBudget(ctx context.Context) (context.Context, *subscriptionAdmissionBudget) {
	if budget, _ := ctx.Value(subscriptionRotationBudgetKey{}).(*subscriptionAdmissionBudget); budget != nil {
		return ctx, budget
	}
	budget := &subscriptionAdmissionBudget{remaining: subscriptionRotationBudget}
	return context.WithValue(ctx, subscriptionRotationBudgetKey{}, budget), budget
}

func (b *subscriptionAdmissionBudget) leaseContext(ctx context.Context, now func() time.Time) (context.Context, func()) {
	started := now()
	leaseCtx, cancel := context.WithTimeout(ctx, b.remaining)
	return leaseCtx, func() {
		b.remaining = max(0, b.remaining-now().Sub(started))
		cancel()
	}
}
