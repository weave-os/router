package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inferenceOrganizationAllowlistEnv = "ROUTER_INFERENCE_ORGANIZATION_ALLOWLIST"

func TestWithAuthInferenceOrganizationAllowlist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []struct {
		name       string
		allowlist  string
		externalID string
		wantStatus int
	}{
		{name: "unset admits every organization", externalID: "org-other", wantStatus: http.StatusNoContent},
		{name: "listed organization is admitted", allowlist: " org-other , org-allowed ", externalID: "org-allowed", wantStatus: http.StatusNoContent},
		{name: "unlisted organization is rejected", allowlist: "org-allowed", externalID: "org-other", wantStatus: http.StatusForbidden},
		{name: "installation without organization is rejected", allowlist: "org-allowed", wantStatus: http.StatusForbidden},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv(inferenceOrganizationAllowlistEnv, scenario.allowlist)
			const routerToken = "rk_allowlist_probe"
			hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
			repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{hash: {
				apiKey:       &auth.APIKey{ID: "key-1", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix},
				installation: &auth.Installation{ID: "inst-1", ExternalID: scenario.externalID},
			}}}
			svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)

			handlerReached := false
			engine := gin.New()
			engine.Use(middleware.WithAuth(svc, false))
			engine.POST("/v1/messages", func(c *gin.Context) {
				handlerReached = true
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			request.Header.Set("Authorization", "Bearer "+routerToken)
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)

			require.Equal(t, scenario.wantStatus, response.Code)
			assert.Equal(t, scenario.wantStatus == http.StatusNoContent, handlerReached)
			if scenario.wantStatus == http.StatusForbidden {
				assert.JSONEq(t, `{"error":"organization_not_allowed"}`, response.Body.String())
			}
		})
	}
}
