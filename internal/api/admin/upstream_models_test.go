package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modelListingClient is a providers.Client whose upstream publishes a model
// list; it records the credentials it received.
type modelListingClient struct {
	models    []string
	err       error
	seenCreds *proxy.Credentials
}

func (c *modelListingClient) Proxy(context.Context, router.Decision, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}
func (c *modelListingClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}
func (c *modelListingClient) ListModels(ctx context.Context) ([]string, error) {
	c.seenCreds = proxy.CredentialsFromContext(ctx)
	return c.models, c.err
}

// plainClient has no model-listing surface.
type plainClient struct{}

func (plainClient) Proxy(context.Context, router.Decision, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}
func (plainClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func upstreamModelsEngine(authSvc *auth.Service, proxySvc *proxy.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/admin/v1/provider-keys/:id/models", func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: testInstallationID})
	}, admin.ListUpstreamModelsHandler(authSvc, proxySvc))
	return engine
}

func upstreamModelsAuthService(keys []*auth.ExternalAPIKey) *auth.Service {
	repo := &fakeExternalAPIKeyRepo{keys: keys}
	return auth.NewService(nil, nil, repo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) })
}

func upstreamModelsProxyService(providerMap map[string]providers.Client) *proxy.Service {
	return proxy.NewService(nil, providerMap, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
}

func TestListUpstreamModelsHandler_ReturnsEndpointModels(t *testing.T) {
	key := &auth.ExternalAPIKey{
		ID:             "ext-1",
		InstallationID: testInstallationID,
		Provider:       providers.ProviderOpenAIGateway,
		Plaintext:      []byte("sk-byok"),
		BaseURL:        "https://cortex.example/api/v2/cortex/v1",
	}
	lister := &modelListingClient{models: []string{"claude-fable-5", "snowflake-llama-70b"}}
	engine := upstreamModelsEngine(
		upstreamModelsAuthService([]*auth.ExternalAPIKey{key}),
		upstreamModelsProxyService(map[string]providers.Client{providers.ProviderOpenAIGateway: lister}),
	)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/provider-keys/ext-1/models", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Models []string `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []string{"claude-fable-5", "snowflake-llama-70b"}, body.Models)
	require.NotNil(t, lister.seenCreds, "the stored key's credential must reach the adapter")
	assert.Equal(t, "sk-byok", string(lister.seenCreds.APIKey))
	assert.Equal(t, key.BaseURL, lister.seenCreds.BaseURL)
}

func TestListUpstreamModelsHandler_UnknownKeyIs404(t *testing.T) {
	engine := upstreamModelsEngine(
		upstreamModelsAuthService(nil),
		upstreamModelsProxyService(map[string]providers.Client{providers.ProviderOpenAIGateway: &modelListingClient{}}),
	)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/provider-keys/missing/models", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListUpstreamModelsHandler_UnsupportedProviderIs501(t *testing.T) {
	key := &auth.ExternalAPIKey{
		ID:             "ext-1",
		InstallationID: testInstallationID,
		Provider:       providers.ProviderGoogle,
		Plaintext:      []byte("sk-byok"),
	}
	engine := upstreamModelsEngine(
		upstreamModelsAuthService([]*auth.ExternalAPIKey{key}),
		upstreamModelsProxyService(map[string]providers.Client{providers.ProviderGoogle: plainClient{}}),
	)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/provider-keys/ext-1/models", nil))

	assert.Equal(t, http.StatusNotImplemented, rec.Code,
		"a provider with no model-listing surface must tell the dashboard to keep manual entry")
}

func discoverModelsEngine(proxySvc *proxy.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/admin/v1/provider-keys/discover-models", admin.DiscoverModelsHandler(proxySvc))
	return engine
}

func TestDiscoverModelsHandler_UsesBodyCredentials(t *testing.T) {
	lister := &modelListingClient{models: []string{"cortex-a"}}
	engine := discoverModelsEngine(
		upstreamModelsProxyService(map[string]providers.Client{providers.ProviderOpenAIGateway: lister}),
	)

	body, _ := json.Marshal(map[string]string{
		"provider": providers.ProviderOpenAIGateway,
		"key":      "sk-unsaved",
		"base_url": "https://cortex.example/api/v2/cortex/v1/",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/provider-keys/discover-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, lister.seenCreds)
	assert.Equal(t, "sk-unsaved", string(lister.seenCreds.APIKey))
	assert.Equal(t, "https://cortex.example/api/v2/cortex/v1", lister.seenCreds.BaseURL,
		"the trailing slash must be trimmed like the save path does")
}

func TestDiscoverModelsHandler_RejectsMissingKey(t *testing.T) {
	engine := discoverModelsEngine(
		upstreamModelsProxyService(map[string]providers.Client{providers.ProviderOpenAIGateway: &modelListingClient{}}),
	)

	body, _ := json.Marshal(map[string]string{"provider": providers.ProviderOpenAIGateway})
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/provider-keys/discover-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListUpstreamModelsHandler_EndpointFailureIs502(t *testing.T) {
	key := &auth.ExternalAPIKey{
		ID:             "ext-1",
		InstallationID: testInstallationID,
		Provider:       providers.ProviderOpenAIGateway,
		Plaintext:      []byte("sk-byok"),
		BaseURL:        "https://cortex.example/api/v2/cortex/v1",
	}
	lister := &modelListingClient{err: errors.New("model listing returned status 401")}
	engine := upstreamModelsEngine(
		upstreamModelsAuthService([]*auth.ExternalAPIKey{key}),
		upstreamModelsProxyService(map[string]providers.Client{providers.ProviderOpenAIGateway: lister}),
	)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/provider-keys/ext-1/models", nil))

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}
