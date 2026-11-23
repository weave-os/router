package proxy

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

func codexRateLimitedErr() *providers.UpstreamErrorResponse {
	return &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"detail":"Rate limit exceeded"}`)}
}

// throttleTestContext denies Weave credits so a throttle stays a refusal; with
// credits allowed a long throttle funds the turn instead.
func throttleTestContext(accounts ...*auth.SubscriptionAccount) context.Context {
	ctx := credentials.WithManagedSubscriptionUsage(billing.WithWeaveCreditsDenied(context.Background()))
	return credentials.WithManagedSubscriptionCandidates(ctx, accounts)
}

func activeCodexAccount(id string) *auth.SubscriptionAccount {
	return &auth.SubscriptionAccount{ID: id, Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}
}

func requireThrottledRefusal(t *testing.T, err error) {
	t.Helper()
	require.ErrorIs(t, err, credentials.ErrSubscriptionPoolThrottled)
	require.ErrorIs(t, err, credentials.ErrSubscriptionPoolUnavailable, "callers branching on pool-unavailable must keep treating it as a refusal")
	class, classified := ClassifyDispatchError(err)
	require.True(t, classified)
	require.Equal(t, http.StatusTooManyRequests, class.Status)
	require.True(t, class.RetryAfter)
}

// A burst rate limit on every seat still blocks paid fallback, but the client
// sees a retryable 429 instead of a 503 outage.
func TestAuthorizeSubscriptionAPI_RateLimitedPoolRefusesAs429(t *testing.T) {
	now := time.Now()
	svc := wireCredentialPorts(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), clock: func() time.Time { return now }}).WithManagedSubscriptions(&scriptedSubscriptionLeaser{})
	ctx := throttleTestContext(activeCodexAccount("seat-1"), activeCodexAccount("seat-2"))
	authorize := func() error {
		return svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{})
	}

	require.True(t, svc.recordManagedSubscriptionFailure(ctx, providers.ProviderOpenAI, codexCoveredModel, subscriptions.Lease{AccountID: "seat-1"}, codexRateLimitedErr()))
	err := authorize()
	require.ErrorIs(t, err, credentials.ErrSubscriptionPoolUnavailable)
	require.NotErrorIs(t, err, credentials.ErrSubscriptionPoolThrottled, "an untried seat with unknown quota is not a throttle")

	require.True(t, svc.recordManagedSubscriptionFailure(ctx, providers.ProviderOpenAI, codexCoveredModel, subscriptions.Lease{AccountID: "seat-2"}, codexRateLimitedErr()))
	requireThrottledRefusal(t, authorize())

	now = now.Add(time.Hour)
	err = authorize()
	require.ErrorIs(t, err, credentials.ErrSubscriptionPoolUnavailable)
	require.NotErrorIs(t, err, credentials.ErrSubscriptionPoolThrottled, "an expired cooldown no longer explains the refusal")
}

func TestAuthorizeSubscriptionAPI_PersistedCooldownCountsAsThrottle(t *testing.T) {
	now := time.Now()
	svc := wireCredentialPorts(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), clock: func() time.Time { return now }})
	cooldownUntil := now.Add(time.Minute)
	cooling := activeCodexAccount("seat-cooling")
	cooling.State = auth.SubscriptionAccountStateCooldown
	cooling.CooldownUntil = &cooldownUntil

	requireThrottledRefusal(t, svc.credentials.AuthorizeSubscriptionAPI(throttleTestContext(cooling), subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{}))

	cooling.CredentialRejections = 1
	require.NoError(t, svc.credentials.AuthorizeSubscriptionAPI(throttleTestContext(cooling), subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{}), "a 401/403 cooldown is a credential fault, not a rate limit, and the seat cannot serve")
}

// A seat or caller-plan throttle would fail the turn, so Weave credits fund
// it, with or without a caller login.
func TestAuthorizeSubscriptionAPI_LongThrottleFundsCredits(t *testing.T) {
	now := time.Now()
	svc := wireCredentialPorts(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), clock: func() time.Time { return now }})
	callerCreds := &requestcontext.Credentials{APIKey: []byte("caller-codex-token"), OAuth: true}
	ctx := credentials.WithManagedSubscriptionCandidates(credentials.WithManagedSubscriptionUsage(context.Background()), []*auth.SubscriptionAccount{activeCodexAccount("seat-1")})
	credentials.RecordManagedThrottle(ctx, "seat-1", now.Add(time.Hour))
	require.NoError(t, svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{}))
	svc.recordDirectSubscriptionQuota(ctx, providers.ProviderOpenAI, codexCoveredModel, callerCreds, codexRateLimitedErr())
	require.NoError(t, svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "caller-codex-token", "", credentials.ClientAppRefusalFunding{}))

	short := credentials.WithManagedSubscriptionCandidates(credentials.WithManagedSubscriptionUsage(context.Background()), []*auth.SubscriptionAccount{activeCodexAccount("seat-1")})
	credentials.RecordManagedThrottle(short, "seat-1", now.Add(10*time.Second))
	require.NoError(t, svc.credentials.AuthorizeSubscriptionAPI(short, subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{}), "outside the subscription-state lane no seat wait is coming, so a short throttle pays too")
	requireThrottledRefusal(t, svc.credentials.AuthorizeSubscriptionAPI(billing.WithWeaveCreditsDenied(short), subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{}))
}

func TestAuthorizeSubscriptionAPI_UnknownCallerQuotaKeeps503(t *testing.T) {
	now := time.Now()
	svc := wireCredentialPorts(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), clock: func() time.Time { return now }})
	ctx := throttleTestContext(activeCodexAccount("seat-1"))
	credentials.RecordManagedThrottle(ctx, "seat-1", now.Add(time.Minute))

	err := svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "caller-codex-token", "", credentials.ClientAppRefusalFunding{})
	require.ErrorIs(t, err, credentials.ErrSubscriptionPoolUnavailable, "paid fallback stays blocked")
	require.NotErrorIs(t, err, credentials.ErrSubscriptionPoolThrottled, "the caller's own plan has no rate-limit evidence")
}

// Proven exhaustion still authorizes paid fallback, and alongside a throttled
// seat the refusal is attributed to the throttle.
func TestAuthorizeSubscriptionAPI_ExhaustionStillAuthorizesPaidFallback(t *testing.T) {
	now := time.Now()
	svc := wireCredentialPorts(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), clock: func() time.Time { return now }})
	ctx := throttleTestContext(activeCodexAccount("seat-spent"), activeCodexAccount("seat-throttled"))
	credentials.RecordManagedQuotaProof(ctx, "seat-spent", now.Add(time.Hour))

	credentials.RecordManagedThrottle(ctx, "seat-throttled", now.Add(time.Minute))
	requireThrottledRefusal(t, svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{}))

	credentials.RecordManagedQuotaProof(ctx, "seat-throttled", now.Add(time.Hour))
	require.NoError(t, svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{}))
}

// Incident 2026-11-13: every Codex seat was exhausted, reconnect-required, or
// cooling down on a dead refresh token. The dead-credential seats cannot be
// leased, so they must not hold paid fallback hostage as "quota unknown".
func TestAuthorizeSubscriptionAPI_CredentialRejectedCooldownDoesNotBlockPaidFallback(t *testing.T) {
	now := time.Now()
	svc := wireCredentialPorts(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), clock: func() time.Time { return now }})
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
	require.NoError(t, svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{}))

	expired := now.Add(-time.Minute)
	deadToken.CooldownUntil = &expired
	err := svc.credentials.AuthorizeSubscriptionAPI(throttleTestContext(deadToken, spent, reconnect), subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{})
	require.ErrorIs(t, err, credentials.ErrSubscriptionPoolUnavailable, "once the credential cooldown lapses the seat may refresh and serve again")
}

// Incident 2026-11-13: the caller's own ChatGPT plan answered a generic 429
// while every shared seat was throttled. Paid fallback stays blocked, but the
// refusal must be a retryable 429 so Codex backs off instead of hot-retrying
// a 503.
func TestAuthorizeSubscriptionAPI_RateLimitedCallerPlanRefusesAs429(t *testing.T) {
	now := time.Now()
	svc := wireCredentialPorts(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), clock: func() time.Time { return now }})
	callerCreds := &requestcontext.Credentials{APIKey: []byte("caller-codex-token"), OAuth: true}
	authorize := func(ctx context.Context) error {
		return svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "caller-codex-token", "", credentials.ClientAppRefusalFunding{})
	}

	pooled := throttleTestContext(activeCodexAccount("seat-1"))
	credentials.RecordManagedThrottle(pooled, "seat-1", now.Add(time.Hour))
	svc.recordDirectSubscriptionQuota(pooled, providers.ProviderOpenAI, codexCoveredModel, callerCreds, codexRateLimitedErr())
	requireThrottledRefusal(t, authorize(pooled))

	unpooled := throttleTestContext()
	svc.recordDirectSubscriptionQuota(unpooled, providers.ProviderOpenAI, codexCoveredModel, callerCreds, codexRateLimitedErr())
	requireThrottledRefusal(t, authorize(unpooled))

	now = now.Add(2 * time.Minute)
	err := authorize(pooled)
	require.ErrorIs(t, err, credentials.ErrSubscriptionPoolUnavailable)
	require.NotErrorIs(t, err, credentials.ErrSubscriptionPoolThrottled, "an expired caller rate limit no longer explains the refusal")
}

func TestAuthorizeSubscriptionAPI_CallerQuotaExhaustionStillAuthorizesPaidFallback(t *testing.T) {
	now := time.Now()
	svc := wireCredentialPorts(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), clock: func() time.Time { return now }})
	ctx := throttleTestContext()
	quotaExhaustedErr := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"usage_limit_reached","message":"limit"}}`)}
	svc.recordDirectSubscriptionQuota(ctx, providers.ProviderOpenAI, codexCoveredModel, &requestcontext.Credentials{APIKey: []byte("caller-codex-token"), OAuth: true}, quotaExhaustedErr)
	require.NoError(t, svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "caller-codex-token", "", credentials.ClientAppRefusalFunding{}))
}

// A rate limit on the caller's own plan is the customer's capacity, so the
// refused turn must not count against router or provider reliability.
func TestCallerPlanRateLimitRefusalIsClientOwned(t *testing.T) {
	now := time.Now()
	svc := wireCredentialPorts(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), clock: func() time.Time { return now }})
	resetAt := now.Add(5 * 24 * time.Hour)
	spentSeat := func(id string) *auth.SubscriptionAccount {
		seat := activeCodexAccount(id)
		seat.State = auth.SubscriptionAccountStateExhausted
		seat.CooldownUntil = &resetAt
		return seat
	}
	ctx := throttleTestContext(spentSeat("seat-1"), spentSeat("seat-2"))
	svc.recordDirectSubscriptionQuota(ctx, providers.ProviderOpenAI, codexCoveredModel, &requestcontext.Credentials{APIKey: []byte("caller-codex-token"), OAuth: true}, codexRateLimitedErr())

	refusal := svc.credentials.AuthorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, inference.Target{}, "caller-codex-token", "", credentials.ClientAppRefusalFunding{})
	requireThrottledRefusal(t, refusal)
	turnErr := fmt.Errorf("%w: %w", dispatch.ErrAuthorizationDenied, refusal)
	require.Equal(t, turnrecord.FailureOwnerClient, turnFailureOwnerFor(ctx, turnErr, turnrecord.TurnErrorOther))

	pooled := throttleTestContext(activeCodexAccount("seat-1"))
	credentials.RecordManagedThrottle(pooled, "seat-1", now.Add(time.Minute))
	managedRefusal := svc.credentials.AuthorizeSubscriptionAPI(pooled, subscriptions.ProviderCodex, inference.Target{}, "", "", credentials.ClientAppRefusalFunding{})
	requireThrottledRefusal(t, managedRefusal)
	require.Equal(t, turnrecord.FailureOwnerProvider, turnFailureOwnerFor(pooled, fmt.Errorf("%w: %w", dispatch.ErrAuthorizationDenied, managedRefusal), turnrecord.TurnErrorOther), "a managed pool throttled by the provider is the provider's rate limit")
}
