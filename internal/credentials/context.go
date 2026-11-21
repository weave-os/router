package credentials

import (
	"context"

	"weave-os/router/internal/auth"
)

type ExternalAPIKeysContextKey struct{}

type AnthropicSubscriptionContextKey struct{}

type OpenAISubscriptionContextKey struct{}
type OpenAIAccountIDContextKey struct{}

type ManagedSubscriptionProvidersContextKey struct{}

type ManagedSubscriptionEnrollmentUnavailableContextKey struct{}

type ManagedSubscriptionUsageContextKey struct{}

type SubscriptionOwnerContextKey struct{}

func WithSubscriptionOwner(ctx context.Context, owner auth.SubscriptionOwner) context.Context {
	return context.WithValue(ctx, SubscriptionOwnerContextKey{}, owner)
}

func ExternalKeysFromContext(ctx context.Context) []*auth.ExternalAPIKey {
	v := ctx.Value(ExternalAPIKeysContextKey{})
	if v == nil {
		return nil
	}
	keys, _ := v.([]*auth.ExternalAPIKey)
	return keys
}
