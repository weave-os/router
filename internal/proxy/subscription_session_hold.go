package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

const subscriptionSessionHold = 5 * time.Minute

func sessionHoldOwner(ctx context.Context) string {
	session := requestcontext.ClientIdentityFrom(ctx).SessionID
	if session == "" {
		return ""
	}
	installation, _ := ctx.Value(requestcontext.InstallationIDContextKey{}).(string)
	return installation + "\x00" + session
}

func (s *Service) holdSessionOffSubscriptionPool(ctx context.Context, provider, model string) {
	owner := sessionHoldOwner(ctx)
	pool, compatible := credentials.ManagedSubscriptionProviderFromUpstream(provider, model)
	if owner == "" || !compatible || !s.subscriptionPoolUnavailable(ctx, provider, model) {
		return
	}
	s.credentials.SessionHolds.Hold(owner, pool, s.clock.Now().Add(subscriptionSessionHold))
	observability.FromContext(ctx).Info("Session held off subscription pool after capacity failure", "pool", pool, "hold", subscriptionSessionHold)
}

func withUnservedHoldsReleased(ctx context.Context, holds *credentials.SubscriptionSessionHolds, enabledProviders map[string]struct{}) context.Context {
	owner := sessionHoldOwner(ctx)
	state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	if owner == "" || state == nil {
		return ctx
	}
	for pool := range state.UnavailableProviders {
		if !otherSubscriptionPoolEnabled(pool, enabledProviders) {
			observability.FromContext(ctx).Info("Session hold kept; the turn could not use another included subscription pool", "pool", pool)
			continue
		}
		if holds.Release(owner, pool) {
			observability.FromContext(ctx).Info("Session hold released; no included subscription pool served the turn", "pool", pool)
		}
	}
	return ctx
}

func (s *Service) withoutHeldSubscriptionModels(ctx context.Context, models []string) []string {
	owner := sessionHoldOwner(ctx)
	if owner == "" {
		return models
	}
	now := s.clock.Now()
	held := func(pool subscriptions.Provider) bool {
		return s.credentials.SessionHolds.Held(owner, pool, now)
	}
	kept := make([]string, 0, len(models))
	for _, model := range models {
		if !modelOnHeldPool(model, held) {
			kept = append(kept, model)
		}
	}
	return kept
}

func modelOnHeldPool(model string, held func(subscriptions.Provider) bool) bool {
	entry, ok := catalog.ByID(model)
	if !ok {
		return false
	}
	for _, binding := range entry.Providers {
		if pool, compatible := credentials.ManagedSubscriptionProviderFromUpstream(binding.Provider, model); compatible && held(pool) {
			return true
		}
	}
	return false
}

func otherSubscriptionPoolEnabled(pool subscriptions.Provider, enabledProviders map[string]struct{}) bool {
	if enabledProviders == nil {
		return true
	}
	for provider := range enabledProviders {
		if other, ok := subscriptions.PoolForUpstream(provider); ok && other != pool {
			return true
		}
	}
	return false
}
