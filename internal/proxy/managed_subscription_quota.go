package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/subscriptions"
)

func recoveredSubscriptionUsageSnapshot(service *Service, lease subscriptions.Lease) (usage.Snapshot, bool) {
	if observer := service.usageObserver; observer != nil {
		observer.Recover(observer.Key([]byte("subscription-account:"+lease.AccountID)), lease.QuotaResetGeneration)
		observer.Recover(observer.Key([]byte(lease.AccessToken)), lease.QuotaResetGeneration)
	}
	return service.managedSubscriptionUsageSnapshot(lease.AccountID, lease.AccessToken)
}

func exhaustManagedSubscription(ctx context.Context, leaser subscriptions.Leaser, owner auth.SubscriptionOwner, provider subscriptions.Provider, lease subscriptions.Lease, resetAt time.Time) error {
	if health, ok := leaser.(interface {
		ExhaustLease(context.Context, auth.SubscriptionOwner, subscriptions.Provider, subscriptions.Lease, time.Time) error
	}); ok {
		return health.ExhaustLease(ctx, owner, provider, lease, resetAt)
	}
	if health, ok := leaser.(interface {
		Exhaust(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string, time.Time) error
	}); ok {
		return health.Exhaust(ctx, owner, provider, lease.AccountID, resetAt)
	}
	return leaser.Cooldown(ctx, owner, provider, lease.AccountID, resetAt)
}
