package proxy

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

func rejectManagedSubscriptionCredential(ctx context.Context, leaser subscriptions.Leaser, owner auth.SubscriptionOwner, provider subscriptions.Provider, accountID string, upstreamStatus int) (bool, error) {
	if rejector, ok := leaser.(interface {
		RejectCredential(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string, int) (bool, error)
	}); ok {
		return rejector.RejectCredential(ctx, owner, provider, accountID, upstreamStatus)
	}
	return true, reconnectManagedSubscription(ctx, leaser, owner, provider, accountID)
}

func reconnectManagedSubscription(ctx context.Context, leaser subscriptions.Leaser, owner auth.SubscriptionOwner, provider subscriptions.Provider, accountID string) error {
	if health, ok := leaser.(interface {
		ReconnectRequired(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string) error
	}); ok {
		return health.ReconnectRequired(ctx, owner, provider, accountID)
	}
	return leaser.Disable(ctx, owner, provider, accountID)
}
