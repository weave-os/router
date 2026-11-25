package subscriptions

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

type QuotaProbe struct {
	Available bool
	ResetAt   time.Time
}

type QuotaProber interface {
	ProbeQuota(ctx context.Context, provider Provider, accessToken, providerAccountID string) (QuotaProbe, error)
}

type QuotaReactivationStore interface {
	ReactivateSubscriptionAccountQuota(ctx context.Context, owner auth.SubscriptionOwner, accountID string, probedCooldown time.Time) (bool, error)
}

func (r *Runtime) WithQuotaProber(prober QuotaProber) *Runtime {
	r.quotaProber = prober
	return r
}

func (r *Runtime) reactivateRestoredQuota(ctx context.Context, owner auth.SubscriptionOwner, account Account, credentials auth.SubscriptionCredentials) bool {
	store, ok := r.store.(QuotaReactivationStore)
	if !ok || r.quotaProber == nil || account.Provider != ProviderCodex || len(credentials.AccessToken) == 0 ||
		credentials.State != auth.SubscriptionAccountStateExhausted || credentials.CooldownUntil == nil || !credentials.CooldownUntil.After(r.clock()) {
		return false
	}
	log := observability.FromContext(ctx).With("account_id", account.ID, "provider", account.Provider, "stored_reset_at", *credentials.CooldownUntil)
	probe, err := r.quotaProber.ProbeQuota(ctx, account.Provider, string(credentials.AccessToken), account.AccountID)
	if err != nil {
		log.Warn("Subscription quota re-check failed", "err", err)
		return false
	}
	if !probe.Available {
		log.Info("Subscription quota still exhausted after refresh", "reset_at", probe.ResetAt)
		return false
	}
	reactivated, err := store.ReactivateSubscriptionAccountQuota(ctx, owner, account.ID, *credentials.CooldownUntil)
	if err != nil {
		log.Warn("Subscription quota reactivation failed", "err", err)
		return false
	}
	log.Info("Subscription quota re-check completed", "reactivated", reactivated)
	return reactivated
}
