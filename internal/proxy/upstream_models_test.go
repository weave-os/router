package proxy_test

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listerClient is a providers.Client that also lists models, recording the
// credentials it saw so tests can assert they were placed in context.
type listerClient struct {
	models    []string
	seenCreds *proxy.Credentials
}

func (c *listerClient) Proxy(context.Context, router.Decision, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}
func (c *listerClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}
func (c *listerClient) ListModels(ctx context.Context) ([]string, error) {
	c.seenCreds = proxy.CredentialsFromContext(ctx)
	return c.models, nil
}

// nonListerClient has no model-listing surface.
type nonListerClient struct{}

func (nonListerClient) Proxy(context.Context, router.Decision, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}
func (nonListerClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func upstreamModelsService(providerMap map[string]providers.Client) *proxy.Service {
	return proxy.NewService(nil, providerMap, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
}

func TestListUpstreamModels_PassesCredentialsToLister(t *testing.T) {
	lister := &listerClient{models: []string{"cortex-a", "cortex-b"}}
	svc := upstreamModelsService(map[string]providers.Client{providers.ProviderOpenAIGateway: lister})

	creds := &proxy.Credentials{APIKey: []byte("byok"), BaseURL: "https://cortex.example/api/v2/cortex/v1"}
	models, err := svc.ListUpstreamModels(context.Background(), providers.ProviderOpenAIGateway, creds)
	require.NoError(t, err)
	assert.Equal(t, []string{"cortex-a", "cortex-b"}, models)
	require.NotNil(t, lister.seenCreds, "the BYOK credentials must reach the adapter via context")
	assert.Equal(t, "byok", string(lister.seenCreds.APIKey))
}

func TestListUpstreamModels_UnsupportedProvider(t *testing.T) {
	svc := upstreamModelsService(map[string]providers.Client{providers.ProviderGoogle: nonListerClient{}})

	_, err := svc.ListUpstreamModels(context.Background(), providers.ProviderGoogle, nil)
	assert.ErrorIs(t, err, proxy.ErrModelListingUnsupported)
}

func TestListUpstreamModels_UnknownProvider(t *testing.T) {
	svc := upstreamModelsService(nil)

	_, err := svc.ListUpstreamModels(context.Background(), providers.ProviderOpenAIGateway, nil)
	assert.ErrorIs(t, err, proxy.ErrProviderNotConfigured)
}
