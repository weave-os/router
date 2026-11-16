package requestcontext

import "context"

type includedSubscriptionOnlyKey struct{}

func WithIncludedSubscriptionOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, includedSubscriptionOnlyKey{}, true)
}

func IncludedSubscriptionOnlyFrom(ctx context.Context) bool {
	value, _ := ctx.Value(includedSubscriptionOnlyKey{}).(bool)
	return value
}
