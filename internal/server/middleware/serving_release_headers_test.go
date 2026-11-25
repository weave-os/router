package middleware

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
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

func TestServingAdmissionReleaseHeadersRequireOptIn(t *testing.T) {
	tests := map[string]struct {
		optIn       bool
		hideSurface bool
		wantHeaders bool
	}{
		"opted in installation names its release and policy": {optIn: true, wantHeaders: true},
		"default installation has no release headers":        {},
		"hidden terminal surfaces hide release headers":      {optIn: true, hideSurface: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, admitted, _, _ := admissionMiddlewareFixture(t)
			cfg.Attribution = admissionAttributionFunc(noopAttribution)
			markInstallation := func(c *gin.Context) {
				ctx := c.Request.Context()
				if tc.optIn {
					ctx = context.WithValue(ctx, requestcontext.InstallationServingReleaseHeadersContextKey{}, true)
				}
				if tc.hideSurface {
					ctx = context.WithValue(ctx, requestcontext.InstallationHideTerminalSurfacesContextKey{}, true)
				}
				c.Request = c.Request.WithContext(ctx)
			}
			response := runAdmissionMiddlewareWithPrelude(t, cfg, admitted, markInstallation, func(c *gin.Context) {
				c.Status(http.StatusAccepted)
			})
			require.Equal(t, http.StatusAccepted, response.Code)
			release := response.Header().Get(requestcontext.HeaderServingReleaseSHA)
			policySHA := response.Header().Get(requestcontext.HeaderServingPolicySHA)
			if !tc.wantHeaders {
				assert.Empty(t, release)
				assert.Empty(t, policySHA)
				return
			}
			assert.Equal(t, admitted.Admission.Selection.Release.SHA256[:12], release)
			assert.Len(t, policySHA, 12)
		})
	}
}

func TestServingAdmissionReleaseHeadersFollowTheFinalResponse(t *testing.T) {
	optIn := func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestcontext.InstallationServingReleaseHeadersContextKey{}, true))
	}
	tests := map[string]struct {
		handler     gin.HandlerFunc
		wantHeaders bool
	}{
		"successful response names its release": {
			handler:     func(c *gin.Context) { c.String(http.StatusOK, "ok") },
			wantHeaders: true,
		},
		"refusal never names a release": {
			handler: func(c *gin.Context) { c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "refused"}) },
		},
		"handler that marks terminal surfaces hidden hides the release": {
			handler: func(c *gin.Context) {
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestcontext.InstallationHideTerminalSurfacesContextKey{}, true))
				c.JSON(http.StatusOK, gin.H{"hide_terminal_surfaces": true})
			},
		},
		"blind experiment resolved after admission hides the release": {
			handler: func(c *gin.Context) {
				blind := auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn}
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), auth.BlindExperimentContextKey{}, blind))
				c.String(http.StatusOK, "ok")
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, admitted, _, _ := admissionMiddlewareFixture(t)
			cfg.Attribution = admissionAttributionFunc(noopAttribution)
			response := runAdmissionMiddlewareWithPrelude(t, cfg, admitted, optIn, tc.handler)
			release := response.Header().Get(requestcontext.HeaderServingReleaseSHA)
			policySHA := response.Header().Get(requestcontext.HeaderServingPolicySHA)
			if !tc.wantHeaders {
				assert.Empty(t, release)
				assert.Empty(t, policySHA)
				return
			}
			assert.Equal(t, admitted.Admission.Selection.Release.SHA256[:12], release)
			assert.Len(t, policySHA, 12)
		})
	}
}
