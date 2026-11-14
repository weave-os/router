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
	require.NoError(t, svc.authorizeSubscriptionAPI(throttleTestContext(cooling), subscriptions.ProviderCodex, "", "", clientAppRefusalFunding{}), "a 401/403 cooldown is a credential fault, not a rate limit, and the seat cannot serve")
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

// Incident 2026-11-13: every Codex seat was exhausted, reconnect-required, or
// cooling down on a dead refresh token. The dead-credential seats cannot be
// leased, so they must not hold paid fallback hostage as "quota unknown".
func TestAuthorizeSubscriptionAPI_CredentialRejectedCooldownDoesNotBlockPaidFallback(t *testing.T) {
	now := time.Now()
	svc := &Service{now: func() time.Time { return now }}
	cooldownUntil := now.Add(2 * time.Hour)
	exhaustedUntil := now.Add(5 * 24 * time.Hour)

	deadToken := activeCodexAccount("seat-dead-refresh-token")
	deadToken.State = auth.SubscriptionAccountStateCooldown
	deadToken.CooldownUntil = &cooldownUntil
	deadToken.CredentialRejections = 4

	spent := activeCodexAccount("seat-exhausted")
	spent.State = auth.SubscriptionAccountStateExhausted
	spent.CooldownUntil = &exhaustedUntil

	reconnect := activeCodexAccount("seat-reconnect")
	reconnect.Enabled = false
	reconnect.State = auth.SubscriptionAccountStateReconnectRequired

	ctx := throttleTestContext(deadToken, spent, reconnect)
	require.NoError(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "", "", clientAppRefusalFunding{}))

	expired := now.Add(-time.Minute)
	deadToken.CooldownUntil = &expired
	err := svc.authorizeSubscriptionAPI(throttleTestContext(deadToken, spent, reconnect), subscriptions.ProviderCodex, "", "", clientAppRefusalFunding{})
	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable, "once the credential cooldown lapses the seat may refresh and serve again")
}

// Incident 2026-11-13: the caller's own ChatGPT plan answered a generic 429
// while every shared seat was throttled. Paid fallback stays blocked, but the
// refusal must be a retryable 429 so Codex backs off instead of hot-retrying
// a 503.
func TestAuthorizeSubscriptionAPI_RateLimitedCallerPlanRefusesAs429(t *testing.T) {
	now := time.Now()
	svc := &Service{now: func() time.Time { return now }}
	callerCreds := &Credentials{APIKey: []byte("caller-codex-token"), OAuth: true}
	authorize := func(ctx context.Context) error {
		return svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "caller-codex-token", "", clientAppRefusalFunding{})
	}

	pooled := throttleTestContext(activeCodexAccount("seat-1"))
	recordManagedThrottle(pooled, "seat-1", now.Add(time.Hour))
	svc.recordDirectSubscriptionQuota(pooled, providers.ProviderOpenAI, codexCoveredModel, callerCreds, codexRateLimitedErr())
	requireThrottledRefusal(t, authorize(pooled))

	unpooled := throttleTestContext()
	svc.recordDirectSubscriptionQuota(unpooled, providers.ProviderOpenAI, codexCoveredModel, callerCreds, codexRateLimitedErr())
	requireThrottledRefusal(t, authorize(unpooled))

	now = now.Add(2 * time.Minute)
	err := authorize(pooled)
	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable)
	require.NotErrorIs(t, err, ErrSubscriptionPoolThrottled, "an expired caller rate limit no longer explains the refusal")
}

func TestAuthorizeSubscriptionAPI_CallerQuotaExhaustionStillAuthorizesPaidFallback(t *testing.T) {
	now := time.Now()
	svc := &Service{now: func() time.Time { return now }}
	ctx := throttleTestContext()
	quotaExhaustedErr := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"usage_limit_reached","message":"limit"}}`)}
	svc.recordDirectSubscriptionQuota(ctx, providers.ProviderOpenAI, codexCoveredModel, &Credentials{APIKey: []byte("caller-codex-token"), OAuth: true}, quotaExhaustedErr)
	require.NoError(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, "caller-codex-token", "", clientAppRefusalFunding{}))
}
