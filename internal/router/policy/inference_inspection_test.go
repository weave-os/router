package policy

import (
	"errors"
	"testing"

	"weave-os/router/internal/providers"

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
