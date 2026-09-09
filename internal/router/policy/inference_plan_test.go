package policy_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

func TestPlanResolverPreservesExistingSidecarBinding(t *testing.T) {
	candidateResolver := policy.NewResolver(
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
		func(model catalog.Model) string { return "roster/" + model.ID },
		policy.ProviderPolicy{},
	)
	routingRequest := router.Request{}
	candidates := candidateResolver.Resolve(routingRequest)
	require.NotEmpty(t, candidates.Candidates)
	selectedCandidate := candidates.Candidates[0]
	existingBinding, found := candidates.BindingForSelection(selectedCandidate.ArmID, selectedCandidate.RosterID)
	require.True(t, found)

	planResolver, err := policy.NewPlanResolver(policy.DefaultRegistry(), candidateResolver)
	require.NoError(t, err)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:       policy.PurposeAnthropicMessages,
		RouterRequest: routingRequest,
		Selection: policy.CandidateSelection{
			ArmID:    selectedCandidate.ArmID,
			RosterID: selectedCandidate.RosterID,
		},
	})
	require.NoError(t, err)

	assert.Equal(t, existingBinding, plan.SelectedBinding())
	assert.Equal(t, policy.DefaultRegistry().Revision(), plan.RegistryRevision())
	assert.Equal(t, policy.SelectionStrategyRouter, plan.Provenance().SelectionStrategy)
}

func TestPlanResolverAppliesTypedOverridePrecedence(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
	)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{
			{Source: policy.OverrideSourceInstallation, CatalogID: "gpt-5.6-luna"},
			{Source: policy.OverrideSourceRequest, CatalogID: "claude-haiku-4-5"},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "claude-haiku-4-5", plan.SelectedBinding().CatalogID)
	assert.Equal(t, policy.OverrideSourceRequest, plan.Provenance().OverrideSource)
}

func TestPlanResolverRejectsInvalidExplicitOverrideWithoutSubstitution(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{
			{Source: policy.OverrideSourceRequest, CatalogID: "not-a-catalog-model"},
			{Source: policy.OverrideSourceInstallation, CatalogID: "gpt-5.6-luna"},
		},
	})

	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestPlanResolverRejectsDisallowedAndDuplicateOverrideSources(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
	)

	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{{
			Source: policy.OverrideSourceClientAuthoritative, CatalogID: "claude-haiku-4-5",
		}},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorOverrideNotAllowed)

	_, err = planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{
			{Source: policy.OverrideSourceRequest, CatalogID: "claude-haiku-4-5"},
			{Source: policy.OverrideSourceRequest, CatalogID: "claude-haiku-4-5"},
		},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorDuplicateOverride)

	_, err = planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{{
			Source: policy.OverrideSourcePolicyDefault, CatalogID: "claude-haiku-4-5",
		}},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestPlanResolverRequiresTypedForceModelOverride(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:       policy.PurposeAnthropicMessages,
		RouterRequest: router.Request{ForceModel: "claude-haiku-4-5"},
		Selection:     policy.CandidateSelection{RosterID: "claude-haiku-4-5"},
	})

	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestPlanResolverRestrictsFixedPolicyToReviewedModels(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
	)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeHandoverSummary})
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", plan.SelectedBinding().CatalogID)

	_, err = planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeHandoverSummary,
		Overrides: []policy.TargetOverride{{
			Source: policy.OverrideSourceDeployment, CatalogID: "gpt-5.6-luna",
		}},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestPlanResolverDeclaresOnlyEligibleBindingFallbacks(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic, providers.ProviderAnthropicGateway),
	)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Selection: policy.CandidateSelection{
			RosterID: "claude-haiku-4-5",
		},
	})
	require.NoError(t, err)

	alternatives := plan.AlternativeBindings()
	require.Len(t, alternatives, 1)
	assert.Equal(t, providers.ProviderAnthropicGateway, alternatives[0].Provider)
	assert.Equal(t, plan.SelectedBinding().CatalogID, alternatives[0].CatalogID)
}

func TestPlanResolverResolvesDeclaredModelFallbacks(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeHandoverSummary)
	specs[index].Fallback = policy.FallbackSpec{
		Kind:         policy.FallbackKindPlanAlternatives,
		Alternatives: []string{"gpt-5.6-luna"},
	}
	registry, err := policy.NewRegistry(specs)
	require.NoError(t, err)
	candidateResolver := policy.NewResolver(
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
		func(model catalog.Model) string { return model.ID },
		policy.ProviderPolicy{},
	)
	planResolver, err := policy.NewPlanResolver(registry, candidateResolver)
	require.NoError(t, err)

	plan, err := planResolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeHandoverSummary})
	require.NoError(t, err)
	require.Len(t, plan.AlternativeBindings(), 1)
	assert.Equal(t, "gpt-5.6-luna", plan.AlternativeBindings()[0].CatalogID)
}

func TestPlanResolverReportsMissingFixedTargetAsIneligible(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeHandoverSummary)
	specs[index].Fallback = policy.FallbackSpec{
		Kind:         policy.FallbackKindPlanAlternatives,
		Alternatives: []string{"gpt-5.6-luna"},
	}
	registry, err := policy.NewRegistry(specs)
	require.NoError(t, err)
	candidateResolver := policy.NewResolver(
		modelSet("gpt-5.6-luna"),
		providerSet(providers.ProviderOpenAI),
		func(model catalog.Model) string { return model.ID },
		policy.ProviderPolicy{},
	)
	planResolver, err := policy.NewPlanResolver(registry, candidateResolver)
	require.NoError(t, err)

	_, err = planResolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeHandoverSummary})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
}

func TestPlanResolverEnforcesBudgetEnvelopeAndSpendCap(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:       policy.PurposeAnthropicMessages,
		RouterRequest: router.Request{EstimatedInputTokens: 1_000},
		Selection:     policy.CandidateSelection{RosterID: "claude-haiku-4-5"},
		Budget: &policy.BudgetOverride{
			Source:      policy.BudgetSourceRequest,
			MaxSpendUSD: 0.0001,
		},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorBudgetViolation)

	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeAnthropicMessages)
	specs[index].Budget.TimeoutMillis = 8_000
	registry, registryErr := policy.NewRegistry(specs)
	require.NoError(t, registryErr)
	candidateResolver := policy.NewResolver(
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
		func(model catalog.Model) string { return model.ID },
		policy.ProviderPolicy{},
	)
	boundedPlanResolver, resolverErr := policy.NewPlanResolver(registry, candidateResolver)
	require.NoError(t, resolverErr)
	_, err = boundedPlanResolver.Resolve(policy.ResolutionRequest{
		Purpose:   policy.PurposeAnthropicMessages,
		Selection: policy.CandidateSelection{RosterID: "claude-haiku-4-5"},
		Budget: &policy.BudgetOverride{
			Source:        policy.BudgetSourceRequest,
			TimeoutMillis: 9_000,
		},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorBudgetViolation)
}

func TestResolvedPlanReturnsImmutableCopies(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic, providers.ProviderAnthropicGateway),
	)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:   policy.PurposeAnthropicMessages,
		Selection: policy.CandidateSelection{RosterID: "claude-haiku-4-5"},
	})
	require.NoError(t, err)

	alternatives := plan.AlternativeBindings()
	alternatives[0].Provider = "changed"
	constraints := plan.HardConstraints()
	constraints[0] = policy.Constraint("changed")
	preferences := plan.SoftPreferences()
	preferences[0] = policy.SoftPreference("changed")

	assert.NotEqual(t, "changed", plan.AlternativeBindings()[0].Provider)
	assert.NotEqual(t, policy.Constraint("changed"), plan.HardConstraints()[0])
	assert.NotEqual(t, policy.SoftPreference("changed"), plan.SoftPreferences()[0])
}

func TestRegistryValidatesDeploymentTargets(t *testing.T) {
	config := validDeploymentPolicyConfig()
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config))

	config.TargetOverrides = config.TargetOverrides[:len(config.TargetOverrides)-1]
	err := policy.DefaultRegistry().ValidateDeployment(config)
	assert.ErrorContains(t, err, "requires a typed deployment target")

	config = validDeploymentPolicyConfig()
	config.TargetOverrides[0].Target.Provider = providers.ProviderOpenAI
	err = policy.DefaultRegistry().ValidateDeployment(config)
	assert.ErrorContains(t, err, "has no available catalog binding")

	config = validDeploymentPolicyConfig()
	config.TargetOverrides[0].Target.CatalogID = "claude-opus-4-0"
	err = policy.DefaultRegistry().ValidateDeployment(config)
	assert.ErrorContains(t, err, "is not a routable catalog model")
}

func TestPlanResolverDoesNotFallBackToRosterForStaleArm(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAnthropic),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Selection: policy.CandidateSelection{
			ArmID:    "stale-arm",
			RosterID: "claude-haiku-4-5",
		},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorUnknownSelection)
}

func TestPlanResolverIncludesEveryAlternativeBinding(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeHandoverSummary)
	specs[index].Fallback = policy.FallbackSpec{
		Kind:         policy.FallbackKindPlanAlternatives,
		Alternatives: []string{"claude-sonnet-4-5"},
	}
	registry, err := policy.NewRegistry(specs)
	require.NoError(t, err)
	candidateResolver := policy.NewResolver(
		modelSet("claude-haiku-4-5", "claude-sonnet-4-5"),
		providerSet(providers.ProviderAnthropic, providers.ProviderAnthropicGateway),
		func(model catalog.Model) string { return model.ID },
		policy.ProviderPolicy{},
	)
	planResolver, err := policy.NewPlanResolver(registry, candidateResolver)
	require.NoError(t, err)

	plan, err := planResolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeHandoverSummary})
	require.NoError(t, err)
	alternatives := plan.AlternativeBindings()
	require.Len(t, alternatives, 2)
	for _, alternative := range alternatives {
		assert.Equal(t, "claude-sonnet-4-5", alternative.CatalogID)
	}
	assert.NotEqual(t, alternatives[0].Provider, alternatives[1].Provider)
}

func newPlanResolver(t *testing.T, deployed, available map[string]struct{}) *policy.PlanResolver {
	t.Helper()
	candidateResolver := policy.NewResolver(
		deployed,
		available,
		func(model catalog.Model) string { return model.ID },
		policy.ProviderPolicy{},
	)
	planResolver, err := policy.NewPlanResolver(policy.DefaultRegistry(), candidateResolver)
	require.NoError(t, err)
	return planResolver
}

func validDeploymentPolicyConfig() policy.DeploymentPolicyConfig {
	targetOverrides := make([]policy.PurposeTargetOverride, 0, 4)
	for _, purpose := range []policy.Purpose{
		policy.PurposeTitleGeneration,
		policy.PurposeClassifier,
		policy.PurposeProbe,
		policy.PurposeSubAgentDispatch,
	} {
		targetOverrides = append(targetOverrides, policy.PurposeTargetOverride{
			Purpose: purpose,
			Target: policy.TargetOverride{
				Source:    policy.OverrideSourceDeployment,
				CatalogID: "claude-haiku-4-5",
				Provider:  providers.ProviderAnthropic,
			},
		})
	}
	return policy.DeploymentPolicyConfig{
		AvailableProviders: providerSet(providers.ProviderAnthropic, providers.ProviderOpenAI),
		TargetOverrides:    targetOverrides,
	}
}

func modelSet(models ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(models))
	for _, model := range models {
		set[model] = struct{}{}
	}
	return set
}

func providerSet(providerNames ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(providerNames))
	for _, providerName := range providerNames {
		set[providerName] = struct{}{}
	}
	return set
}

func assertResolutionErrorCode(t *testing.T, err error, expected policy.ResolutionErrorCode) {
	t.Helper()
	var resolutionErr *policy.ResolutionError
	require.True(t, errors.As(err, &resolutionErr), "expected ResolutionError, got %v", err)
	assert.Equal(t, expected, resolutionErr.Code)
}

func TestFixedCatalogTargetSetIncludesReviewedUntieredModels(t *testing.T) {
	t.Parallel()

	available := providerSet(providers.ProviderAnthropic)
	targets := policy.DefaultRegistry().FixedCatalogTargetSet(available)
	assert.Contains(t, targets, "claude-fable-5")
	assert.Contains(t, targets, "claude-opus-4-8")
	assert.NotContains(t, catalog.RoutingTargetSet(available), "claude-fable-5")

	assert.Empty(t, policy.DefaultRegistry().FixedCatalogTargetSet(providerSet(providers.ProviderOpenAI)))
}

func TestPlanResolverResolvesUntieredPrecompactionSummarizer(t *testing.T) {
	t.Parallel()

	available := providerSet(providers.ProviderAnthropic)
	deployed := policy.DefaultRegistry().FixedCatalogTargetSet(available)
	for id := range catalog.RoutingTargetSet(available) {
		deployed[id] = struct{}{}
	}
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		deployed, available, func(m catalog.Model) string { return m.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)

	for _, model := range []string{"claude-fable-5", "claude-opus-4-8"} {
		plan, err := plans.Resolve(policy.ResolutionRequest{
			Purpose:       policy.PurposePrecompactionSummary,
			RouterRequest: router.Request{AllowedModels: modelSet(model)},
			Overrides: []policy.TargetOverride{{
				Source:    policy.OverrideSourceSession,
				CatalogID: model,
				Provider:  providers.ProviderAnthropic,
			}},
		})
		require.NoError(t, err, model)
		assert.Equal(t, model, plan.SelectedTarget().CatalogID)
	}
}

func TestRegistryAcceptsUntieredFixedCatalogDeploymentTarget(t *testing.T) {
	t.Parallel()

	config := validDeploymentPolicyConfig()
	config.TargetOverrides = append(config.TargetOverrides, policy.PurposeTargetOverride{
		Purpose: policy.PurposePrecompactionSummary,
		Target: policy.TargetOverride{
			Source:    policy.OverrideSourceDeployment,
			CatalogID: "claude-fable-5",
			Provider:  providers.ProviderAnthropic,
		},
	})
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config))
}
