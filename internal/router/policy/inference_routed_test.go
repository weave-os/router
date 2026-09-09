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

func routedResolver(t *testing.T) *policy.PlanResolver {
	t.Helper()
	resolver, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		nil, nil, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)
	return resolver
}

func TestResolveRouted_AuthorizesBindingWalkInOrder(t *testing.T) {
	const model = "deepseek/deepseek-v4-pro"
	entry, ok := catalog.ByID(model)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(entry.Providers), 2)
	primary, secondary := entry.Providers[0], entry.Providers[1]

	plan, err := routedResolver(t).ResolveRouted(policy.RoutedResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Decision: router.Decision{
			Model: model, Provider: primary.Provider,
			Metadata: &router.RoutingMetadata{SelectedArmID: "arm-1", SelectedRosterArmID: "arm-1:high"},
		},
		Bindings: []catalog.ProviderBinding{{Provider: primary.Provider}, secondary},
	})
	require.NoError(t, err)

	assert.Equal(t, policy.PurposeAnthropicMessages, plan.Purpose())
	assert.Equal(t, policy.PolicyID("main-anthropic-messages"), plan.PolicyID())
	spec, found := policy.DefaultRegistry().Spec(policy.PurposeAnthropicMessages)
	require.True(t, found)
	assert.Equal(t, policy.MigrationStatusExecutor, spec.MigrationStatus)
	selected := plan.SelectedBinding()
	assert.Equal(t, model, selected.CatalogID)
	assert.Equal(t, primary.Provider, selected.Provider)
	assert.Equal(t, primary.UpstreamID, selected.UpstreamID, "provider-only binding inherits the catalog upstream id")
	assert.Equal(t, 0, selected.BindingIndex)
	assert.Equal(t, "arm-1", selected.ArmID)
	alternatives := plan.AlternativeBindings()
	require.Len(t, alternatives, 1)
	assert.Equal(t, secondary.Provider, alternatives[0].Provider)
	assert.Equal(t, 1, alternatives[0].BindingIndex)
	assert.Equal(t, policy.SelectionStrategyRouter, plan.Provenance().SelectionStrategy)
	assert.Equal(t, "arm-1:high", plan.Provenance().RosterID)
	assert.Empty(t, plan.Provenance().OverrideSource)
}

func TestResolveRouted_RecordsCallerOrigin(t *testing.T) {
	plan, err := routedResolver(t).ResolveRouted(policy.RoutedResolutionRequest{
		Purpose:  policy.PurposeAnthropicMessages,
		Decision: router.Decision{Model: "claude-haiku-4-5", Provider: providers.ProviderAnthropic},
		Bindings: []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
		Origin:   policy.OverrideSourceRequest,
	})
	require.NoError(t, err)
	assert.Equal(t, policy.OverrideSourceRequest, plan.Provenance().OverrideSource)
	assert.Empty(t, plan.AlternativeBindings())
}

func TestResolveRouted_FailsClosed(t *testing.T) {
	resolver := routedResolver(t)
	valid := policy.RoutedResolutionRequest{
		Purpose:  policy.PurposeAnthropicMessages,
		Decision: router.Decision{Model: "claude-haiku-4-5", Provider: providers.ProviderAnthropic},
		Bindings: []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
	}
	cases := map[string]struct {
		mutate func(*policy.RoutedResolutionRequest)
		code   policy.ResolutionErrorCode
	}{
		"unknown purpose":     {func(r *policy.RoutedResolutionRequest) { r.Purpose = "nope" }, policy.ResolutionErrorUnknownPurpose},
		"auxiliary purpose":   {func(r *policy.RoutedResolutionRequest) { r.Purpose = policy.PurposeHandoverSummary }, policy.ResolutionErrorUnsupportedPurpose},
		"no model":            {func(r *policy.RoutedResolutionRequest) { r.Decision.Model = "" }, policy.ResolutionErrorMissingSelection},
		"no bindings":         {func(r *policy.RoutedResolutionRequest) { r.Bindings = nil }, policy.ResolutionErrorNoEligibleBinding},
		"unnamed provider":    {func(r *policy.RoutedResolutionRequest) { r.Bindings = []catalog.ProviderBinding{{}} }, policy.ResolutionErrorNoEligibleBinding},
		"policy default":      {func(r *policy.RoutedResolutionRequest) { r.Origin = policy.OverrideSourcePolicyDefault }, policy.ResolutionErrorInvalidOverride},
		"unknown origin":      {func(r *policy.RoutedResolutionRequest) { r.Origin = "vibes" }, policy.ResolutionErrorInvalidOverride},
		"undeclared override": {func(r *policy.RoutedResolutionRequest) { r.Origin = policy.OverrideSourceClientAuthoritative }, policy.ResolutionErrorOverrideNotAllowed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			request := valid
			tc.mutate(&request)
			_, err := resolver.ResolveRouted(request)
			var resolution *policy.ResolutionError
			require.True(t, errors.As(err, &resolution), "got %v", err)
			assert.Equal(t, tc.code, resolution.Code)
		})
	}
}
