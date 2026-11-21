package proxy

import (
	"context"
	"weave-os/router/internal/requestcontext"
)

func WithSubscriptionStateModels(ctx context.Context, active, inactive []string) context.Context {
	ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionStateModelsEnabledContextKey{}, true)
	ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenActiveContextKey{}, active)
	return context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenInactiveContextKey{}, inactive)
}
