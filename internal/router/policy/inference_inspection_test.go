package policy

import (
	"errors"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanProjection_BoundsAlternatives(t *testing.T) {
	alternatives := make([]Binding, 0, maxProjectedAlternatives+3)
	for index := 0; index < maxProjectedAlternatives+3; index++ {
		alternatives = append(alternatives, Binding{CatalogID: "claude-haiku-4-5", Provider: providers.ProviderAnthropic, BindingIndex: index})
	}
	plan := ResolvedPlan{
		purpose:             PurposeHandoverSummary,
		policyID:            "aux-handover-summary",
		selectedBinding:     Binding{CatalogID: "claude-haiku-4-5", Provider: providers.ProviderAnthropic},
		alternativeBindings: alternatives,
		provenance:          PlanProvenance{SelectionStrategy: SelectionStrategyFixedCatalog, OverrideSource: OverrideSourcePolicyDefault},
	}

	projection := plan.Projection()

	assert.Len(t, projection.Alternatives, maxProjectedAlternatives)
	assert.Equal(t, len(alternatives), projection.AlternativeCount)
	assert.Equal(t, providers.ProviderAnthropic, projection.SelectedTarget.Provider)
	assert.Equal(t, OverrideSourcePolicyDefault, projection.Provenance.OverrideSource)
}

func TestProjectResolutionError_OnlyProjectsTypedFailures(t *testing.T) {
	_, ok := ProjectResolutionError(errors.New("pool exhausted: dsn=postgres://user:secret@host"))
	assert.False(t, ok)

	projected, ok := ProjectResolutionError(resolutionError(ResolutionErrorUnknownPurpose, Purpose("nope"), "", "purpose is not registered"))
	require.True(t, ok)
	assert.Equal(t, ResolutionErrorUnknownPurpose, projected.Code)
	assert.Equal(t, Purpose("nope"), projected.Purpose)
}

func TestDeploymentProjection_OmitsCandidateDumpForRouterPolicies(t *testing.T) {
	available := map[string]struct{}{providers.ProviderAnthropic: {}}
	projection := DefaultRegistry().DeploymentProjection(DeploymentPolicyConfig{AvailableProviders: available})

	assert.Equal(t, []string{providers.ProviderAnthropic}, projection.AvailableProviders)
	for _, entry := range projection.Policies {
		if entry.SelectionStrategy == SelectionStrategyRouter {
			assert.Empty(t, entry.CandidateBindings, entry.PolicyID)
			assert.Equal(t, projection.RoutableModels, entry.RoutableModels, entry.PolicyID)
		}
	}
}

func TestInspect_RouterPurposeUsesDeploymentServingUniverse(t *testing.T) {
	available := map[string]struct{}{providers.ProviderOpenAI: {}}
	resolver, err := NewPlanResolver(DefaultRegistry(), NewResolver(
		catalog.RoutingTargetSet(available), available, func(model catalog.Model) string { return model.ID }, ProviderPolicy{}))
	require.NoError(t, err)
	request := InspectionRequest{Purpose: PurposeOpenAIResponses, Model: "gpt-5.6-luna-pro"}

	_, err = resolver.Inspect(request, DeploymentPolicyConfig{AvailableProviders: available})
	var resolution *ResolutionError
	require.ErrorAs(t, err, &resolution)
	assert.Equal(t, ResolutionErrorNoEligibleBinding, resolution.Code)

	served := catalog.HMMRoutingTargetSet(available)
	plan, err := resolver.Inspect(request, DeploymentPolicyConfig{AvailableProviders: available, RoutableModels: served})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.6-luna-pro", plan.SelectedBinding().CatalogID)

	projection := DefaultRegistry().DeploymentProjection(DeploymentPolicyConfig{AvailableProviders: available, RoutableModels: served})
	assert.Equal(t, len(served), projection.RoutableModels)
}
