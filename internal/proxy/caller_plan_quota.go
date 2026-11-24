package proxy

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

func (s *Service) recordDirectSubscriptionQuota(ctx context.Context, provider, model string, creds *requestcontext.Credentials, attemptErr error) {
	if state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); state != nil && (credentials.AnthropicOAuthCredentialRejected(attemptErr) || credentials.CodexOAuthCredentialRejected(attemptErr)) {
		state.CallerCredentialRejected = true
	}
	pool, compatible := credentials.ManagedSubscriptionProviderFromUpstream(provider, model)
	if !compatible {
		return
	}
	resetAt, exhausted := s.subscriptionQuotaExhaustion(pool, subscriptions.Lease{AccessToken: string(creds.APIKey)}, attemptErr)
	if !exhausted {
		if upstreamStatus(attemptErr) == http.StatusTooManyRequests {
			recordCallerPlanThrottle(ctx, pool, credentials.ManagedSubscriptionResetAt(attemptErr, s.clock.Now()))
		}
		return
	}
	if state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); state != nil {
		if state.QuotaExhaustedProviders == nil {
			state.QuotaExhaustedProviders = make(map[subscriptions.Provider]time.Time)
		}
		state.QuotaExhaustedProviders[pool] = resetAt
	}
}

func recordCallerPlanThrottle(ctx context.Context, pool subscriptions.Provider, until time.Time) {
	if state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); state != nil {
		if state.ThrottledProviders == nil {
			state.ThrottledProviders = make(map[subscriptions.Provider]time.Time)
		}
		state.ThrottledProviders[pool] = until
	}
}
