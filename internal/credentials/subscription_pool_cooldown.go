package credentials

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/subscriptions"
)

const SubscriptionUnavailableCooldown = time.Minute

func MarkSubscriptionPoolUnavailable(ctx context.Context, pool subscriptions.Provider, now time.Time) {
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if state == nil {
		return
	}
	if state.UnavailableProviders == nil {
		state.UnavailableProviders = make(map[subscriptions.Provider]time.Time)
	}
	state.UnavailableProviders[pool] = now.Add(SubscriptionUnavailableCooldown)
	observability.FromContext(ctx).Warn("Subscription inference failed on provider capacity; paid rescue eligible subject to funding authorization")
}

func SubscriptionProviderUnavailable(ctx context.Context, pool subscriptions.Provider, now time.Time) bool {
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return state != nil && state.UnavailableProviders[pool].After(now)
}
