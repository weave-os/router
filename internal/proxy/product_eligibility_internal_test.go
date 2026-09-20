package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

func maxScopedContext() context.Context {
	return entitlement.WithProductScope(context.Background(), entitlement.PlanMax)
}

func TestMaxRequestCarriesTheBoundaryAndHardExclusions(t *testing.T) {
	svc := &Service{}

	req := svc.withPolicyRequestContext(maxScopedContext(), router.Request{})

	assert.True(t, req.ProductEligibility.Restricts())
	// Desugared into the HARD set, not AutomaticExcludedModels: a router may
	// drop the soft set to keep a pool non-empty, which would serve exactly
	// the models Max does not sell.
	assert.Contains(t, req.ExcludedModels, "claude-opus-4-8")
	assert.Contains(t, req.ExcludedModels, "muse-spark-1.3")
	assert.NotContains(t, req.ExcludedModels, "deepseek/deepseek-v4-pro")
	assert.NotContains(t, req.AutomaticExcludedModels, "claude-opus-4-8")
}

func TestUnsubscribedRequestKeepsUnrestrictedRouting(t *testing.T) {
	svc := &Service{}

	req := svc.withPolicyRequestContext(context.Background(), router.Request{})

	assert.False(t, req.ProductEligibility.Restricts())
	assert.Empty(t, req.ExcludedModels)
}

// A router that reaches a closed-source model anyway — from a pin, a fallback
// table, or its deployed-set default — is refused rather than dispatched.
func TestMaxRouteRefusesAClosedSourceDecision(t *testing.T) {
	closedRouter := &registryRouter{decision: router.Decision{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic}}
	svc := &Service{router: closedRouter}

	_, err := svc.Route(maxScopedContext(), router.Request{})

	require.ErrorIs(t, err, eligibility.ErrModelIneligible)
	assert.Contains(t, err.Error(), "claude-opus-4-8")
}

func TestMaxRouteRefusesAnUnknownSourceDecision(t *testing.T) {
	unknownRouter := &registryRouter{decision: router.Decision{Model: "muse-spark-1.3", Provider: providers.ProviderMeta}}
	svc := &Service{router: unknownRouter}

	_, err := svc.Route(maxScopedContext(), router.Request{})

	require.ErrorIs(t, err, eligibility.ErrModelIneligible)
}

func TestMaxRouteServesOpenSourceDecision(t *testing.T) {
	openRouter := &registryRouter{decision: router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}}
	svc := &Service{router: openRouter}

	decision, err := svc.Route(maxScopedContext(), router.Request{})

	require.NoError(t, err)
	assert.Equal(t, "deepseek/deepseek-v4-pro", decision.Model)
}

// Funding is orthogonal to eligibility: prepaid capacity, a billing override,
// and a covered included allowance all leave the boundary exactly as it was.
// Max sells an open-source-only boundary, so no book paying for the turn can
// buy a closed-source model.
func TestFundingCannotWidenMaxEligibility(t *testing.T) {
	closedRouter := &registryRouter{decision: router.Decision{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic}}
	svc := &Service{router: closedRouter}

	for name, ctx := range map[string]context.Context{
		// Included allowance paying for the turn.
		"included coverage": entitlement.WithCoverage(maxScopedContext(), entitlement.Coverage{Plan: entitlement.PlanMax}),
		// Allowance spent, so the turn falls through to the prepaid and
		// organization books that fund it; the stamped scope still holds.
		"allowance spent, other funds paying": maxScopedContext(),
		// The org-wide billing override, the broadest funding escape hatch.
		"billing override": context.WithValue(maxScopedContext(), billing.HasOverrideContextKey, true),
	} {
		t.Run(name, func(t *testing.T) {
			req := svc.withPolicyRequestContext(ctx, router.Request{})
			assert.Contains(t, req.ExcludedModels, "claude-opus-4-8")

			_, err := svc.Route(ctx, router.Request{})
			require.ErrorIs(t, err, eligibility.ErrModelIneligible)
		})
	}
}

// The turn loop mints decisions the routed pool never produced — a session
// pin, a hard-pinned auxiliary turn, /force-model. They all reach the
// provider through dispatchWithFallback, which is where the boundary is
// terminal.
func TestDispatchRefusesAnIneligibleDecisionTheTurnLoopMinted(t *testing.T) {
	anthropic := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("served")}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: anthropic})

	rec := httptest.NewRecorder()
	in := plannedInputs(rec, newPreludeBuffer(rec), []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic}

	_, err := svc.dispatchWithFallback(maxScopedContext(), in)

	require.ErrorIs(t, err, eligibility.ErrModelIneligible)
	assert.Zero(t, anthropic.calls)
}

func TestDispatchServesAnEligibleDecision(t *testing.T) {
	fireworks := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{writeBytes: []byte("served")}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: fireworks})

	rec := httptest.NewRecorder()
	in := plannedInputs(rec, newPreludeBuffer(rec), []catalog.ProviderBinding{{Provider: providers.ProviderFireworks}}, nil)

	_, err := svc.dispatchWithFallback(maxScopedContext(), in)

	require.NoError(t, err)
	assert.Equal(t, 1, fireworks.calls)
}

// The subscription pass-through lane serves the requested model verbatim
// without routing, so an ineligible model disengages it and the turn falls
// through to routed dispatch instead.
func TestUsageBypassDisengagesForAnIneligibleModel(t *testing.T) {
	const token = "sk-ant-oat01-test-subscription-token"
	threshold := 0.80
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(token)), usage.Snapshot{Primary: usage.Window{UsedPercent: 0.20, WindowMinutes: 300}})
	svc := &Service{usageObserver: obs}
	ctx := context.WithValue(maxScopedContext(), AnthropicSubscriptionContextKey{}, token)
	ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true, Threshold: &threshold})

	_, engaged := svc.usageBypassDecision(ctx, http.Header{}, router.Request{
		RequestedModel:     "claude-sonnet-4-6",
		ProductEligibility: eligibility.MaxOpenSourceOnly,
	}, nil, turntype.MainLoop)

	assert.False(t, engaged)
}

// Agent-shadow evaluation forces a preplanned canonical model and skips the
// scorer entirely, so it is gated on the boundary directly.
func TestAgentShadowEvaluationRefusesAnIneligibleModel(t *testing.T) {
	svc := &Service{}
	env := bypassAnthropicEnvelope(t)

	_, err := svc.runAgentShadowEvaluationRoute(
		maxScopedContext(), env, translate.RoutingFeatures{Model: "claude-opus-4-8"}, uuid.Nil,
		router.Request{ProductEligibility: eligibility.MaxOpenSourceOnly},
		AgentShadowEvaluation{Model: "claude-opus-4-8", RolloutID: "rollout-1", StateID: "state-1"},
	)

	require.ErrorIs(t, err, eligibility.ErrModelIneligible)
}

// A refusal is the caller's problem to fix (pick a covered model), not an
// upstream failure: it must not fall through to the generic 502.
func TestIneligibleModelIsAClassifiedClientError(t *testing.T) {
	cls, matched := ClassifyDispatchError(fmt.Errorf("strategy %q: %w", "cluster", eligibility.ErrModelIneligible))

	require.True(t, matched)
	assert.Equal(t, DispatchErrorProductIneligible, cls.Kind)
	assert.Equal(t, http.StatusForbidden, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
}

// The operator escape hatch replaces the exclusion set wholesale; the
// product boundary still survives it.
func TestExcludedModelsOverrideCannotWidenTheBoundary(t *testing.T) {
	svc := &Service{excludedModelsOverride: map[string]struct{}{"gpt-5.5": {}}}

	excluded := svc.excludedModelsForRequest(maxScopedContext())

	assert.Contains(t, excluded, "claude-opus-4-8")
	assert.Contains(t, excluded, "muse-spark-1.3")
	assert.Contains(t, excluded, "gpt-5.5")
	assert.NotContains(t, excluded, "deepseek/deepseek-v4-pro")
	assert.NotContains(t, svc.excludedModelsOverride, "claude-opus-4-8")
}
