package proxy

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestRouteRefusesAStrategyDecisionOutsideTheEligibleCandidatesWithoutAnEligibleFallback(t *testing.T) {
	served := router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}
	svc := serviceWithDefaultRouter(&registryRouter{decision: served})

	for violated, candidates := range map[string]router.Candidates{
		"excluded_models":                  {ExcludedModels: map[string]struct{}{served.Model: {}}},
		"safety_excluded_models":           {SafetyExcludedModels: map[string]struct{}{served.Model: {}}},
		"context_window_excluded_models":   {ContextWindowExcludedModels: map[string]struct{}{served.Model: {}}},
		"image_limit_excluded_models":      {ImageLimitExcludedModels: map[string]struct{}{served.Model: {}}},
		"unsigned_history_excluded_models": {UnsignedHistoryExcludedModels: map[string]struct{}{served.Model: {}}},
		"allowed_models":                   {AllowedModels: map[string]struct{}{"claude-haiku-4-5": {}}},
	} {
		t.Run(violated, func(t *testing.T) {
			var output bytes.Buffer
			ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))

			_, err := svc.Route(ctx, router.Request{Candidates: candidates})

			require.ErrorIs(t, err, policy.ErrNoEligibleArm)
			assert.Contains(t, output.String(), `"level":"WARN"`)
			assert.Contains(t, output.String(), `"violated_set":"`+violated+`"`)
			assert.Contains(t, output.String(), `"model":"`+served.Model+`"`)
		})
	}
}

func TestRouteServesTheFirstEligibleRankedFallbackForADecisionOutsideTheEligibleCandidates(t *testing.T) {
	picked := router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks, Reason: "hmm_policy", Metadata: &router.RoutingMetadata{
		CandidateModels:    []string{"deepseek/deepseek-v4-pro", "claude-opus-4-7", "claude-haiku-4-5", "claude-sonnet-4-5"},
		CandidateScores:    map[string]float32{"deepseek/deepseek-v4-pro": 0.9, "claude-opus-4-7": 0.8, "claude-haiku-4-5": 0.2, "claude-sonnet-4-5": 0.5},
		CandidateProviders: map[string]string{"deepseek/deepseek-v4-pro": providers.ProviderFireworks, "claude-opus-4-7": providers.ProviderAnthropic, "claude-haiku-4-5": providers.ProviderAnthropic, "claude-sonnet-4-5": providers.ProviderAnthropic},
	}}
	svc := serviceWithDefaultRouter(&registryRouter{decision: picked})

	decision, err := svc.Route(context.Background(), router.Request{Candidates: router.Candidates{
		SafetyExcludedModels:    map[string]struct{}{"deepseek/deepseek-v4-pro": {}},
		AutomaticExcludedModels: map[string]struct{}{"claude-opus-4-7": {}},
	}})

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5", decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, decision.Provider)
	assert.Equal(t, "candidate_fallback", decision.Reason)
}

func TestRouteKeepsAnAutomaticExcludedDecisionWhenNoRankedFallbackIsEligible(t *testing.T) {
	picked := router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}
	svc := serviceWithDefaultRouter(&registryRouter{decision: picked})

	decision, err := svc.Route(context.Background(), router.Request{Candidates: router.Candidates{
		AutomaticExcludedModels: map[string]struct{}{picked.Model: {}},
	}})

	require.NoError(t, err)
	assert.Equal(t, picked.Model, decision.Model)
}

func TestRouteDoesNotLogAStrategyDecisionInsideTheFilteredCandidates(t *testing.T) {
	svc := serviceWithDefaultRouter(&registryRouter{decision: router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}})
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))

	_, err := svc.Route(ctx, router.Request{Candidates: router.Candidates{
		ExcludedModels: map[string]struct{}{"claude-haiku-4-5": {}},
		AllowedModels:  map[string]struct{}{"deepseek/deepseek-v4-pro": {}},
	}})

	require.NoError(t, err)
	assert.NotContains(t, output.String(), "outside the filtered candidate set")
}
