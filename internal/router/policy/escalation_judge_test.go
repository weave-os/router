package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestEscalationJudgePolicyOnlyResolvesOpenRouter(t *testing.T) {
	resolver := newPlanResolver(t, modelSet(policy.EscalationJudgeModel), providerSet(providers.ProviderMakora, providers.ProviderOpenRouter))
	plan, err := resolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeEscalationJudge})
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderOpenRouter, plan.SelectedTarget().Provider)
	assert.Empty(t, plan.AlternativeTargets())
	assert.Equal(t, 1, plan.Budget().MaxAttempts)
	assert.EqualValues(t, 20_000, plan.Budget().TimeoutMillis)
	resolver = newPlanResolver(t, modelSet(policy.EscalationJudgeModel), providerSet(providers.ProviderMakora))
	_, err = resolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeEscalationJudge, RouterRequest: router.Request{EnabledProviders: providerSet(providers.ProviderMakora)}})
	require.Error(t, err)
}

func TestEscalationJudgeDeploymentIsOptional(t *testing.T) {
	config := policy.DeploymentPolicyConfig{AvailableProviders: providerSet(providers.ProviderAnthropic), TargetOverrides: []policy.PurposeTargetOverride{}}
	for _, purpose := range []policy.Purpose{policy.PurposeTitleGeneration, policy.PurposeClassifier, policy.PurposeProbe, policy.PurposeSubAgentDispatch, policy.PurposeClientCompaction} {
		config.TargetOverrides = append(config.TargetOverrides, policy.PurposeTargetOverride{Purpose: purpose, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: policy.UtilityHardPinDefaultModel, Provider: providers.ProviderAnthropic}})
	}
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config))
	config.EnabledOptionalPurposes = map[policy.Purpose]bool{policy.PurposeEscalationJudge: true}
	require.Error(t, policy.DefaultRegistry().ValidateDeployment(config))
	config.AvailableProviders[providers.ProviderOpenRouter] = struct{}{}
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config))
}
