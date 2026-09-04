package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ctxWithKeys(keys ...*auth.ExternalAPIKey) context.Context {
	return context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, keys)
}

func gatewayKey(provider, baseURL string, models ...string) *auth.ExternalAPIKey {
	aliases := make(map[string]string, len(models))
	for _, m := range models {
		aliases[m] = m
	}
	return &auth.ExternalAPIKey{
		Provider:     provider,
		Plaintext:    []byte("token"),
		BaseURL:      baseURL,
		ModelAliases: aliases,
	}
}

// A direct vendor has catalog bindings as its source of truth, so its 404s
// must not be memoized as an endpoint capability.
func TestGatewayModelKey_OnlyGateways(t *testing.T) {
	assert.Empty(t, gatewayModelKey("https://api.x.ai", providers.ProviderXAI, "grok-4.6"))
	assert.Empty(t, gatewayModelKey("https://gw.example.com", providers.ProviderOpenAIGateway, ""))
	assert.Equal(t, "https://gw.example.com|grok-4.6",
		gatewayModelKey("https://gw.example.com/", providers.ProviderOpenAIGateway, "grok-4.6"),
		"trailing slashes must not split one endpoint into two memo entries")
	assert.Equal(t, providers.ProviderOpenAIGateway+"|grok-4.6",
		gatewayModelKey("", providers.ProviderOpenAIGateway, "grok-4.6"),
		"a deployment-keyed gateway has one endpoint per process, keyed by provider")
}

func TestGatewayUnservedModelsForRequest_EmptyWithoutGatewayKeys(t *testing.T) {
	s := &Service{}
	s.rememberGatewayLacksModel(context.Background(), providers.ProviderOpenAIGateway, "grok-4.6")

	assert.Nil(t, s.gatewayUnservedModelsForRequest(context.Background()))
	assert.Nil(t, s.gatewayUnservedModelsForRequest(ctxWithKeys(
		&auth.ExternalAPIKey{Provider: providers.ProviderXAI, Plaintext: []byte("t"),
			ModelAliases: map[string]string{"grok-4.6": "grok-4.6"}},
	)), "a vendor BYOK key is not gateway-exclusive")
}

func TestGatewayUnservedModelsForRequest_ExcludesRefusedAliasOnly(t *testing.T) {
	const endpoint = "https://cortex.example.com/api/v2/cortex"
	s := &Service{}
	s.unservedGatewayModels.Store(gatewayModelKey(endpoint, providers.ProviderOpenAIGateway, "grok-4.6"), struct{}{})

	got := s.gatewayUnservedModelsForRequest(ctxWithKeys(
		gatewayKey(providers.ProviderOpenAIGateway, endpoint, "grok-4.6", "claude-haiku-4-5"),
	))

	require.NotNil(t, got)
	assert.Contains(t, got, "grok-4.6")
	assert.NotContains(t, got, "claude-haiku-4-5")
}

// Another endpoint aliasing the same model may still serve it; excluding the
// model outright would strand routing on the one gateway that refused it.
func TestGatewayUnservedModelsForRequest_KeepsModelWhenAnotherGatewayServesIt(t *testing.T) {
	const refused = "https://cortex.example.com/api/v2/cortex"
	const other = "https://gw.example.com/v1"
	s := &Service{}
	s.unservedGatewayModels.Store(gatewayModelKey(refused, providers.ProviderOpenAIGateway, "grok-4.6"), struct{}{})

	got := s.gatewayUnservedModelsForRequest(ctxWithKeys(
		gatewayKey(providers.ProviderOpenAIGateway, refused, "grok-4.6"),
		gatewayKey(providers.ProviderAnthropicGateway, other, "grok-4.6"),
	))

	assert.NotContains(t, got, "grok-4.6")
}

func TestRememberGatewayLacksModel_MemoizesPerEndpointAndModel(t *testing.T) {
	const endpoint = "https://cortex.example.com/api/v2/cortex"
	s := &Service{}
	ctx := context.WithValue(context.Background(), CredentialsContextKey{},
		&Credentials{APIKey: []byte("k"), BaseURL: endpoint})

	s.rememberGatewayLacksModel(ctx, providers.ProviderOpenAIGateway, "grok-4.6")

	assert.True(t, s.gatewayLacksModel(gatewayModelKey(endpoint, providers.ProviderOpenAIGateway, "grok-4.6")))
	assert.False(t, s.gatewayLacksModel(gatewayModelKey(endpoint, providers.ProviderOpenAIGateway, "claude-haiku-4-5")))
	assert.False(t, s.gatewayLacksModel(gatewayModelKey("https://other.example.com", providers.ProviderOpenAIGateway, "grok-4.6")))
}

// An unknown alias key never reaches routing, so it must not be reported.
func TestGatewayUnservedModelsForRequest_IgnoresNonCatalogAliases(t *testing.T) {
	const endpoint = "https://cortex.example.com/api/v2/cortex"
	s := &Service{}
	s.unservedGatewayModels.Store(gatewayModelKey(endpoint, providers.ProviderOpenAIGateway, "not-a-catalog-model"), struct{}{})

	got := s.gatewayUnservedModelsForRequest(ctxWithKeys(
		gatewayKey(providers.ProviderOpenAIGateway, endpoint, "not-a-catalog-model"),
	))

	assert.Nil(t, got)
}
