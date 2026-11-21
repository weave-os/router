package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

var goldenCodexUsageLimitReset = goldenEpoch.Add(89*time.Minute + 30*time.Second)

var goldenCodexModelUsageLimitBody = `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"pro","resets_at":` +
	strconv.FormatInt(goldenCodexUsageLimitReset.Unix(), 10) + `}}`

func withCodexAndClaudeSeats(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return svc.WithManagedSubscriptions(goldenProviderPools{
		subscriptions.ProviderCodex:  newGoldenSubscriptionPool(h.clock.Now, "codex-seat"),
		subscriptions.ProviderClaude: newGoldenSubscriptionPool(h.clock.Now, "claude-seat"),
	})
}

func goldenCodexAndClaudeSeatsCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel, goldenAnthropicModel}
	ctx = requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{ClientApp: requestcontext.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

// goldenSubscriptionUsageLimitScenarios pin a Codex tool turn whose picked
// model hit the seat's usage limit: the turn moves to the Claude seat, and the
// routing marker names the usage limit and its reset instead of the synthetic
// follow-up denial.
func goldenSubscriptionUsageLimitScenarios() []goldenScenario {
	return []goldenScenario{{
		name: ingressResponses + "_stream_codex_model_usage_limit_switch_marker_names_limit", ingress: ingressResponses,
		decisions: []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel), decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI:    {goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody)},
			providers.ProviderAnthropic: {anthropicOK(true, "served on claude seat")},
		},
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeaders, decorateContext: goldenCodexAndClaudeSeatsCtx}},
		configure: withCodexAndClaudeSeats,
	}}
}

// TestCodexUsageLimitSwitchMarkerNamesLimit states the invariant the scenario
// pins: the marker says the picked model hit the Codex usage limit, never that
// it rejected the request.
func TestCodexUsageLimitSwitchMarkerNamesLimit(t *testing.T) {
	t.Parallel()
	sc := goldenSubscriptionUsageLimitScenarios()[0]
	h := newGoldenHarness(t, sc)
	served := h.serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	raw, err := json.Marshal(served.Response)
	require.NoError(t, err)
	body := string(raw)
	require.Contains(t, body, "switched after "+goldenCodexModel+" hit the Codex usage limit (resets in 1h 29m)")
	for _, masked := range []string{"rejected the request", "had no subscription capacity"} {
		require.NotContains(t, body, masked)
	}
}
