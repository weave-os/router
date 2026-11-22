package proxy_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// goldenCodexOpaqueHistoryTurn continues the agent turn with history only the
// Responses wire family can carry verbatim: an encrypted-only agent message and
// a tool output whose call was trimmed away.
const goldenCodexOpaqueHistoryTurn = goldenCodexToolTurnPrefix + `,` +
	`{"type":"agent_message","author":"child","recipient":"parent","content":[{"type":"encrypted_content","encrypted_content":"opaque"}]},` +
	`{"type":"function_call_output","call_id":"","output":"tests passed"}]}`

func withLongThrottledCodexSeatsAndClaudeSeat(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	codex := newGoldenSubscriptionPool(h.clock.Now, "codex-seat-a", "codex-seat-b")
	codex.cooling["codex-seat-a"], codex.cooling["codex-seat-b"] = goldenCodexSeatLongCooldown, goldenCodexSeatLongCooldown
	return svc.WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
		WithManagedSubscriptions(goldenProviderPools{
			subscriptions.ProviderCodex:  codex,
			subscriptions.ProviderClaude: newGoldenSubscriptionPool(h.clock.Now, "claude-seat"),
		})
}

func goldenCodexPlanSpentClaudeSeatAvailableCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel, goldenAnthropicModel}
	ctx = goldenCodexCtx(ctx)
	ctx = requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{ClientApp: requestcontext.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	cooldown := goldenCodexSeatLongCooldown
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat-a", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
		{ID: "codex-seat-b", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
		{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	return credentials.WithManagedSubscriptionUsage(ctx)
}

// TestSpentCodexPlanRescuesOpaqueHistoryOnClaudeSeat states the invariant: once
// the caller's own Codex plan is spent and no Codex seat can serve, a turn whose
// history only the Responses wire family carries verbatim is converted lossily
// and served on the Claude seat instead of staying locked to OpenAI.
func TestSpentCodexPlanRescuesOpaqueHistoryOnClaudeSeat(t *testing.T) {
	t.Parallel()
	sc := goldenScenario{
		name: ingressResponses + "_stream_spent_codex_plan_rescues_opaque_history_on_claude_seat", ingress: ingressResponses,
		decisions: []router.Decision{
			decision(providers.ProviderOpenAI, goldenCodexModel),
			decision(providers.ProviderAnthropic, goldenAnthropicModel),
		},
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenResponsesOK("served on openai"),
			},
			providers.ProviderAnthropic: {anthropicOK(true, "served on claude seat"), anthropicOK(true, "rescued on claude seat")},
		},
		requests: []goldenRequest{
			{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor("c0e1"), decorateContext: goldenCodexPlanSpentClaudeSeatAvailableCtx},
			{body: goldenCodexOpaqueHistoryTurn, headers: goldenCodexSessionHeadersFor("c0e1"), decorateContext: goldenCodexPlanSpentClaudeSeatAvailableCtx, advance: time.Second},
		},
		configure: withLongThrottledCodexSeatsAndClaudeSeat,
	}
	h := newGoldenHarness(t, sc)
	h.serve(t, sc.ingress, sc.requests[0])
	served := h.serve(t, sc.ingress, sc.requests[1])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	last := served.Attempts[len(served.Attempts)-1]
	require.Equal(t, providers.ProviderAnthropic, last.Provider, "attempts: %+v", served.Attempts)
	require.Equal(t, "ok", last.Outcome)
}
