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

	cooling.CredentialRejections = 1
	err := svc.authorizeSubscriptionAPI(throttleTestContext(cooling), subscriptions.ProviderCodex, "", "", clientAppRefusalFunding{})
	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable)
	require.NotErrorIs(t, err, ErrSubscriptionPoolThrottled, "a 401/403 cooldown is a credential fault, not a rate limit")
}

func TestAuthorizeSubscriptionAPI_UnknownCallerQuotaKeeps503(t *testing.T) {
	now := time.Now()
	svc := &Service{now: func() time.Time { return now }}
	ctx := throttleTestContext(activeCodexAccount("seat-1"))
	recordManagedThrottle(ctx, "seat-1", now.Add(time.Minute))

	err := svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "caller-codex-token", "", clientAppRefusalFunding{})
	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable, "paid fallback stays blocked")
	require.NotErrorIs(t, err, ErrSubscriptionPoolThrottled, "the caller's own plan has no rate-limit evidence")
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

// A non-quota 429 on the caller's own login is rate-limit evidence for that
// plan: paid fallback stays blocked, but the client sees a retryable 429
// rather than a 503 outage.
func TestAuthorizeSubscriptionAPI_RateLimitedCallerLoginRefusesAs429(t *testing.T) {
	now := time.Now()
	svc := &Service{now: func() time.Time { return now }}
	callerCreds := &Credentials{APIKey: []byte("caller-codex-token"), OAuth: true, Source: credSourceCodexSubscription}
	rateLimited := codexRateLimitedErr()
	rateLimited.Headers = http.Header{"Retry-After": []string{"30"}}

	for name, ctx := range map[string]context.Context{
		"throttled seat": throttleTestContext(activeCodexAccount("seat-1")),
		"no seat":        throttleTestContext(),
	} {
		t.Run(name, func(t *testing.T) {
			recordManagedThrottle(ctx, "seat-1", now.Add(time.Minute))
			authorize := func() error {
				return svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, string(callerCreds.APIKey), "", clientAppRefusalFunding{})
			}
			require.NotErrorIs(t, authorize(), ErrSubscriptionPoolThrottled, "an untried caller login has no rate-limit evidence")

			svc.recordDirectSubscriptionQuota(ctx, providers.ProviderOpenAI, codexCoveredModel, callerCreds, rateLimited)
			requireThrottledRefusal(t, authorize())

			now = now.Add(time.Hour)
			err := authorize()
			require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable)
			require.NotErrorIs(t, err, ErrSubscriptionPoolThrottled, "an expired caller throttle no longer explains the refusal")
		})
	}
}
