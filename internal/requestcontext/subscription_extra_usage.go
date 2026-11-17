package requestcontext

import "context"

type subscriptionExtraUsageContextKey struct{}

func WithSubscriptionExtraUsage(ctx context.Context) context.Context {
	return context.WithValue(ctx, subscriptionExtraUsageContextKey{}, true)
}

func SubscriptionExtraUsageFrom(ctx context.Context) bool {
	allowed, _ := ctx.Value(subscriptionExtraUsageContextKey{}).(bool)
	return allowed
}
