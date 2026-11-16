package proxy

import "context"

func WithSubscriptionStateModels(ctx context.Context, active, inactive []string) context.Context {
	ctx = context.WithValue(ctx, InstallationSubscriptionStateModelsEnabledContextKey{}, true)
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, active)
	return context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, inactive)
}
