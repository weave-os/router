package routingpolicy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/routingpolicy"
)

func planResolver(t *testing.T) *policy.PlanResolver {
	t.Helper()
	resolver, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		nil, nil, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)
	return resolver
}

func TestPolicyPlan_ResolvesRoutedDecisionThroughPlanResolver(t *testing.T) {
	const model = "deepseek/deepseek-v4-pro"
	entry, ok := catalog.ByID(model)
	require.True(t, ok)
	binding := entry.Providers[0]

	plan, err := routingpolicy.New(planResolver(t)).Plan(policy.RoutedResolutionRequest{
		Purpose:  policy.PurposeAnthropicMessages,
		Decision: router.Decision{Model: model, Provider: binding.Provider},
		Bindings: []catalog.ProviderBinding{binding},
	})

	require.NoError(t, err)
	assert.Equal(t, model, plan.SelectedBinding().CatalogID)
	assert.Equal(t, binding.Provider, plan.SelectedBinding().Provider)
}

func TestPolicyPlan_FailsClosedWithoutPlanResolver(t *testing.T) {
	_, err := routingpolicy.New(nil).Plan(policy.RoutedResolutionRequest{})

	assert.ErrorIs(t, err, routingpolicy.ErrNoPlanResolver)
}

func TestPolicyExcludedModelsOverride_CopiesAndClears(t *testing.T) {
	base := routingpolicy.New(nil)

	overridden := base.WithExcludedModelsOverride([]string{"gpt-5.5"})

	assert.Equal(t, map[string]struct{}{"gpt-5.5": {}}, overridden.ExcludedModelsOverride())
	assert.Nil(t, base.ExcludedModelsOverride(), "the override returns a copy")
	assert.Nil(t, overridden.WithExcludedModelsOverride(nil).ExcludedModelsOverride())
}
