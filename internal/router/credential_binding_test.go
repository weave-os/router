package router_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
)

func TestCredentialProvidersForModelHonorsModelAndEndpointScope(t *testing.T) {
	providerSet := map[string]struct{}{
		providers.ProviderCodex:  {},
		providers.ProviderOpenAI: {},
		"other":                  {},
	}
	bindings := []router.CredentialBinding{
		{
			Provider: providers.ProviderCodex,
			Source:   router.CredentialSourceCodexOAuth,
			Models:   map[string]struct{}{"gpt-5.6-terra": {}},
			Endpoints: map[router.TranslationEndpoint]struct{}{
				router.EndpointOpenAIResponses: {},
			},
		},
		{Provider: providers.ProviderOpenAI, Source: router.CredentialSourceDeploymentKey},
	}

	got := router.CredentialProvidersForModel(providerSet, "gpt-5.6-terra", router.EndpointOpenAIResponses, bindings)
	assert.Equal(t, map[string]struct{}{providers.ProviderCodex: {}, providers.ProviderOpenAI: {}, "other": {}}, got)

	got = router.CredentialProvidersForModel(providerSet, "gpt-5.4-nano", router.EndpointOpenAIChat, bindings)
	assert.Equal(t, map[string]struct{}{providers.ProviderOpenAI: {}, "other": {}}, got)
}

func TestCredentialSourceForReturnsFirstMatchingBinding(t *testing.T) {
	bindings := []router.CredentialBinding{
		{Provider: providers.ProviderOpenAI, Source: router.CredentialSourceCodexOAuth, Models: map[string]struct{}{"gpt-5.6-terra": {}}},
		{Provider: providers.ProviderOpenAI, Source: router.CredentialSourceDeploymentKey},
	}

	assert.Equal(t, router.CredentialSourceCodexOAuth,
		router.CredentialSourceFor(providers.ProviderOpenAI, "gpt-5.6-terra", router.EndpointOpenAIResponses, bindings))
	assert.Equal(t, router.CredentialSourceDeploymentKey,
		router.CredentialSourceFor(providers.ProviderOpenAI, "gpt-5.4-nano", router.EndpointOpenAIChat, bindings))
}
