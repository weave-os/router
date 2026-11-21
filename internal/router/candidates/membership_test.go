package candidates_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/policy"
)

func TestEnforceMembershipKeepsADecisionInsideTheCandidates(t *testing.T) {
	picked := router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}

	decision, err := candidates.EnforceMembership(context.Background(), router.Strategy("scripted"), router.Request{Candidates: router.Candidates{
		AllowedModels: map[string]struct{}{picked.Model: {}},
	}}, picked)

	require.NoError(t, err)
	assert.Equal(t, picked, decision)
}

func TestEnforceMembershipRefusesAHardViolationWithoutAnEligibleFallback(t *testing.T) {
	picked := router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}

	_, err := candidates.EnforceMembership(context.Background(), router.Strategy("scripted"), router.Request{Candidates: router.Candidates{
		SafetyExcludedModels: map[string]struct{}{picked.Model: {}},
	}}, picked)

	require.ErrorIs(t, err, policy.ErrNoEligibleArm)
}

func TestEnforceMembershipServesTheFirstEligibleRankedFallback(t *testing.T) {
	picked := router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks, Metadata: &router.RoutingMetadata{
		CandidateModels:    []string{"deepseek/deepseek-v4-pro", "claude-opus-4-7", "claude-sonnet-4-5"},
		CandidateScores:    map[string]float32{"deepseek/deepseek-v4-pro": 0.9, "claude-opus-4-7": 0.8, "claude-sonnet-4-5": 0.5},
		CandidateProviders: map[string]string{"deepseek/deepseek-v4-pro": providers.ProviderFireworks, "claude-opus-4-7": providers.ProviderAnthropic, "claude-sonnet-4-5": providers.ProviderAnthropic},
	}}

	decision, err := candidates.EnforceMembership(context.Background(), router.Strategy("scripted"), router.Request{Candidates: router.Candidates{
		SafetyExcludedModels:    map[string]struct{}{"deepseek/deepseek-v4-pro": {}},
		AutomaticExcludedModels: map[string]struct{}{"claude-opus-4-7": {}},
	}}, picked)

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5", decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, decision.Provider)
}

func TestEnforceMembershipKeepsAnAutomaticExclusionWithoutAnEligibleFallback(t *testing.T) {
	picked := router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}

	decision, err := candidates.EnforceMembership(context.Background(), router.Strategy("scripted"), router.Request{Candidates: router.Candidates{
		AutomaticExcludedModels: map[string]struct{}{picked.Model: {}},
	}}, picked)

	require.NoError(t, err)
	assert.Equal(t, picked, decision)
}
