package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

// rosterExcluding builds a plan resolver whose scoring roster maps every
// deployed model except the excluded ones, so the fallback path can be shown
// to work without a roster entry.
func rosterExcluding(t *testing.T, deployed, available map[string]struct{}, excluded ...string) *policy.PlanResolver {
	t.Helper()
	excludedSet := modelSet(excluded...)
	candidateResolver := policy.NewResolver(
		deployed,
		available,
		func(model catalog.Model) string {
			if _, skip := excludedSet[model.ID]; skip {
				return ""
			}
			return model.ID
		},
		policy.ManagedProviderPolicy(),
	)
	planResolver, err := policy.NewPlanResolver(policy.DefaultRegistry(), candidateResolver)
	require.NoError(t, err)
	return planResolver
}

func originalTarget(catalogID string) policy.TargetOverride {
	return policy.TargetOverride{Source: policy.OverrideSourceClientAuthoritative, CatalogID: catalogID}
}

func TestOriginalModelFallbackPolicyContract(t *testing.T) {
	spec, found := policy.DefaultRegistry().Spec(policy.PurposeOriginalModelFallback)
	require.True(t, found)

	assert.Equal(t, policy.SelectionStrategyClientAuthoritative, spec.SelectionStrategy)
	assert.Equal(t, policy.CandidateSourceRequest, spec.CandidateSource)
	assert.Equal(t, policy.DispatchClassClientAuthoritative, spec.DispatchClass)
	assert.Equal(t, 1, spec.Budget.MaxAttempts, "the fallback issues exactly one upstream attempt")
	assert.Equal(t, policy.FallbackSpec{Kind: policy.FallbackKindNone}, spec.Fallback, "no alternative target may be substituted")
	assert.Equal(t, []policy.OverrideSource{policy.OverrideSourceClientAuthoritative}, spec.OverridePrecedence)
	assert.Empty(t, spec.FixedCatalogModels)
	assert.Equal(t, policy.MigrationStatusExecutor, spec.MigrationStatus)
}

func TestResolveOriginalUsesRosterBindingForRoutableModel(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
	)

	plan, err := planResolver.ResolveOriginal(router.Request{}, originalTarget("claude-haiku-4-5"))
	require.NoError(t, err)

	assert.Equal(t, policy.PurposeOriginalModelFallback, plan.Purpose())
	assert.Equal(t, "claude-haiku-4-5", plan.SelectedBinding().CatalogID)
	assert.Equal(t, providers.ProviderAnthropic, plan.SelectedBinding().Provider)
	assert.Empty(t, plan.AlternativeTargets(), "the fallback plan never carries alternative targets")
	assert.Equal(t, 1, plan.Budget().MaxAttempts)
	assert.Equal(t, policy.SelectionStrategyClientAuthoritative, plan.Provenance().SelectionStrategy)
	assert.Equal(t, policy.OverrideSourceClientAuthoritative, plan.Provenance().OverrideSource)
	assert.Equal(t, policy.DefaultRegistry().Revision(), plan.RegistryRevision())
}

func TestResolveOriginalServesProviderSupportedModelOutsideRoster(t *testing.T) {
	// claude-fable-5 is untiered: the routing roster never offers it, yet the
	// caller asked for it by name and Anthropic serves it natively.
	planResolver := rosterExcluding(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:   policy.PurposeAgentShadowEvaluation,
		Overrides: []policy.TargetOverride{{Source: policy.OverrideSourceRequest, CatalogID: "claude-fable-5"}},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)

	plan, err := planResolver.ResolveOriginal(router.Request{}, originalTarget("claude-fable-5"))
	require.NoError(t, err)

	target := plan.SelectedTarget()
	assert.Equal(t, "claude-fable-5", target.CatalogID)
	assert.Equal(t, providers.ProviderAnthropic, target.Provider)
	assert.Equal(t, "claude-fable-5", target.UpstreamID, "a binding without an upstream override keeps the catalog spelling")
	assert.Equal(t, 0, target.BindingIndex)
}

func TestResolveOriginalServesRosterExcludedDeployedModel(t *testing.T) {
	planResolver := rosterExcluding(t,
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
		"gpt-5.6-luna",
	)

	plan, err := planResolver.ResolveOriginal(router.Request{}, originalTarget("gpt-5.6-luna"))
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderOpenAI, plan.SelectedBinding().Provider)
	assert.Equal(t, "gpt-5.6-luna", plan.SelectedBinding().CatalogID)
}

func TestResolveOriginalRejectsUnknownModelWithoutInventingProvider(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
	)

	_, err := planResolver.ResolveOriginal(router.Request{}, originalTarget("totally-unknown-model"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)

	_, err = planResolver.ResolveOriginal(router.Request{}, policy.TargetOverride{
		Source:    policy.OverrideSourceClientAuthoritative,
		CatalogID: "totally-unknown-model",
		Provider:  providers.ProviderAnthropic,
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestResolveOriginalRejectsProviderWithoutBinding(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
	)

	_, err := planResolver.ResolveOriginal(router.Request{}, policy.TargetOverride{
		Source:    policy.OverrideSourceClientAuthoritative,
		CatalogID: "claude-haiku-4-5",
		Provider:  providers.ProviderOpenAI,
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
}

func TestResolveOriginalHonorsTenantModelDenials(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
	)

	_, err := planResolver.ResolveOriginal(router.Request{
		ExcludedModels: modelSet("claude-haiku-4-5"),
	}, originalTarget("claude-haiku-4-5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)

	_, err = planResolver.ResolveOriginal(router.Request{
		AllowedModels: modelSet("gpt-5.6-luna"),
	}, originalTarget("claude-haiku-4-5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)

	// An untiered model is still subject to the same tenant denials even
	// though it never enters the roster.
	_, err = planResolver.ResolveOriginal(router.Request{
		ExcludedModels: modelSet("claude-fable-5"),
	}, originalTarget("claude-fable-5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
}

func TestResolveOriginalHonorsProviderDenials(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
	)

	_, err := planResolver.ResolveOriginal(router.Request{
		EnabledProviders: providerSet(providers.ProviderOpenAI),
	}, originalTarget("claude-haiku-4-5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)

	_, err = planResolver.ResolveOriginal(router.Request{
		EnabledProviders: providerSet(providers.ProviderOpenAI),
	}, originalTarget("claude-fable-5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
}

func TestResolveOriginalHonorsManagedProviderPolicy(t *testing.T) {
	planResolver := rosterExcluding(t,
		modelSet("moonshotai/kimi-k2.5"),
		providerSet(providers.ProviderOpenRouter),
		"moonshotai/kimi-k2.5",
	)

	_, err := planResolver.ResolveOriginal(router.Request{}, originalTarget("moonshotai/kimi-k2.5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
}

func TestResolveOriginalPreservesGatewayIsolation(t *testing.T) {
	planResolver := rosterExcluding(t,
		modelSet("claude-opus-5"),
		providerSet(providers.ProviderAnthropic, providers.ProviderAnthropicGateway),
		"claude-opus-5",
	)
	gatewayRequest := router.Request{
		EnabledProviders: providerSet(providers.ProviderAnthropicGateway),
		GatewayProviders: providerSet(providers.ProviderAnthropicGateway),
		CustomBindings:   map[string][]string{"claude-opus-5": {providers.ProviderAnthropicGateway}},
	}

	plan, err := planResolver.ResolveOriginal(gatewayRequest, originalTarget("claude-opus-5"))
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderAnthropicGateway, plan.SelectedBinding().Provider, "a gateway-only tenant must stay on its gateway")

	unaliased := gatewayRequest
	unaliased.CustomBindings = nil
	_, err = planResolver.ResolveOriginal(unaliased, originalTarget("claude-opus-5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)

	_, err = planResolver.ResolveOriginal(gatewayRequest, originalTarget("claude-haiku-4-5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
}

func TestResolveOriginalRejectsForeignOverrideSources(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
	)

	for _, source := range []policy.OverrideSource{
		policy.OverrideSourceRequest,
		policy.OverrideSourceSession,
		policy.OverrideSourceInstallation,
		policy.OverrideSourceDeployment,
	} {
		_, err := planResolver.ResolveOriginal(router.Request{}, policy.TargetOverride{Source: source, CatalogID: "claude-haiku-4-5"})
		assertResolutionErrorCode(t, err, policy.ResolutionErrorOverrideNotAllowed)
	}
	_, err := planResolver.ResolveOriginal(router.Request{}, policy.TargetOverride{Source: policy.OverrideSourcePolicyDefault, CatalogID: "claude-haiku-4-5"})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)

	plan, err := planResolver.ResolveOriginal(router.Request{}, policy.TargetOverride{CatalogID: "claude-haiku-4-5"})
	require.NoError(t, err, "an unsourced target defaults to the client-authoritative source")
	assert.Equal(t, policy.OverrideSourceClientAuthoritative, plan.Provenance().OverrideSource)
}

func TestResolveOriginalRejectsConflictingForceModel(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
	)

	_, err := planResolver.ResolveOriginal(router.Request{ForceModel: "gpt-5.6-luna"}, originalTarget("claude-haiku-4-5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)

	plan, err := planResolver.ResolveOriginal(router.Request{ForceModel: "claude-haiku-4-5"}, originalTarget("claude-haiku-4-5"))
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", plan.SelectedBinding().CatalogID)
}

func TestResolveOriginalRejectsOversizedContext(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
	)

	oversized := router.Request{EstimatedInputTokens: catalog.ContextWindowFor("claude-haiku-4-5") + 1}
	_, err := planResolver.ResolveOriginal(oversized, originalTarget("claude-haiku-4-5"))
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)

	oversized.EstimatedInputTokens = catalog.ContextWindowFor("claude-fable-5") + 1
	_, err = planResolver.ResolveOriginal(oversized, originalTarget("claude-fable-5"))
	// The roster-free path enforces the same context window.
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
}

func TestResolveOriginalCarriesEffortAndInvalidEffortFails(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
	)

	plan, err := planResolver.ResolveOriginal(router.Request{}, policy.TargetOverride{
		Source:    policy.OverrideSourceClientAuthoritative,
		CatalogID: "claude-haiku-4-5",
		Effort:    "high",
	})
	require.NoError(t, err)
	assert.Equal(t, "high", plan.SelectedTarget().Effort)

	_, err = planResolver.ResolveOriginal(router.Request{}, policy.TargetOverride{
		Source:    policy.OverrideSourceClientAuthoritative,
		CatalogID: "claude-haiku-4-5",
		Effort:    "not-an-effort",
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestResolveOriginalPlanSatisfiesExecutorContract(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
	)
	plan, err := planResolver.ResolveOriginal(router.Request{}, originalTarget("claude-haiku-4-5"))
	require.NoError(t, err)

	var executorPlan inference.ResolvedPlan = plan
	assert.Equal(t, inference.PurposeOriginalModelFallback, executorPlan.Purpose())
	assert.Empty(t, executorPlan.AlternativeTargets())
}
