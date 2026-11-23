package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type depsKeyUsageRecorder struct {
	credentials.ExternalKeyUsageRecorder
}

func TestWithDepsAppliesTheCredentialsBrokerage(t *testing.T) {
	recorder := &depsKeyUsageRecorder{}
	observer := usage.NewObserver([]byte("synthetic-salt"), time.Minute, time.Now)
	plans := subscriptions.NewPlanResolver(subscriptionPlanReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) {
		return providers.SubscriptionPlanUnknown, nil
	}), time.Now)
	leaser := &scriptedSubscriptionLeaser{}
	passthrough := map[string]struct{}{providers.ProviderAnthropic: {}}
	excluded := map[string]struct{}{providers.ProviderGoogle: {}}
	svc := WithDeps(NewService(nil, dispatch.NewClients(nil), nil, nil, false, "", "", nil), Deps{
		BYOKOnly:                 true,
		DeploymentKeyedProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
		Credentials: &credentials.Brokerage{ExternalKeyUsage: recorder, UsageObserver: observer, ClaudePlans: plans, CodexExtraCredits: true,
			ManagedSubscriptions: leaser, PassthroughEligible: passthrough, ExcludedProvidersOverride: excluded, BYOKOnly: false},
	})

	assert.Same(t, recorder, svc.credentials.ExternalKeyUsage)
	assert.Same(t, observer, svc.credentials.UsageObserver)
	assert.Same(t, plans, svc.credentials.ClaudePlans)
	assert.True(t, svc.credentials.CodexExtraCredits)
	assert.Same(t, leaser, svc.credentials.ManagedSubscriptions)
	assert.Equal(t, passthrough, svc.credentials.PassthroughEligible)
	assert.Equal(t, excluded, svc.credentials.ExcludedProvidersOverride)
	assert.True(t, svc.credentials.BYOKOnly, "BYOK-only comes from Deps.BYOKOnly, not the brokerage")
	assert.Equal(t, map[string]struct{}{providers.ProviderOpenAI: {}}, svc.credentials.KeyedProviders)
	require.NotNil(t, svc.credentials.SubscriptionTransport, "proxy keeps its credential ports")
	require.NotNil(t, svc.credentials.CallerPassthrough)

	passthrough[providers.ProviderOpenAI] = struct{}{}
	excluded[providers.ProviderOpenAI] = struct{}{}
	assert.NotContains(t, svc.credentials.PassthroughEligible, providers.ProviderOpenAI, "WithDeps copies the passthrough set")
	assert.NotContains(t, svc.credentials.ExcludedProvidersOverride, providers.ProviderOpenAI, "WithDeps copies the exclusion set")
}

func TestWithDepsWithoutCredentialsKeepsTheNewServiceDefaults(t *testing.T) {
	svc := WithDeps(NewService(nil, dispatch.NewClients(nil), nil, nil, false, "", "", nil), Deps{})
	assert.Nil(t, svc.credentials.ExternalKeyUsage)
	assert.Nil(t, svc.credentials.UsageObserver)
	assert.Nil(t, svc.credentials.ClaudePlans)
	assert.False(t, svc.credentials.CodexExtraCredits)
	assert.Nil(t, svc.credentials.ManagedSubscriptions)
	assert.Nil(t, svc.credentials.PassthroughEligible)
	assert.Nil(t, svc.credentials.ExcludedProvidersOverride)
	assert.Equal(t, auth.Clock(nil).Now().Truncate(time.Hour), svc.credentials.Clock.Now().Truncate(time.Hour))
}
