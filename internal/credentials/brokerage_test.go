package credentials_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

func set(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func apiKeyHeaders(key string) http.Header {
	h := http.Header{}
	h.Set("x-api-key", key)
	return h
}

func TestEligibility_NilKeyedProvidersTreatsRegisteredAsDeploymentKeyed(t *testing.T) {
	got := credentials.Brokerage{}.Eligibility(context.Background(), credentials.EligibilityRequest{
		Registered: set(providers.ProviderAnthropic, providers.ProviderOpenAI),
	})
	assert.Equal(t, set(providers.ProviderAnthropic, providers.ProviderOpenAI), got.EnabledProviders)
	assert.False(t, got.UsesNonDeploymentCreds)
}

func TestEligibility_BYOKOnlyEnrollsOnlyUsableOrFailedKeys(t *testing.T) {
	b := credentials.Brokerage{BYOKOnly: true, KeyedProviders: set(providers.ProviderOpenAI)}
	got := b.Eligibility(context.Background(), credentials.EligibilityRequest{
		Registered: set(providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderGoogle),
		ExternalKeys: []*auth.ExternalAPIKey{
			{Provider: providers.ProviderAnthropic, Plaintext: []byte("byok")},
			{Provider: providers.ProviderGoogle, CredentialError: errors.New("decrypt failed")},
			{Provider: providers.ProviderOpenAI},
		},
	})
	assert.Equal(t, set(providers.ProviderAnthropic, providers.ProviderGoogle), got.EnabledProviders)
	assert.True(t, got.UsesNonDeploymentCreds)
}

func TestEligibility_ClientKeyEnablesOnlyItsSurface(t *testing.T) {
	b := credentials.Brokerage{KeyedProviders: set()}
	req := credentials.EligibilityRequest{SurfaceProvider: providers.ProviderAnthropic, Headers: apiKeyHeaders("sk-ant-api-client")}

	got := b.Eligibility(context.Background(), req)
	assert.Equal(t, set(providers.ProviderAnthropic), got.EnabledProviders)
	assert.True(t, got.UsesNonDeploymentCreds)

	req.RouterKeyAuthed = true
	assert.Empty(t, b.Eligibility(context.Background(), req).EnabledProviders,
		"the inbound key is the router key on router-keyed requests")
}

func TestEligibility_PassthroughIsSurfaceScoped(t *testing.T) {
	b := credentials.Brokerage{KeyedProviders: set(), PassthroughEligible: set(providers.ProviderOpenAI)}
	got := b.Eligibility(context.Background(), credentials.EligibilityRequest{SurfaceProvider: providers.ProviderAnthropic})
	assert.Empty(t, got.EnabledProviders)

	got = b.Eligibility(context.Background(), credentials.EligibilityRequest{SurfaceProvider: providers.ProviderOpenAI})
	assert.Equal(t, set(providers.ProviderOpenAI), got.EnabledProviders)

	got = b.Eligibility(context.Background(), credentials.EligibilityRequest{SurfaceProvider: providers.ProviderOpenAI, RouterKeyAuthed: true})
	assert.Empty(t, got.EnabledProviders)
}

func TestEligibility_ExclusionsBeatSubscriptionLanes(t *testing.T) {
	b := credentials.Brokerage{KeyedProviders: set(providers.ProviderOpenAI)}
	got := b.Eligibility(context.Background(), credentials.EligibilityRequest{
		SubscriptionLanes:             set(providers.ProviderAnthropic),
		InstallationExcludedProviders: []string{providers.ProviderOpenAI},
		SessionDisabledProviders:      []string{providers.ProviderGoogle},
	})
	assert.Equal(t, set(providers.ProviderAnthropic), got.EnabledProviders)
	assert.Equal(t, set(providers.ProviderOpenAI, providers.ProviderGoogle), got.ExcludedProviders)
}

func TestEligibility_GatewayDisplacesEveryOtherProvider(t *testing.T) {
	b := credentials.Brokerage{KeyedProviders: set(providers.ProviderAnthropic)}
	got := b.Eligibility(context.Background(), credentials.EligibilityRequest{
		ExternalKeys: []*auth.ExternalAPIKey{{
			Provider: providers.ProviderOpenAIGateway, Plaintext: []byte("gw"),
			ModelAliases: map[string]string{"gpt-5": "gpt-5"},
		}},
		InstallationExcludedProviders: []string{providers.ProviderOpenAIGateway},
	})
	assert.Equal(t, set(providers.ProviderOpenAIGateway), got.EnabledProviders)
	assert.Equal(t, set(providers.ProviderOpenAIGateway), got.GatewayProviders)
	assert.Equal(t, map[string][]string{"gpt-5": {providers.ProviderOpenAIGateway}}, got.CustomBindings)
}

func TestPolicyExcludedProviders_OverrideReplacesInstallationList(t *testing.T) {
	b := credentials.Brokerage{ExcludedProvidersOverride: set(providers.ProviderGoogle)}
	assert.Equal(t, set(providers.ProviderGoogle), b.PolicyExcludedProviders([]string{providers.ProviderOpenAI}))
	assert.Equal(t, set(providers.ProviderGoogle, providers.ProviderOpenAI),
		b.ExcludedProviders([]string{providers.ProviderAnthropic}, []string{providers.ProviderOpenAI}))
	assert.Nil(t, credentials.Brokerage{}.PolicyExcludedProviders(nil))
}

func TestEnabledProviders_MatchesEligibility(t *testing.T) {
	b := credentials.Brokerage{KeyedProviders: set(providers.ProviderOpenAI), ExcludedProvidersOverride: set(providers.ProviderOpenAI)}
	req := credentials.EligibilityRequest{
		Registered:        set(providers.ProviderAnthropic, providers.ProviderOpenAI),
		SubscriptionLanes: set(providers.ProviderAnthropic),
	}
	assert.Equal(t, b.Eligibility(context.Background(), req).EnabledProviders, b.EnabledProviders(req))
}
