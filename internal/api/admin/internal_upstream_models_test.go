package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"workweave/router/internal/api/admin"
	"workweave/router/internal/auth"
	"workweave/router/internal/providers"
	"workweave/router/internal/proxy"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func internalUpstreamModelsEngine(authSvc *auth.Service, proxySvc *proxy.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/internal/v1/provider-keys/models", admin.InternalListUpstreamModelsHandler(authSvc, proxySvc))
	return engine
}

func internalUpstreamModelsRequest(t *testing.T, installationID, keyID string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]string{"installation_id": installationID, "key_id": keyID})
	require.NoError(t, err)
	return httptest.NewRequest(http.MethodPost, "/internal/v1/provider-keys/models", bytes.NewReader(body))
}

func TestInternalListUpstreamModelsHandler_ListsWithMintedCredential(t *testing.T) {
	key := &auth.ExternalAPIKey{
		ID:             "ext-1",
		InstallationID: testInstallationID,
		Provider:       providers.ProviderOpenAIGateway,
		Plaintext:      []byte("sk-byok"),
		BaseURL:        "https://cortex.example/api/v2/cortex/v1",
	}
	lister := &modelListingClient{models: []string{"claude-fable-5"}}
	engine := internalUpstreamModelsEngine(
		upstreamModelsAuthService([]*auth.ExternalAPIKey{key}),
		upstreamModelsProxyService(map[string]providers.Client{providers.ProviderOpenAIGateway: lister}),
	)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, internalUpstreamModelsRequest(t, testInstallationID, "ext-1"))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Models []string `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []string{"claude-fable-5"}, body.Models)
	require.NotNil(t, lister.seenCreds)
	assert.Equal(t, key.BaseURL, lister.seenCreds.BaseURL)
}

func TestInternalListUpstreamModelsHandler_UnmintableCredentialIs502(t *testing.T) {
	key := &auth.ExternalAPIKey{
		ID:             "ext-1",
		InstallationID: testInstallationID,
		Provider:       providers.ProviderOpenAIGateway,
		AuthType:       auth.AuthTypeWIF,
		BaseURL:        "https://cortex.example/api/v2/cortex/v1",
	}
	engine := internalUpstreamModelsEngine(
		upstreamModelsAuthService([]*auth.ExternalAPIKey{key}),
		upstreamModelsProxyService(map[string]providers.Client{providers.ProviderOpenAIGateway: &modelListingClient{}}),
	)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, internalUpstreamModelsRequest(t, testInstallationID, "ext-1"))

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestInternalListUpstreamModelsHandler_KeyOfAnotherInstallationIs404(t *testing.T) {
	key := &auth.ExternalAPIKey{
		ID:             "ext-1",
		InstallationID: testInstallationID,
		Provider:       providers.ProviderOpenAIGateway,
		BaseURL:        "https://cortex.example/api/v2/cortex/v1",
	}
	engine := internalUpstreamModelsEngine(
		upstreamModelsAuthService([]*auth.ExternalAPIKey{key}),
		upstreamModelsProxyService(map[string]providers.Client{providers.ProviderOpenAIGateway: &modelListingClient{}}),
	)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, internalUpstreamModelsRequest(t, "other-installation", "ext-1"))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestInternalListUpstreamModelsHandler_MissingFieldsIs400(t *testing.T) {
	engine := internalUpstreamModelsEngine(
		upstreamModelsAuthService(nil),
		upstreamModelsProxyService(nil),
	)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, internalUpstreamModelsRequest(t, testInstallationID, ""))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
