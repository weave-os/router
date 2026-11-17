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
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

// goldenProviderPools leases from a separate managed pool per subscription
// provider, so one provider's pool can be spent while another still has seats.
type goldenProviderPools map[subscriptions.Provider]*goldenSubscriptionPool

func (p goldenProviderPools) Lease(ctx context.Context, owner auth.SubscriptionOwner, provider subscriptions.Provider, model string) (subscriptions.Lease, bool, error) {
	pool, ok := p[provider]
	if !ok {
		return subscriptions.Lease{}, false, nil
	}
	return pool.Lease(ctx, owner, provider, model)
}

func (p goldenProviderPools) Cooldown(ctx context.Context, owner auth.SubscriptionOwner, provider subscriptions.Provider, accountID string, until time.Time) error {
	return p[provider].Cooldown(ctx, owner, provider, accountID, until)
}

func (p goldenProviderPools) Disable(ctx context.Context, owner auth.SubscriptionOwner, provider subscriptions.Provider, accountID string) error {
	return p[provider].Disable(ctx, owner, provider, accountID)
}

// withSpentCodexPool enrolls a Codex pool whose only seat is cooling down and a
// Claude pool with a free seat.
func withSpentCodexPool(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	codex := newGoldenSubscriptionPool(h.clock.Now, "codex-seat")
	codex.cooling["codex-seat"] = goldenCodexSeatReset
	return svc.WithManagedSubscriptions(goldenProviderPools{
		subscriptions.ProviderCodex:  codex,
		subscriptions.ProviderClaude: newGoldenSubscriptionPool(h.clock.Now, "claude-seat"),
	})
}

var goldenCodexSeatReset = goldenEpoch.Add(time.Hour)

func goldenSpentCodexCtx(ctx context.Context) context.Context {
	ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, []string{goldenCodexModel}, []string{goldenCodexModel})
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &goldenCodexSeatReset},
		{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

const goldenCodexToolTurnPrefix = `{"model":"auto","stream":true,` +
	`"tools":[{"type":"function","name":"exec","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}],` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the failing build"}]},` +
	`{"type":"function_call","call_id":"call_1","name":"exec","arguments":"{\"cmd\":\"make build\"}"},` +
	`{"type":"function_call_output","call_id":"call_1","output":"build failed"}`

// goldenCodexPortableToolTurn continues the agent turn with a tool result any
// provider family can serve.
const goldenCodexPortableToolTurn = goldenCodexToolTurnPrefix + `]}`

// goldenCodexNativeOnlyToolTurn continues the same agent turn with a tool
// output only the Responses wire family can carry.
const goldenCodexNativeOnlyToolTurn = goldenCodexToolTurnPrefix + `,` +
	`{"type":"function_call","call_id":"call_2","name":"exec","arguments":"{\"cmd\":\"make test\"}"},` +
	`{"type":"function_call_output","call_id":"call_2","output":{"exit_code":1}}]}`

// goldenSubscriptionExhaustionScenarios pin an agent turn that outlives its
// Codex pool: the spent pool rescues onto Claude, then a native-only
// continuation must still be served by OpenAI on paid capacity.
func goldenSubscriptionExhaustionScenarios() []goldenScenario {
	return []goldenScenario{{
		name: ingressResponses + "_stream_spent_codex_pool_native_only_continuation_served_on_paid_key", ingress: ingressResponses,
		decisions: []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel), decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		// What the HMM policy returns once every arm it may rank is excluded.
		noCandidateErr: fmt.Errorf("hmm_embedding: arm selection: %w: %w", policy.ErrNoEligibleArm, hmm.ErrHMMUnavailable),
		upstreams: map[string][]goldenStep{
			providers.ProviderAnthropic: {anthropicOK(true, "rescued on claude")},
			providers.ProviderOpenAI:    {goldenResponsesOK("served on paid key")},
		},
		requests: []goldenRequest{
			{body: goldenCodexPortableToolTurn, headers: goldenCodexHeaders, decorateContext: goldenSpentCodexCtx},
			{body: goldenCodexNativeOnlyToolTurn, headers: goldenCodexHeaders, decorateContext: goldenSpentCodexCtx},
		},
		configure: withSpentCodexPool,
	}}
}

// TestSpentCodexPoolNativeOnlyContinuationIsServed states the invariant the
// scenario pins: a spent Codex pool is a funding outcome, not a model failure,
// so it must not leave a native-only continuation with nothing to route to.
func TestSpentCodexPoolNativeOnlyContinuationIsServed(t *testing.T) {
	t.Parallel()
	sc := goldenSubscriptionExhaustionScenarios()[0]
	h := newGoldenHarness(t, sc)
	rescued := h.serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, rescued.Response.Status)

	continuation := sc.requests[1]
	continuation.noTelemetry = true
	served := h.serve(t, sc.ingress, continuation)
	require.Equal(t, http.StatusOK, served.Response.Status, "continuation response: %+v", served.Response.Body)
	require.NotEmpty(t, served.Attempts)
	last := served.Attempts[len(served.Attempts)-1]
	require.Equal(t, providers.ProviderOpenAI, last.Provider)
	require.Empty(t, last.Credential, "the spent pool cannot fund the continuation")
	require.Equal(t, "ok", last.Outcome)
}
