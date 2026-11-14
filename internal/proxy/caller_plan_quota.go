package proxy

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/subscriptions"
)

func (s *Service) recordDirectSubscriptionQuota(ctx context.Context, provider, model string, creds *Credentials, attemptErr error) {
	pool, compatible := managedSubscriptionProviderFromUpstream(provider, model)
	if !compatible {
		return
	}
	resetAt, exhausted := s.subscriptionQuotaExhaustion(pool, subscriptions.Lease{AccessToken: string(creds.APIKey)}, attemptErr)
	if !exhausted {
		if upstreamStatus(attemptErr) == http.StatusTooManyRequests {
			recordCallerPlanThrottle(ctx, pool, managedSubscriptionResetAt(attemptErr, s.clockNow()))
		}
		return
	}
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		if state.QuotaExhaustedProviders == nil {
			state.QuotaExhaustedProviders = make(map[subscriptions.Provider]time.Time)
		}
		state.QuotaExhaustedProviders[pool] = resetAt
	}
}

// recordCallerPlanThrottle marks, until its Retry-After, a 429 on the caller's
// own plan that did not prove quota exhaustion.
func recordCallerPlanThrottle(ctx context.Context, pool subscriptions.Provider, until time.Time) {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		if state.ThrottledProviders == nil {
			state.ThrottledProviders = make(map[subscriptions.Provider]time.Time)
		}
		state.ThrottledProviders[pool] = until
	}
}

// callerPlanThrottled reports a live rate limit on the caller's own plan. It
// keeps paid fallback blocked but makes the refusal a retryable 429.
func callerPlanThrottled(ctx context.Context, pool subscriptions.Provider, now time.Time) bool {
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return state != nil && state.ThrottledProviders[pool].After(now)
}
