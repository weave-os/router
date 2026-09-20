package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/subscriptions/entitlement"
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
