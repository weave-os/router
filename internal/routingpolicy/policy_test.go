package routingpolicy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/routingpolicy"
)

type fakeBroker struct {
	eligibility credentials.Eligibility
	err         error
	got         []credentials.EligibilityRequest
}

func (b *fakeBroker) Eligibility(_ context.Context, req credentials.EligibilityRequest) (credentials.Eligibility, error) {
	b.got = append(b.got, req)
	return b.eligibility, b.err
}

func planResolver(t *testing.T) *policy.PlanResolver {
	t.Helper()
	resolver, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		nil, nil, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)
	return resolver
}

func TestPolicyEligibility_ReadsCredentialReachFromBroker(t *testing.T) {
	want := credentials.Eligibility{
		EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		CustomBindings:   map[string][]string{"claude-opus-4-8": {"byok-gateway"}},
	}
	broker := &fakeBroker{eligibility: want}
	req := credentials.EligibilityRequest{Surface: providers.ProviderAnthropic, InstallationID: uuid.New()}

	got, err := routingpolicy.New(broker, nil).Eligibility(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, []credentials.EligibilityRequest{req}, broker.got)
}

func TestPolicyEligibility_PropagatesBrokerError(t *testing.T) {
	brokerErr := errors.New("credits exhausted")

	_, err := routingpolicy.New(&fakeBroker{err: brokerErr}, nil).Eligibility(context.Background(), credentials.EligibilityRequest{})

	assert.ErrorIs(t, err, brokerErr)
}

func TestPolicyPlan_ResolvesRoutedDecisionThroughPlanResolver(t *testing.T) {
	const model = "deepseek/deepseek-v4-pro"
	entry, ok := catalog.ByID(model)
	require.True(t, ok)
	binding := entry.Providers[0]

	plan, err := routingpolicy.New(&fakeBroker{}, planResolver(t)).Plan(policy.RoutedResolutionRequest{
		Purpose:  policy.PurposeAnthropicMessages,
		Decision: router.Decision{Model: model, Provider: binding.Provider},
		Bindings: []catalog.ProviderBinding{binding},
	})

	require.NoError(t, err)
	assert.Equal(t, model, plan.SelectedBinding().CatalogID)
	assert.Equal(t, binding.Provider, plan.SelectedBinding().Provider)
}

func TestPolicyPlan_FailsClosedWithoutPlanResolver(t *testing.T) {
	_, err := routingpolicy.New(&fakeBroker{}, nil).Plan(policy.RoutedResolutionRequest{})

	assert.ErrorIs(t, err, routingpolicy.ErrNoPlanResolver)
}

func TestPolicyExcludedModelsOverride_CopiesAndClears(t *testing.T) {
	base := routingpolicy.New(&fakeBroker{}, nil)

	overridden := base.WithExcludedModelsOverride([]string{"gpt-5.5"})

	assert.Equal(t, map[string]struct{}{"gpt-5.5": {}}, overridden.ExcludedModelsOverride())
	assert.Nil(t, base.ExcludedModelsOverride(), "the override returns a copy")
	assert.Nil(t, overridden.WithExcludedModelsOverride(nil).ExcludedModelsOverride())
}
