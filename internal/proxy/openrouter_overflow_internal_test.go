package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

type overflowDispatch struct {
	svc        *Service
	anthropic  *fakeClient
	openRouter *fakeClient
	served     []router.Decision
}

func newOverflowDispatch(activeProviders map[subscriptions.Provider]bool) *overflowDispatch {
	d := &overflowDispatch{
		anthropic:  &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("anthropic answer")}}},
		openRouter: &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("openrouter answer")}}},
	}
	d.svc = NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderAnthropic: d.anthropic, providers.ProviderOpenRouter: d.openRouter}, nil, false, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: activeProviders})
	d.svc.deploymentKeyedProviders = map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}, providers.ProviderOpenRouter: {}}
	return d
}

func overflowContext(exhausted bool) context.Context {
	ctx := managedSubscriptionTestContext()
	if exhausted {
		ctx = exhaustedManagedSubscriptionContext(auth.SubscriptionProviderClaude)
	}
	return flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeySubscriptionExhaustedOpenRouterOnly: true}})
}

func (d *overflowDispatch) attempt() dispatchAttempt {
	return func(attemptCtx context.Context, decision router.Decision, client providers.Client) error {
		d.served = append(d.served, decision)
		return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	}
}

func (d *overflowDispatch) run(ctx context.Context, laneRequest bool) error {
	rec := httptest.NewRecorder()
	in := failoverInputs{
		w: rec, buf: newPreludeBuffer(rec),
		initialDecision:  router.Decision{Model: stateClaudeModel, Provider: providers.ProviderAnthropic},
		bindings:         []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
		attempt:          d.attempt(),
		purpose:          inference.PurposeAnthropicMessages,
		buildAlternative: func(router.Decision) (dispatchAttempt, error) { return d.attempt(), nil },
	}
	if laneRequest {
		in.subscriptionStateRequest = &router.Request{
			AllowedModels:    allowedModelsForRequest(ctx),
			EnabledProviders: modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter}),
		}
	}
	_, err := d.svc.dispatchWithFallback(ctx, in)
	return err
}

func TestOpenRouterOverflowServesExhaustedSubscriptionOnOpenRouter(t *testing.T) {
	d := newOverflowDispatch(map[subscriptions.Provider]bool{})

	require.NoError(t, d.run(overflowContext(true), true))

	require.Equal(t, 0, d.anthropic.calls, "an exhausted subscription must not fall back to the direct Anthropic key")
	require.Equal(t, 1, d.openRouter.calls)
	require.Equal(t, []router.Decision{{Model: stateClaudeModel, Provider: providers.ProviderOpenRouter}}, servedTargets(d.served))
}

func TestOpenRouterOverflowKeepsLiveSubscriptionFirst(t *testing.T) {
	d := newOverflowDispatch(map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true})

	require.NoError(t, d.run(overflowContext(false), true))

	require.Equal(t, 1, d.anthropic.calls)
	require.Equal(t, 0, d.openRouter.calls)
	require.Equal(t, []router.Decision{{Model: stateClaudeModel, Provider: providers.ProviderAnthropic}}, servedTargets(d.served))
}

func TestOpenRouterOverflowFailsClosedWhenOpenRouterExcluded(t *testing.T) {
	d := newOverflowDispatch(map[subscriptions.Provider]bool{})
	ctx := context.WithValue(overflowContext(true), InstallationExcludedProvidersContextKey{}, []string{providers.ProviderOpenRouter})

	require.Error(t, d.run(ctx, true))

	require.Equal(t, 0, d.anthropic.calls)
	require.Equal(t, 0, d.openRouter.calls)
}

func TestOpenRouterOverflowRewritesPaidRescueOutsideLane(t *testing.T) {
	d := newOverflowDispatch(map[subscriptions.Provider]bool{})

	require.NoError(t, d.run(subscriptionStatePaidRescueContext(overflowContext(true), stateClaudeModel), false))

	require.Equal(t, 0, d.anthropic.calls)
	require.Equal(t, []router.Decision{{Model: stateClaudeModel, Provider: providers.ProviderOpenRouter}}, servedTargets(d.served))
}

func TestOpenRouterOverflowOffKeepsDirectPaidFallback(t *testing.T) {
	d := newOverflowDispatch(map[subscriptions.Provider]bool{})
	ctx := context.WithValue(exhaustedManagedSubscriptionContext(auth.SubscriptionProviderClaude), InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{stateClaudeModel})

	require.NoError(t, d.run(ctx, true))

	require.Equal(t, 0, d.openRouter.calls)
	require.Equal(t, []router.Decision{{Model: stateClaudeModel, Provider: providers.ProviderAnthropic}}, servedTargets(d.served))
}

func TestOpenRouterOverflowModelsKeepOnlyOpenRouterServedModels(t *testing.T) {
	ctx := overflowContext(false)
	require.Contains(t, installationSubscriptionModelsWhenInactiveFromContext(ctx), stateClaudeModel)
	require.NotContains(t, installationSubscriptionModelsWhenInactiveFromContext(ctx), "claude-opus-4-0")

	configured := context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{"claude-opus-4-0", "gpt-5.4", "gemini-2.5-pro"})
	require.Equal(t, []string{"gpt-5.4"}, installationSubscriptionModelsWhenInactiveFromContext(configured))
}

func servedTargets(served []router.Decision) []router.Decision {
	out := make([]router.Decision, 0, len(served))
	for _, decision := range served {
		out = append(out, router.Decision{Model: decision.Model, Provider: decision.Provider})
	}
	return out
}

func TestOpenRouterOverflowServesConfiguredModelRoutingCannotSee(t *testing.T) {
	d := newOverflowDispatch(map[subscriptions.Provider]bool{})
	d.svc.deploymentKeyedProviders = map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenRouter: {}}
	ctx := context.WithValue(overflowContext(true), InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{"gpt-5.4"})

	require.NoError(t, d.run(ctx, true))

	require.Equal(t, 0, d.anthropic.calls)
	require.Equal(t, []router.Decision{{Model: "gpt-5.4", Provider: providers.ProviderOpenRouter}}, servedTargets(d.served))
}
