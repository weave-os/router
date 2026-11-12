package requestcontext

import "context"

type subscriptionExtraUsageContextKey struct{}

// WithSubscriptionExtraUsage lets this request spend the caller's own
// subscription extra usage once included quota is spent. This permits Codex
// credits and Claude Enterprise overage, without lifting provider cooldowns.
func WithSubscriptionExtraUsage(ctx context.Context) context.Context {
	return context.WithValue(ctx, subscriptionExtraUsageContextKey{}, true)
}

// SubscriptionExtraUsageFrom reports whether WithSubscriptionExtraUsage marked ctx.
func SubscriptionExtraUsageFrom(ctx context.Context) bool {
	allowed, _ := ctx.Value(subscriptionExtraUsageContextKey{}).(bool)
	return allowed
}
