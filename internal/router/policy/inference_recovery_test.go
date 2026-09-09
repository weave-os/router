package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestRecoveryIndependentlyAuthorizesEveryBinding(t *testing.T) {
	resolver := newPlanResolver(t, modelSet("claude-haiku-4-5", "claude-sonnet-4-6", "gpt-5.6-luna"), providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI))
	for _, tc := range []struct {
		name            string
		request         router.Request
		previousModel   string
		configuredModel string
		fit             func(policy.Binding) bool
		want            []string
	}{
		{"live history first", router.Request{}, "claude-sonnet-4-6", "claude-haiku-4-5", nil, []string{"claude-sonnet-4-6", "gpt-5.6-luna", "claude-haiku-4-5"}},
		{"excluded history skipped", router.Request{ExcludedModels: modelSet("claude-sonnet-4-6")}, "claude-sonnet-4-6", "claude-haiku-4-5", nil, []string{"claude-haiku-4-5", "gpt-5.6-luna"}},
		{"credentials restrict every plan", router.Request{EnabledProviders: providerSet(providers.ProviderOpenAI)}, "claude-sonnet-4-6", "claude-haiku-4-5", nil, []string{"gpt-5.6-luna"}},
		{"full envelope fit restricts every plan", router.Request{}, "claude-sonnet-4-6", "claude-haiku-4-5", func(b policy.Binding) bool { return b.Provider == providers.ProviderOpenAI }, []string{"gpt-5.6-luna"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fit := tc.fit
			if fit == nil {
				fit = func(policy.Binding) bool { return true }
			}
			plans, err := resolver.ResolveRecovery(policy.ResolutionRequest{Purpose: policy.PurposeAnthropicMessages, RouterRequest: tc.request}, tc.previousModel, tc.configuredModel, fit)
			require.NoError(t, err)
			var models []string
			for _, plan := range plans {
				models = append(models, plan.SelectedTarget().CatalogID)
				assert.Equal(t, inference.SelectionStrategyRecovery, plan.SelectionStrategy())
				for _, target := range plan.AlternativeTargets() {
					assert.Equal(t, plan.SelectedTarget().CatalogID, target.CatalogID)
				}
			}
			assert.Equal(t, tc.want, models)
		})
	}
}

func TestRecoveryPreservesExplicitAndEmptyTargetContracts(t *testing.T) {
	resolver := newPlanResolver(t, modelSet("claude-haiku-4-5"), providerSet(providers.ProviderAnthropic))
	for _, req := range []router.Request{
		{ShadowMode: true}, {ForceModel: "claude-haiku-4-5"}, {ForceCluster: "synthetic"},
		{ExcludedModels: modelSet("claude-haiku-4-5")}, {EnabledProviders: providerSet(providers.ProviderOpenAI)},
	} {
		plans, err := resolver.ResolveRecovery(policy.ResolutionRequest{Purpose: policy.PurposeAnthropicMessages, RouterRequest: req}, "", "", func(policy.Binding) bool { return true })
		var resolution *policy.ResolutionError
		require.ErrorAs(t, err, &resolution)
		assert.Empty(t, plans)
	}
}

func TestRecoveryWithoutEnvelopeRetainsEstimatedContextLimit(t *testing.T) {
	resolver := newPlanResolver(t, modelSet("claude-haiku-4-5"), providerSet(providers.ProviderAnthropic))
	plans, err := resolver.ResolveRecovery(policy.ResolutionRequest{Purpose: policy.PurposeAnthropicMessages, RouterRequest: router.Request{EstimatedInputTokens: 2_000_000}}, "", "", nil)
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
	assert.Empty(t, plans)
}

func TestRecoveryPreservesGatewayIntersectionAndEffectiveContext(t *testing.T) {
	resolver := newPlanResolver(t, modelSet("claude-sonnet-4-6"), providerSet(providers.ProviderAnthropic, providers.ProviderAnthropicGateway, providers.ProviderOpenAIGateway))
	request := router.Request{
		EstimatedInputTokens: 250000,
		EnabledProviders:     providerSet(providers.ProviderAnthropicGateway),
		GatewayProviders:     providerSet(providers.ProviderAnthropicGateway, providers.ProviderOpenAIGateway),
		CustomBindings:       map[string][]string{"claude-sonnet-4-6": {providers.ProviderAnthropicGateway, providers.ProviderOpenAIGateway}},
	}
	plans, err := resolver.ResolveRecovery(policy.ResolutionRequest{Purpose: policy.PurposeAnthropicMessages, RouterRequest: request}, "claude-sonnet-4-6:high", "", func(b policy.Binding) bool { return b.Provider == providers.ProviderAnthropicGateway })
	require.NoError(t, err)
	require.Len(t, plans, 1)
	assert.Equal(t, providers.ProviderAnthropicGateway, plans[0].SelectedTarget().Provider)
	assert.Equal(t, "high", plans[0].SelectedTarget().Effort)
	assert.Empty(t, plans[0].AlternativeTargets())
	_, err = resolver.ResolveRecovery(policy.ResolutionRequest{Purpose: policy.PurposeAnthropicMessages, RouterRequest: request}, "", "", func(policy.Binding) bool { return false })
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
}
