package credentials

import (
	"context"
	"time"

	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/subscriptions"
)

func (b *Brokerage) CallerQuotaSpent(ctx context.Context, pool subscriptions.Provider, token string, now time.Time) bool {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil && state.QuotaExhaustedProviders[pool].After(now) {
		return true
	}
	if b.UsageObserver != nil {
		key := b.UsageObserver.Key([]byte(token))
		if b.UsageObserver.Rejected(key) {
			return true
		}
		if snapshot, observed := b.UsageObserver.Snapshot(key); observed && IncludedQuotaSpent(snapshot, now) {
			return true
		}
	}
	return false
}

func IncludedQuotaSpent(snapshot usage.Snapshot, now time.Time) bool {
	return (snapshot.OverageInUse && snapshot.UnifiedResetAt.After(now)) || snapshot.ExhaustedAsOf(now)
}
