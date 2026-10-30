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
	"weave-os/router/internal/server/middleware"
)

func TestFederationAdmissionFailureHonorsBYOKOptIn(t *testing.T) {
	for _, tc := range []struct {
		name             string
		managed, optedIn bool
		status           int
	}{
		{"managed opted in", true, true, http.StatusServiceUnavailable},
		{"managed opted out", true, false, http.StatusOK},
		{"self hosted", false, false, http.StatusServiceUnavailable},
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
			engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
			request := httptest.NewRequest(http.MethodGet, "/probe", nil)
			request.Header.Set(middleware.RouterKeyHeader, token)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, tc.status, recorder.Code)
			if tc.status == http.StatusServiceUnavailable {
				assert.Equal(t, "5", recorder.Header().Get("Retry-After"))
				assert.Contains(t, recorder.Body.String(), "anthropic_federation_unavailable")
			}
		})
	}
}
