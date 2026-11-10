package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

func codexRateLimitedErr() *providers.UpstreamErrorResponse {
	return &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"detail":"Rate limit exceeded"}`)}
}

func throttleTestContext(accounts ...*auth.SubscriptionAccount) context.Context {
	ctx := WithManagedSubscriptionUsage(context.Background())
	return WithManagedSubscriptionCandidates(ctx, accounts)
}

func activeCodexAccount(id string) *auth.SubscriptionAccount {
	return &auth.SubscriptionAccount{ID: id, Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}
}

func requireThrottledRefusal(t *testing.T, err error) {
	t.Helper()
	require.ErrorIs(t, err, ErrSubscriptionPoolThrottled)
	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable, "callers branching on pool-unavailable must keep treating it as a refusal")
	class, classified := ClassifyDispatchError(err)
	require.True(t, classified)
	require.Equal(t, http.StatusTooManyRequests, class.Status)
	require.True(t, class.RetryAfter)
}

// A burst rate limit on every seat still blocks paid fallback, but the client
// sees a retryable 429 instead of a 503 outage.
func TestAuthorizeSubscriptionAPI_RateLimitedPoolRefusesAs429(t *testing.T) {
	now := time.Now()
	svc := (&Service{now: func() time.Time { return now }}).WithManagedSubscriptions(&scriptedSubscriptionLeaser{})
	ctx := throttleTestContext(activeCodexAccount("seat-1"), activeCodexAccount("seat-2"))
	authorize := func() error {
		return svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "", "", clientAppRefusalFunding{})
	}

	require.True(t, svc.recordManagedSubscriptionFailure(ctx, providers.ProviderOpenAI, codexCoveredModel, subscriptions.Lease{AccountID: "seat-1"}, codexRateLimitedErr()))
	err := authorize()
	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable)
	require.NotErrorIs(t, err, ErrSubscriptionPoolThrottled, "an untried seat with unknown quota is not a throttle")

	require.True(t, svc.recordManagedSubscriptionFailure(ctx, providers.ProviderOpenAI, codexCoveredModel, subscriptions.Lease{AccountID: "seat-2"}, codexRateLimitedErr()))
	requireThrottledRefusal(t, authorize())

	now = now.Add(time.Hour)
	err = authorize()
	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable)
	require.NotErrorIs(t, err, ErrSubscriptionPoolThrottled, "an expired cooldown no longer explains the refusal")
}

func TestAuthorizeSubscriptionAPI_PersistedCooldownCountsAsThrottle(t *testing.T) {
	now := time.Now()
	svc := &Service{now: func() time.Time { return now }}
	cooldownUntil := now.Add(time.Minute)
	cooling := activeCodexAccount("seat-cooling")
	cooling.State = auth.SubscriptionAccountStateCooldown
	cooling.CooldownUntil = &cooldownUntil

	requireThrottledRefusal(t, svc.authorizeSubscriptionAPI(throttleTestContext(cooling), subscriptions.ProviderCodex, "", "", clientAppRefusalFunding{}))
}

// Proven exhaustion still authorizes paid fallback, and alongside a throttled
// seat the refusal is attributed to the throttle.
func TestAuthorizeSubscriptionAPI_ExhaustionStillAuthorizesPaidFallback(t *testing.T) {
	now := time.Now()
	svc := &Service{now: func() time.Time { return now }}
	ctx := throttleTestContext(activeCodexAccount("seat-spent"), activeCodexAccount("seat-throttled"))
	recordManagedQuotaProof(ctx, "seat-spent", now.Add(time.Hour))

	recordManagedThrottle(ctx, "seat-throttled", now.Add(time.Minute))
	requireThrottledRefusal(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "", "", clientAppRefusalFunding{}))

	recordManagedQuotaProof(ctx, "seat-throttled", now.Add(time.Hour))
	require.NoError(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "", "", clientAppRefusalFunding{}))
}
