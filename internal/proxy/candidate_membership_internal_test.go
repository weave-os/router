package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
)

func TestRouteRefusesAStrategyDecisionOutsideTheFilteredCandidates(t *testing.T) {
	strategy := &registryRouter{decision: router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}}
	svc := (&Service{}).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: strategy, Unavailable: hmm.ErrHMMUnavailable})

	for name, candidates := range map[string]router.Candidates{
		"excluded":         {ExcludedModels: map[string]struct{}{"deepseek/deepseek-v4-pro": {}}},
		"context window":   {ContextWindowExcludedModels: map[string]struct{}{"deepseek/deepseek-v4-pro": {}}},
		"unsigned history": {UnsignedHistoryExcludedModels: map[string]struct{}{"deepseek/deepseek-v4-pro": {}}},
		"not allowlisted":  {AllowedModels: map[string]struct{}{"claude-haiku-4-5": {}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Route(context.Background(), router.Request{Candidates: candidates})

			require.ErrorIs(t, err, policy.ErrNoEligibleArm)
			require.ErrorIs(t, err, hmm.ErrHMMUnavailable)
			assert.Contains(t, err.Error(), "deepseek/deepseek-v4-pro")
		})
	}
}

func TestRouteServesAStrategyDecisionInsideTheFilteredCandidates(t *testing.T) {
	strategy := &registryRouter{decision: router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}}
	svc := serviceWithDefaultRouter(strategy)

	decision, err := svc.Route(context.Background(), router.Request{Candidates: router.Candidates{
		ExcludedModels: map[string]struct{}{"claude-haiku-4-5": {}},
		AllowedModels:  map[string]struct{}{"deepseek/deepseek-v4-pro": {}},
	}})

	require.NoError(t, err)
	assert.Equal(t, "deepseek/deepseek-v4-pro", decision.Model)
}
