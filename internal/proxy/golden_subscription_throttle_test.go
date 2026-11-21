package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

// withThrottledCodexPool enrolls a Codex pool with two free seats and no
// Claude pool, so a rate-limited Codex turn has no included rescue.
func withThrottledCodexPool(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return svc.WithManagedSubscriptions(goldenProviderPools{
		subscriptions.ProviderCodex: newGoldenSubscriptionPool(h.clock.Now, "codex-seat-a", "codex-seat-b"),
	})
}

func goldenThrottledCodexCreditsDeniedCtx(ctx context.Context) context.Context {
	ctx = goldenCreditsDeniedCtx(ctx)
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat-a", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "codex-seat-b", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

// goldenSubscriptionThrottleScenarios pin a subscription-only installation
// whose every Codex seat is rate-limited: the paid baseline failover is refused
// because Weave credits are off, and the client must still learn the seats are
// rate-limited rather than that the turn can never be funded.
func goldenSubscriptionThrottleScenarios() []goldenScenario {
	return []goldenScenario{{
		name: ingressAnthropic + "_stream_codex_pool_throttled_credits_denied_stays_retryable", ingress: ingressAnthropic,
		decisions: []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel)},
		// What the HMM policy returns once every arm it may rank is excluded.
		noCandidateErr: fmt.Errorf("hmm_embedding: arm selection: %w", policy.ErrNoEligibleArm),
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, goldenRateLimitBody),
				goldenUpstreamError(http.StatusTooManyRequests, goldenRateLimitBody),
			},
			providers.ProviderAnthropic: {anthropicOK(true, "must not be served on Weave credits")},
		},
		requests:  []goldenRequest{{body: goldenAnthropicBody(true), decorateContext: goldenThrottledCodexCreditsDeniedCtx}},
		configure: withThrottledCodexPool,
	}}
}

// TestThrottledPoolWithCreditsDeniedIsRetryable states the invariant the
// scenario pins: rate-limited seats are a transient condition, so the refused
// paid failover must not turn it into a non-retryable credits refusal.
func TestThrottledPoolWithCreditsDeniedIsRetryable(t *testing.T) {
	t.Parallel()
	sc := goldenSubscriptionThrottleScenarios()[0]
	h := newGoldenHarness(t, sc)
	trace := h.serve(t, sc.ingress, sc.requests[0])
	for _, attempt := range trace.Attempts {
		require.NotEqual(t, providers.ProviderAnthropic, attempt.Provider, "Weave credits are off: no Anthropic attempt may be dispatched")
	}
	require.Empty(t, trace.Billing, "a refused turn must not be billed")
	require.NotNil(t, trace.DispatchError, "response: %+v", trace.Response)
	require.Equal(t, http.StatusTooManyRequests, trace.DispatchError.Status, "dispatch error: %+v", trace.DispatchError)
	require.True(t, trace.DispatchError.RetryAfter, "dispatch error: %+v", trace.DispatchError)
	require.False(t, trace.DispatchError.NoClientRetry, "dispatch error: %+v", trace.DispatchError)
}
