package requestcontext

import "context"

type subscriptionQuotaRecoveryContextKey struct{}

// WithSubscriptionQuotaRecovery lets quota management refresh credentials for
// a cooling-down account. Inference refreshes never carry this marker.
func WithSubscriptionQuotaRecovery(ctx context.Context) context.Context {
	return context.WithValue(ctx, subscriptionQuotaRecoveryContextKey{}, true)
}

// SubscriptionQuotaRecoveryFrom reports whether WithSubscriptionQuotaRecovery marked ctx.
func SubscriptionQuotaRecoveryFrom(ctx context.Context) bool {
	allowed, _ := ctx.Value(subscriptionQuotaRecoveryContextKey{}).(bool)
	return allowed
}
