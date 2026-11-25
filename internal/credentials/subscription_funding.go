package credentials

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions"
)

type ClientAppRefusalFunding struct {
	paidFallbackCounts bool
	poolRefused        bool
	clientApp          string
}

func (b *Brokerage) AuthorizeSubscriptionAPI(ctx context.Context, pool subscriptions.Provider, target inference.Target, codexToken, claudeToken string, refusal ClientAppRefusalFunding) error {
	if ManagedSubscriptionEnrollmentUnavailable(ctx) {
		return SubscriptionPoolUnavailableBecause(ctx, "subscription enrollment could not be verified")
	}
	now := b.Clock.Now()
	if IncludedLaneCannotServe(ctx, pool, target, now) {
		return nil
	}
	candidates, snapshotPresent := ctx.Value(ManagedSubscriptionCandidatesContextKey{}).([]ManagedSubscriptionCandidate)
	throttledBlocker := false
	for _, candidate := range candidates {
		if candidate.Provider != auth.SubscriptionProvider(pool) || !candidate.Enabled || candidate.State == auth.SubscriptionAccountStateDisabled || candidate.State == auth.SubscriptionAccountStateReconnectRequired {
			continue
		}
		if b.ManagedAccountQuotaExhausted(ctx, candidate, now) || ManagedAccountCredentialCooling(candidate, now) {
			continue
		}
		if b.SubscriptionModels.ManagedDenied("account:"+candidate.AccountID, candidate.AccountID, target.Provider, target.CatalogID, now) {
			continue
		}
		if refusal.paidFallbackCounts && pool == subscriptions.ProviderClaude && (refusal.poolRefused || b.SubscriptionModels.ManagedClientAppDenied(candidate.AccountID, providers.ProviderAnthropic, refusal.clientApp, now)) {
			continue
		}
		if ManagedAccountThrottled(ctx, candidate, now) {
			throttledBlocker = true
			continue
		}
		return SubscriptionPoolUnavailableBecause(ctx, "included quota is available or unknown")
	}
	token := claudeToken
	if pool == subscriptions.ProviderCodex {
		token = codexToken
	}
	callerQuotaUnproven := token != "" && !b.CallerQuotaSpent(ctx, pool, token, now)
	if throttledBlocker {
		if err := b.CallerPlanRefusal(ctx, pool, callerQuotaUnproven, now); err != nil {
			return err
		}
		return ThrottledPoolRefusal(ctx, pool, token != "", now)
	}
	if err := MissingQuotaSnapshotRefusal(ctx, pool, snapshotPresent); err != nil {
		return err
	}
	return b.CallerPlanRefusal(ctx, pool, callerQuotaUnproven, now)
}

func (b *Brokerage) CallerPlanRefusal(ctx context.Context, pool subscriptions.Provider, callerQuotaUnproven bool, now time.Time) error {
	if !callerQuotaUnproven {
		return nil
	}
	if CallerPlanThrottled(ctx, pool, now) {
		if !billing.WeaveCreditsDeniedFromContext(ctx) {
			return nil
		}
		return SubscriptionPoolRefusal(ctx, subscriptionlane.CallerPlanThrottled)
	}
	if b.triedSubscriptionFallsBackToCredits(ctx) {
		return nil
	}
	return SubscriptionPoolUnavailableBecause(ctx, "caller subscription quota is available or unknown")
}

func (b *Brokerage) triedSubscriptionFallsBackToCredits(ctx context.Context) bool {
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if state == nil || !state.SubscriptionAttempted || state.CallerCredentialRejected || billing.WeaveCreditsDeniedFromContext(ctx) {
		return false
	}
	return !b.CallerPassthrough(ctx)
}

func (b *Brokerage) SubscriptionPoolUnavailable(ctx context.Context, provider, model string) bool {
	pool, compatible := ManagedSubscriptionProviderFromUpstream(provider, model)
	return compatible && SubscriptionProviderUnavailable(ctx, pool, b.Clock.Now())
}

func (b *Brokerage) LinkedSeatCanCover(ctx context.Context, provider, model string) bool {
	if b.ManagedSubscriptions == nil || !ManagedSubscriptionCanServe(ctx, provider, model) || ManagedSubscriptionEnrollmentUnavailable(ctx) {
		return false
	}
	pool, _ := ManagedSubscriptionProviderFromUpstream(provider, model)
	now := b.Clock.Now()
	return ManagedSeatCanCover(ctx, pool, now, func(candidate ManagedSubscriptionCandidate) bool {
		return b.ManagedAccountQuotaExhausted(ctx, candidate, now)
	})
}

func ManagedSeatCanCover(ctx context.Context, pool subscriptions.Provider, now time.Time, quotaExhausted func(ManagedSubscriptionCandidate) bool) bool {
	if SubscriptionProviderUnavailable(ctx, pool, now) {
		return false
	}
	candidates, snapshotPresent := ctx.Value(ManagedSubscriptionCandidatesContextKey{}).([]ManagedSubscriptionCandidate)
	if !snapshotPresent {
		return true
	}
	for _, candidate := range candidates {
		if candidate.Provider != auth.SubscriptionProvider(pool) || !candidate.Enabled || candidate.State == auth.SubscriptionAccountStateDisabled || candidate.State == auth.SubscriptionAccountStateReconnectRequired {
			continue
		}
		if candidate.State == auth.SubscriptionAccountStateCooldown && candidate.ResetAt.After(now) {
			continue
		}
		if !quotaExhausted(candidate) {
			return true
		}
	}
	return false
}

func (b *Brokerage) ManagedAccountQuotaExhausted(ctx context.Context, candidate ManagedSubscriptionCandidate, now time.Time) bool {
	if RecordedQuotaExhausted(ctx, candidate, now) {
		return true
	}
	if candidate.Provider == auth.SubscriptionProviderClaude && ClaudeEnterpriseOnly(ctx) {
		return false
	}
	return IncludedQuotaObservedSpent(b.UsageObserver, candidate, now)
}

func IncludedQuotaObservedSpent(observer *usage.Observer, candidate ManagedSubscriptionCandidate, now time.Time) bool {
	if observer == nil {
		return false
	}
	snapshot, observed := observer.Snapshot(observer.Key([]byte("subscription-account:" + candidate.AccountID)))
	if observed && codexQuotaReactivatedAfterObservation(candidate, snapshot) {
		return false
	}
	return observed && IncludedQuotaSpent(snapshot, now)
}

func RecordedQuotaExhausted(ctx context.Context, candidate ManagedSubscriptionCandidate, now time.Time) bool {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		if state.QuotaExhaustedAccounts[candidate.AccountID].After(now) {
			return true
		}
	}
	if candidate.Provider == auth.SubscriptionProviderClaude && ClaudeEnterpriseOnly(ctx) {
		return false
	}
	return candidate.State == auth.SubscriptionAccountStateExhausted && candidate.ResetAt.After(now)
}

func ManagedSubscriptionEnrollmentUnavailable(ctx context.Context) bool {
	unavailable, _ := ctx.Value(ManagedSubscriptionEnrollmentUnavailableContextKey{}).(bool)
	return unavailable
}

func codexQuotaReactivatedAfterObservation(candidate ManagedSubscriptionCandidate, snapshot usage.Snapshot) bool {
	return candidate.Provider == auth.SubscriptionProviderCodex && candidate.State.Routable() &&
		snapshot.ObservedAt.Before(candidate.QuotaReactivatedAt)
}
