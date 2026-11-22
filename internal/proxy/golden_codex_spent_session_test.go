package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

func withOverloadedCodexSeatsAndClaudeSeat(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return svc.WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
		WithManagedSubscriptions(goldenProviderPools{
			subscriptions.ProviderCodex:  newGoldenSubscriptionPool(h.clock.Now, "codex-seat-a", "codex-seat-b"),
			subscriptions.ProviderClaude: newGoldenSubscriptionPool(h.clock.Now, "claude-seat"),
		})
}

func goldenCodexPlanSpentOverloadedSeatsCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel, goldenAnthropicModel}
	ctx = goldenCodexCtx(ctx)
	ctx = requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{ClientApp: requestcontext.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	ctx = credentials.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat-a", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "codex-seat-b", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	return credentials.WithManagedSubscriptionUsage(ctx)
}

// goldenCodexSpentSessionScenario pins an agent session whose own Codex plan is
// spent while every Codex seat stays rate-limited: the first turn falls back to
// the Claude seat, and the router keeps scoring the Codex model on later turns.
func goldenCodexSpentSessionScenarios() []goldenScenario {
	turn := func(suffix string, decorate func(context.Context) context.Context, advance time.Duration) goldenRequest {
		return goldenRequest{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor(suffix), decorateContext: decorate, advance: advance}
	}
	decisions := []router.Decision{
		decision(providers.ProviderOpenAI, goldenCodexModel),
		decision(providers.ProviderAnthropic, goldenAnthropicModel),
	}
	claude := []goldenStep{
		anthropicOK(true, "served on claude seat"),
		anthropicOK(true, "stayed on claude seat"),
		anthropicOK(true, "still on claude seat"),
	}
	return []goldenScenario{{
		name: ingressResponses + "_stream_spent_codex_plan_overloaded_seats_keeps_session_on_claude_seat", ingress: ingressResponses,
		decisions: decisions,
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenUpstreamError(http.StatusServiceUnavailable, goldenCodexOverloadBody),
				goldenUpstreamError(http.StatusServiceUnavailable, goldenCodexOverloadBody),
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenUpstreamError(http.StatusServiceUnavailable, goldenCodexOverloadBody),
				goldenUpstreamError(http.StatusServiceUnavailable, goldenCodexOverloadBody),
			},
			providers.ProviderAnthropic: claude,
		},
		requests: []goldenRequest{
			turn("c0f2", goldenCodexPlanSpentOverloadedSeatsCtx, 0),
			turn("c0f2", goldenCodexPlanSpentOverloadedSeatsCtx, time.Second),
			turn("c0f2", goldenCodexPlanSpentOverloadedSeatsCtx, 30*time.Second),
		},
		configure: withOverloadedCodexSeatsAndClaudeSeat,
	}, {
		name: ingressResponses + "_stream_spent_codex_plan_throttled_seats_keeps_session_on_claude_seat", ingress: ingressResponses,
		decisions: decisions,
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
			},
			providers.ProviderAnthropic: claude,
		},
		requests: []goldenRequest{
			turn("c0f1", goldenCodexPlanSpentClaudeSeatAvailableCtx, 0),
			turn("c0f1", goldenCodexPlanSpentClaudeSeatAvailableCtx, time.Second),
			turn("c0f1", goldenCodexPlanSpentClaudeSeatAvailableCtx, 30*time.Second),
		},
		configure: withLongThrottledCodexSeatsAndClaudeSeat,
	}}
}

// TestSpentCodexPlanKeepsSessionOffCodexModels states the invariant: once a
// turn establishes that the Codex lane cannot serve, later turns of the same
// session neither attempt nor switch back to a Codex model; they stay on the
// included pool that can serve.
func TestSpentCodexPlanKeepsSessionOffCodexModels(t *testing.T) {
	t.Parallel()
	for _, sc := range goldenCodexSpentSessionScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			assertSessionStaysOffSpentCodex(t, sc)
		})
	}
}

func assertSessionStaysOffSpentCodex(t *testing.T, sc goldenScenario) {
	t.Helper()
	h := newGoldenHarness(t, sc)
	first := h.serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, first.Response.Status, "response: %+v", first.Response.Body)
	require.Equal(t, providers.ProviderAnthropic, first.Attempts[len(first.Attempts)-1].Provider, "attempts: %+v", first.Attempts)
	for i, req := range sc.requests[1:] {
		served := h.serve(t, sc.ingress, req)
		require.Equal(t, http.StatusOK, served.Response.Status, "turn %d response: %+v", i+1, served.Response.Body)
		for _, attempt := range served.Attempts {
			require.NotEqual(t, providers.ProviderOpenAI, attempt.Provider, "turn %d re-attempted the spent Codex lane: %+v", i+1, served.Attempts)
		}
		last := served.Attempts[len(served.Attempts)-1]
		require.Equal(t, providers.ProviderAnthropic, last.Provider, "turn %d attempts: %+v", i+1, served.Attempts)
		require.Equal(t, "ok", last.Outcome, "turn %d", i+1)
	}
}

// goldenCodexNativeOnlyImageTurn carries a standalone tool image only the
// Responses wire family can carry, so the Claude seat cannot serve this turn.
const goldenCodexNativeOnlyImageTurn = goldenCodexToolTurnPrefix + `,` +
	`{"type":"function_call_output","call_id":"","output":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}]}`

func withOverloadedCodexAndFreeClaudeSeat(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return svc.WithManagedSubscriptions(goldenProviderPools{
		subscriptions.ProviderCodex:  newGoldenSubscriptionPool(h.clock.Now, "codex-seat"),
		subscriptions.ProviderClaude: newGoldenSubscriptionPool(h.clock.Now, "claude-seat"),
	})
}

func goldenOverloadedCodexFreeClaudeSeatCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel, goldenAnthropicModel}
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	ctx = credentials.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	return credentials.WithManagedSubscriptionUsage(ctx)
}

// TestCodexHoldOutlivesTurnOnlyCodexCouldCarry states the invariant: a turn
// whose history restricts it to the Responses wire family cannot use the free
// Claude seat, so a Codex capacity failure on it falls back to paid. That turn
// must not release the session's hold off Codex, so the next portable turn
// serves on the Claude seat instead of re-attempting the failing Codex lane.
func TestCodexHoldOutlivesTurnOnlyCodexCouldCarry(t *testing.T) {
	t.Parallel()
	turn := func(body string, advance time.Duration) goldenRequest {
		return goldenRequest{body: body, headers: goldenCodexSessionHeadersFor("c0f3"), decorateContext: goldenOverloadedCodexFreeClaudeSeatCtx, advance: advance}
	}
	sc := goldenScenario{
		name: ingressResponses + "_stream_codex_hold_outlives_native_only_turn", ingress: ingressResponses,
		decisions: []router.Decision{
			decision(providers.ProviderOpenAI, goldenCodexModel),
			decision(providers.ProviderAnthropic, goldenAnthropicModel),
		},
		noCandidateErr: fmt.Errorf("hmm_embedding: arm selection: %w: %w", policy.ErrNoEligibleArm, hmm.ErrHMMUnavailable),
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusBadGateway, goldenCodexOverloadBody),
				goldenResponsesOK("served on paid openai"),
				goldenUpstreamError(http.StatusBadGateway, goldenCodexOverloadBody),
				goldenResponsesOK("served on openai"),
				goldenResponsesOK("served on openai again"),
			},
			providers.ProviderAnthropic: {anthropicOK(true, "served on claude seat")},
		},
		requests: []goldenRequest{
			turn(goldenCodexNativeOnlyImageTurn, 0),
			turn(goldenCodexPortableToolTurn, time.Minute),
			turn(goldenCodexNativeOnlyImageTurn, 5*time.Second),
		},
		configure: withOverloadedCodexAndFreeClaudeSeat,
	}
	h := newGoldenHarness(t, sc)
	first := h.serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, first.Response.Status, "response: %+v", first.Response.Body)
	require.Equal(t, providers.ProviderOpenAI, first.Attempts[len(first.Attempts)-1].Provider, "attempts: %+v", first.Attempts)
	served := h.serve(t, sc.ingress, sc.requests[1])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	for _, attempt := range served.Attempts {
		require.NotEqual(t, providers.ProviderOpenAI, attempt.Provider, "re-attempted the failing Codex lane: %+v", served.Attempts)
	}
	last := served.Attempts[len(served.Attempts)-1]
	require.Equal(t, providers.ProviderAnthropic, last.Provider, "attempts: %+v", served.Attempts)
	require.Equal(t, "ok", last.Outcome)
	nativeOnlyServed := h.serve(t, sc.ingress, sc.requests[2])
	require.Equal(t, http.StatusOK, nativeOnlyServed.Response.Status, "native-only turn under the hold: %+v", nativeOnlyServed.Response.Body)
	require.Equal(t, "ok", nativeOnlyServed.Attempts[len(nativeOnlyServed.Attempts)-1].Outcome, "attempts: %+v", nativeOnlyServed.Attempts)
}
