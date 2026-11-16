package proxy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

const goldenCodexTokenRejectedBody = `{"error":{"code":"token_invalidated","message":"golden token rejected"}}`

var goldenClaudeSeatReset = goldenEpoch.Add(time.Hour)

// goldenPlanIneligiblePools leave the Codex pool empty and lease a Claude seat
// whose stored state is still spent, so admission refuses it for the plan.
type goldenPlanIneligiblePools struct{ goldenProviderPools }

func (p goldenPlanIneligiblePools) Lease(ctx context.Context, owner auth.SubscriptionOwner, provider subscriptions.Provider, model string) (subscriptions.Lease, bool, error) {
	lease, ok, err := p.goldenProviderPools.Lease(ctx, owner, provider, model)
	if provider == subscriptions.ProviderClaude && err == nil {
		lease.State, lease.CooldownUntil = auth.SubscriptionAccountStateExhausted, goldenClaudeSeatReset
	}
	return lease, ok, err
}

func withRejectedCodexLoginPools(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return withGoldenClock(svc, h).WithIngress(ingress.New(5 * time.Millisecond)).WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
		WithManagedSubscriptions(goldenPlanIneligiblePools{goldenProviderPools{
			subscriptions.ProviderCodex:  newGoldenSubscriptionPool(h.clock.Now),
			subscriptions.ProviderClaude: newGoldenSubscriptionPool(h.clock.Now, "claude-seat"),
		}})
}

func goldenRejectedCodexLoginCtx(ctx context.Context) context.Context {
	ctx = goldenCodexCtx(ctx)
	ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, []string{goldenCodexModel, goldenAnthropicModel, goldenCodexAlternativeModel}, []string{goldenCodexModel, goldenAnthropicModel, goldenCodexAlternativeModel})
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

func goldenCodexLoginRejectedScenarios() []goldenScenario {
	return []goldenScenario{{
		name: ingressResponses + "_stream_codex_login_rejected_after_subscription_ladder", ingress: ingressResponses,
		decisions: []router.Decision{
			decision(providers.ProviderOpenAI, goldenCodexModel),
			decision(providers.ProviderAnthropic, goldenAnthropicModel),
			decision(providers.ProviderOpenAI, goldenCodexAlternativeModel),
		},
		noCandidateErr: fmt.Errorf("hmm_embedding: arm selection: %w: %w", policy.ErrNoEligibleArm, hmm.ErrHMMUnavailable),
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusUnauthorized, goldenCodexTokenRejectedBody),
				goldenUpstreamError(http.StatusUnauthorized, goldenCodexTokenRejectedBody),
				goldenResponsesOK("served on credits")},
			providers.ProviderAnthropic: {anthropicOK(true, "never served")}},
		requests: []goldenRequest{
			{body: goldenResponsesBody(true), headers: goldenCodexHeaders, decorateContext: goldenRejectedCodexLoginCtx},
			{body: goldenResponsesBody(true), headers: goldenCodexHeaders, decorateContext: goldenRejectedCodexLoginCtx, advance: time.Second}},
		configure: withRejectedCodexLoginPools,
	}}
}

// TestRejectedCodexLoginRescueDispatchesLadderTarget states the invariant the
// scenario pins: once the subscription ladder has moved the turn to another
// model, the Weave-credit rescue prepares that model's request, so a rejected
// login surfaces its reconnect instruction instead of a generic router error.
func TestRejectedCodexLoginRescueDispatchesLadderTarget(t *testing.T) {
	t.Parallel()
	sc := goldenCodexLoginRejectedScenarios()[0]
	h := newGoldenHarness(t, sc)
	served := h.serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, served.Response.Status)

	attempts, err := json.Marshal(served.InferenceAttempts)
	require.NoError(t, err)
	require.NotContains(t, string(attempts), string(dispatch.FailureReasonTargetMismatch))

	require.NotEmpty(t, served.Response.Events)
	terminal := served.Response.Events[len(served.Response.Events)-1]
	require.Equal(t, "response.failed", terminal.Event)
	payload, err := json.Marshal(terminal.Data)
	require.NoError(t, err)
	require.Contains(t, string(payload), "ChatGPT (Codex) login was rejected")
}
