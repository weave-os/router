package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

const goldenCodexOverloadBody = `{"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"server_error"}}`

// withOverloadedCodexNoClaudeSeat enrolls a Codex pool with a free seat and a
// Claude pool whose only seat is spent, so no included pool can rescue a Codex
// capacity failure.
func withOverloadedCodexNoClaudeSeat(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return withGoldenClock(svc, h).WithManagedSubscriptions(goldenPlanIneligiblePools{goldenProviderPools{
		subscriptions.ProviderCodex:  newGoldenSubscriptionPool(h.clock.Now, "codex-seat"),
		subscriptions.ProviderClaude: newGoldenSubscriptionPool(h.clock.Now, "claude-seat"),
	}})
}

func goldenOverloadedCodexCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel, goldenAnthropicModel}
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &goldenClaudeSeatReset},
	})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

var goldenCodexSessionHeaders = map[string]string{"User-Agent": goldenCodexUserAgent, "Session-Id": "00000000-0000-4000-8000-00000000c0de"}

func goldenOverloadedCodexToolTurn(advance time.Duration) goldenRequest {
	return goldenRequest{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeaders, decorateContext: goldenOverloadedCodexCtx, advance: advance}
}

// goldenSubscriptionSessionHoldScenarios pin an agent session whose Codex pool
// fails on capacity while no Claude seat can rescue it: every later tool turn
// must stay on the model the failing turn served instead of flipping between
// GPT and Claude.
func goldenSubscriptionSessionHoldScenarios() []goldenScenario {
	return []goldenScenario{{
		name: ingressResponses + "_stream_codex_overload_without_included_rescue_keeps_model_across_tool_turns", ingress: ingressResponses,
		decisions:      []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel), decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		noCandidateErr: fmt.Errorf("hmm_embedding: arm selection: %w: %w", policy.ErrNoEligibleArm, hmm.ErrHMMUnavailable),
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenResponsesOK("served on codex seat"),
				goldenUpstreamError(http.StatusBadGateway, goldenCodexOverloadBody),
				goldenResponsesOK("served after overload"),
			},
			providers.ProviderAnthropic: {anthropicOK(true, "flipped to claude")},
		},
		requests: []goldenRequest{
			goldenOverloadedCodexToolTurn(0),
			goldenOverloadedCodexToolTurn(5 * time.Second),
			goldenOverloadedCodexToolTurn(time.Minute),
			goldenOverloadedCodexToolTurn(time.Minute),
		},
		configure: withOverloadedCodexNoClaudeSeat,
	}, {
		name: ingressResponses + "_stream_codex_overload_subscription_only_retries_codex_next_tool_turn", ingress: ingressResponses,
		decisions:      []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel), decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		noCandidateErr: fmt.Errorf("hmm_embedding: arm selection: %w: %w", policy.ErrNoEligibleArm, hmm.ErrHMMUnavailable),
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenResponsesOK("served on codex seat"),
				goldenUpstreamError(http.StatusBadGateway, goldenCodexOverloadBody),
				goldenResponsesOK("codex recovered"),
			},
			providers.ProviderAnthropic: {anthropicOK(true, "flipped to claude")},
		},
		requests: []goldenRequest{
			goldenSubscriptionOnlyCodexToolTurn(0),
			goldenSubscriptionOnlyCodexToolTurn(5 * time.Second),
			goldenSubscriptionOnlyCodexToolTurn(time.Minute),
		},
		configure: withOverloadedCodexNoClaudeSeat,
	}}
}

func goldenSubscriptionOnlyCodexToolTurn(advance time.Duration) goldenRequest {
	req := goldenOverloadedCodexToolTurn(advance)
	req.decorateContext = chainCtx(goldenSubscriptionOnlyCtx, goldenOverloadedCodexCtx)
	return req
}

// TestCodexOverloadWithoutPaidFallbackRetriesCodexNextTurn pins that a turn
// which fails with no included rescue and no paid fallback leaves no hold: the
// next tool turn retries the recovered Codex seat instead of the refusing one.
func TestCodexOverloadWithoutPaidFallbackRetriesCodexNextTurn(t *testing.T) {
	t.Parallel()
	sc := goldenSubscriptionSessionHoldScenarios()[1]
	h := newGoldenHarness(t, sc)
	h.serve(t, sc.ingress, sc.requests[0])
	failed := h.serve(t, sc.ingress, sc.requests[1])
	require.NotEmpty(t, failed.Attempts)
	require.NotEqual(t, "ok", failed.Attempts[len(failed.Attempts)-1].Outcome, "response: %+v", failed.Response)
	served := h.serve(t, sc.ingress, sc.requests[2])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	require.NotEmpty(t, served.Attempts)
	last := served.Attempts[len(served.Attempts)-1]
	require.Equal(t, providers.ProviderOpenAI, last.Provider, "attempts: %+v", served.Attempts)
	require.Equal(t, "ok", last.Outcome)
}

// TestCodexOverloadWithoutIncludedRescueKeepsModelAcrossToolTurns states the
// invariant the scenario pins: a session hold off an overloaded pool may only
// move the session onto another included pool, never onto a different paid
// model than the failing turn already used.
func TestCodexOverloadWithoutIncludedRescueKeepsModelAcrossToolTurns(t *testing.T) {
	t.Parallel()
	sc := goldenSubscriptionSessionHoldScenarios()[0]
	h := newGoldenHarness(t, sc)
	for i, req := range sc.requests {
		served := h.serve(t, sc.ingress, req)
		require.Equal(t, http.StatusOK, served.Response.Status, "turn %d response: %+v", i, served.Response.Body)
		require.NotEmpty(t, served.Attempts, "turn %d", i)
		last := served.Attempts[len(served.Attempts)-1]
		require.Equal(t, "ok", last.Outcome, "turn %d", i)
		require.Equal(t, providers.ProviderOpenAI, last.Provider, "turn %d flipped off the session's model: %+v", i, served.Attempts)
	}
}
