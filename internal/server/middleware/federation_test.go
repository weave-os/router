package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server/middleware"
)

func TestFederationAdmissionFailureHonorsBYOKOptIn(t *testing.T) {
	for _, tc := range []struct {
		name             string
		managed, optedIn bool
		status           int
	}{
		{"managed opted in", true, true, http.StatusOK},
		{"managed opted out", true, false, http.StatusOK},
		{"self hosted", false, false, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "rk_synthetic_federation_admission"
			hash := auth.HashAPIKeySHA256(token)
			installation := &auth.Installation{ID: "inst-federation", ExternalID: "org-federation", ByokEnabled: tc.optedIn}
			apiKey := &auth.APIKey{ID: "key-federation", InstallationID: installation.ID, KeyHash: hash}
			keys := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{hash: {apiKey: apiKey, installation: installation}}}
			external := &fakeExternalAPIKeyRepository{byInstallationID: map[string][]*auth.ExternalAPIKey{installation.ID: {{ID: "ext-federation", InstallationID: installation.ID, Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF}}}}
			svc := auth.NewService(fakeInstallationRepository{}, keys, external, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)
			engine := gin.New()
			engine.Use(middleware.WithAuth(svc, tc.managed))
			engine.GET("/probe", func(c *gin.Context) {
				resolved, _ := c.Request.Context().Value(proxy.ExternalAPIKeysContextKey{}).([]*auth.ExternalAPIKey)
				if tc.managed && !tc.optedIn {
					assert.Empty(t, resolved)
				} else {
					require.Len(t, resolved, 1)
					assert.ErrorIs(t, resolved[0].CredentialError, auth.ErrAnthropicFederationUnavailable)
				}
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodGet, "/probe", nil)
			request.Header.Set(middleware.RouterKeyHeader, token)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, tc.status, recorder.Code)
		})
	}
}

func TestFederationFailureDoesNotBlockOtherProviders(t *testing.T) {
	const token = "rk_synthetic_federation_isolation"
	hash := auth.HashAPIKeySHA256(token)
	installation := &auth.Installation{ID: "inst-isolation", ExternalID: "org-isolation", ByokEnabled: true}
	apiKey := &auth.APIKey{ID: "key-isolation", InstallationID: installation.ID, KeyHash: hash}
	keys := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{hash: {apiKey: apiKey, installation: installation}}}
	external := &fakeExternalAPIKeyRepository{byInstallationID: map[string][]*auth.ExternalAPIKey{installation.ID: {
		{ID: "ext-federation", InstallationID: installation.ID, Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF},
		{ID: "ext-openai", InstallationID: installation.ID, Provider: providers.ProviderOpenAI, Plaintext: []byte("synthetic-openai-key")},
	}}}
	svc := auth.NewService(fakeInstallationRepository{}, keys, external, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)
	engine := gin.New()
	engine.Use(middleware.WithAuth(svc, true))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	request := httptest.NewRequest(http.MethodGet, "/probe", nil)
	request.Header.Set(middleware.RouterKeyHeader, token)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, "an Anthropic outage must not prevent an unrelated provider from being selected")
}
