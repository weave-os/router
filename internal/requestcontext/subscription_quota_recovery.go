package requestcontext

import "context"

type subscriptionQuotaRecoveryContextKey struct{}

func WithSubscriptionQuotaRecovery(ctx context.Context) context.Context {
	return context.WithValue(ctx, subscriptionQuotaRecoveryContextKey{}, true)
}

func SubscriptionQuotaRecoveryFrom(ctx context.Context) bool {
	allowed, _ := ctx.Value(subscriptionQuotaRecoveryContextKey{}).(bool)
	return allowed
}
