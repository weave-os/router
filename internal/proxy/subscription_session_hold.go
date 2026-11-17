package proxy

import (
	"context"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

const subscriptionSessionHold = 5 * time.Minute

type subscriptionSessionHolds struct {
	once    sync.Once
	entries *lru.Cache[string, time.Time]
}

func (h *subscriptionSessionHolds) cache() *lru.Cache[string, time.Time] {
	h.once.Do(func() { h.entries, _ = lru.New[string, time.Time](8192) })
	return h.entries
}

func sessionHoldOwner(ctx context.Context) string {
	session := ClientIdentityFrom(ctx).SessionID
	if session == "" {
		return ""
	}
	installation, _ := ctx.Value(InstallationIDContextKey{}).(string)
	return installation + "\x00" + session
}

func (s *Service) holdSessionOffSubscriptionPool(ctx context.Context, provider, model string) {
	owner := sessionHoldOwner(ctx)
	pool, compatible := managedSubscriptionProviderFromUpstream(provider, model)
	if owner == "" || !compatible || !s.subscriptionPoolUnavailable(ctx, provider, model) {
		return
	}
	s.sessionHolds.cache().Add(owner+"\x00"+string(pool), s.clockNow().Add(subscriptionSessionHold))
	observability.FromContext(ctx).Info("Session held off subscription pool after capacity failure", "pool", pool, "hold", subscriptionSessionHold)
}

func (h *subscriptionSessionHolds) withUnservedHoldsReleased(ctx context.Context) context.Context {
	owner := sessionHoldOwner(ctx)
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if owner == "" || state == nil {
		return ctx
	}
	for pool := range state.UnavailableProviders {
		if h.cache().Remove(owner + "\x00" + string(pool)) {
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
	now := s.clockNow()
	held := func(pool subscriptions.Provider) bool {
		until, ok := s.sessionHolds.cache().Get(owner + "\x00" + string(pool))
		return ok && now.Before(until)
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
		if pool, compatible := managedSubscriptionProviderFromUpstream(binding.Provider, model); compatible && held(pool) {
			return true
		}
	}
	return false
}
