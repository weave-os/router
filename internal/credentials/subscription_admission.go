package credentials

import (
	"context"
	"time"
)

type SubscriptionAdmissionBudget struct {
	Remaining time.Duration
}

func WithSubscriptionAdmissionBudget(ctx context.Context) (context.Context, *SubscriptionAdmissionBudget) {
	if budget, _ := ctx.Value(SubscriptionRotationBudgetKey{}).(*SubscriptionAdmissionBudget); budget != nil {
		return ctx, budget
	}
	budget := &SubscriptionAdmissionBudget{Remaining: SubscriptionRotationBudget}
	return context.WithValue(ctx, SubscriptionRotationBudgetKey{}, budget), budget
}

func (b *SubscriptionAdmissionBudget) LeaseContext(ctx context.Context, now func() time.Time) (context.Context, func()) {
	started := now()
	leaseCtx, cancel := context.WithTimeout(ctx, b.Remaining)
	return leaseCtx, func() {
		b.Remaining = max(0, b.Remaining-now().Sub(started))
		cancel()
	}
}

const SubscriptionRotationBudget = 10 * time.Second
