package middleware

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
)

func noopAttribution(context.Context, string, policyregistry.ServingAssertion) error { return nil }

func TestServingAdmissionRecordsWorkerRecipe(t *testing.T) {
	cfg, admitted, _, _ := admissionMiddlewareFixture(t)
	cfg.Attribution = admissionAttributionFunc(noopAttribution)
	response := runAdmissionMiddleware(t, cfg, admitted, func(c *gin.Context) {
		identity, managed := requestcontext.ServingIdentityFromContext(c.Request.Context())
		require.True(t, managed)
		snapshot := policyregistry.ServingSnapshotFromContext(c.Request.Context())
		require.NotNil(t, snapshot)
		assert.Equal(t, requestcontext.ServingRecipe{
			WorkerRevision:            cfg.Identity.Revision,
			WorkerImageDigest:         cfg.Identity.ImageDigest,
			WorkerConfigurationSHA256: cfg.Identity.Configuration.SHA256,
			PolicySHA256:              snapshot.Release.Policy.SHA256,
		}, identity.Recipe)
		c.Status(http.StatusAccepted)
	})
	assert.Equal(t, http.StatusAccepted, response.Code)
}

func TestServingAdmissionNeverStampsReleaseHeadersOnModelResponses(t *testing.T) {
	optIn := func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestcontext.InstallationServingReleaseHeadersContextKey{}, true))
	}
	for name, handler := range map[string]gin.HandlerFunc{
		"successful model response": func(c *gin.Context) { c.String(http.StatusOK, "ok") },
		"refusal":                   func(c *gin.Context) { c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "refused"}) },
	} {
		t.Run(name, func(t *testing.T) {
			cfg, admitted, _, _ := admissionMiddlewareFixture(t)
			cfg.Attribution = admissionAttributionFunc(noopAttribution)
			response := runAdmissionMiddlewareWithPrelude(t, cfg, admitted, optIn, handler)
			assert.Empty(t, response.Header().Get(requestcontext.HeaderServingReleaseSHA), "only /v1/display-settings names the release")
			assert.Empty(t, response.Header().Get(requestcontext.HeaderServingPolicySHA), "only /v1/display-settings names the policy")
		})
	}
}
