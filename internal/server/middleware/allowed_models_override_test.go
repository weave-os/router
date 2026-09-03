package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"workweave/router/internal/auth"
	"workweave/router/internal/flags"
	"workweave/router/internal/proxy"
	"workweave/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubAllowedModelsGate bool

func (g stubAllowedModelsGate) ResolveAllowedModelsHeader(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAllowedModelsHeader, bool(g))
}

func runAllowedModelsMiddleware(t *testing.T, installation *auth.Installation, header string) (int, proxy.RequestAllowedModels, bool) {
	return runAllowedModelsMiddlewareWithDefault(t, installation, header, false)
}

func runAllowedModelsMiddlewareWithDefault(t *testing.T, installation *auth.Installation, header string, deploymentDefault bool) (int, proxy.RequestAllowedModels, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		if installation != nil {
			c.Set("router_installation", installation)
			c.Request = c.Request.WithContext(flags.WithOverrides(c.Request.Context(), installation.FlagOverrides))
		}
		c.Next()
	})
	engine.Use(middleware.WithAllowedModelsOverride(stubAllowedModelsGate(deploymentDefault)))
	var observed proxy.RequestAllowedModels
	var ok bool
	engine.POST("/v1/messages", func(c *gin.Context) {
		observed, ok = c.Request.Context().Value(proxy.RequestAllowedModelsContextKey{}).(proxy.RequestAllowedModels)
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if header != "" {
		req.Header.Set(proxy.AllowedModelsHeader, header)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec.Code, observed, ok
}

func TestAllowedModelsOverride_AbsentHeaderIsNoOp(t *testing.T) {
	status, _, ok := runAllowedModelsMiddleware(t, &auth.Installation{}, "")
	require.Equal(t, http.StatusOK, status)
	assert.False(t, ok)
}

func TestAllowedModelsOverride_UnauthorizedIsForbidden(t *testing.T) {
	status, _, ok := runAllowedModelsMiddleware(t, &auth.Installation{}, "sol")
	assert.Equal(t, http.StatusForbidden, status)
	assert.False(t, ok)
}

func TestAllowedModelsOverride_PolicyHeadersAuthorize(t *testing.T) {
	status, got, ok := runAllowedModelsMiddleware(t, &auth.Installation{PolicyHeaderOverridesEnabled: true}, "sol,terra")
	require.Equal(t, http.StatusOK, status)
	require.True(t, ok)
	assert.Equal(t, []string{"gpt-5.6-sol", "gpt-5.6-terra"}, got.Effective)
}

func TestAllowedModelsOverride_OrgFlagAuthorizes(t *testing.T) {
	installation := &auth.Installation{FlagOverrides: flags.Overrides{Bools: map[flags.Key]bool{flags.KeyAllowedModelsHeader: true}}}
	status, got, ok := runAllowedModelsMiddleware(t, installation, "sol")
	require.Equal(t, http.StatusOK, status)
	require.True(t, ok)
	assert.Equal(t, []string{"gpt-5.6-sol"}, got.Effective)
}

func TestAllowedModelsOverride_IntersectsInstallationAllowlist(t *testing.T) {
	installation := &auth.Installation{PolicyHeaderOverridesEnabled: true, AllowedModels: []string{"gpt-5.6-sol"}}
	status, got, ok := runAllowedModelsMiddleware(t, installation, "sol,terra")
	require.Equal(t, http.StatusOK, status)
	require.True(t, ok)
	assert.Equal(t, []string{"gpt-5.6-sol", "gpt-5.6-terra"}, got.Requested)
	assert.Equal(t, []string{"gpt-5.6-sol"}, got.Effective)
}

func TestAllowedModelsOverride_EmptyIntersectionIsBadRequest(t *testing.T) {
	installation := &auth.Installation{PolicyHeaderOverridesEnabled: true, AllowedModels: []string{"gpt-5.6-sol"}}
	status, _, ok := runAllowedModelsMiddleware(t, installation, "terra")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.False(t, ok)
}

func TestAllowedModelsOverride_UnknownModelIsBadRequest(t *testing.T) {
	status, _, ok := runAllowedModelsMiddleware(t, &auth.Installation{PolicyHeaderOverridesEnabled: true}, "sol,nope")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.False(t, ok)
}

func TestAllowedModelsOverride_DeploymentDefaultAuthorizes(t *testing.T) {
	status, got, ok := runAllowedModelsMiddlewareWithDefault(t, &auth.Installation{}, "sol", true)
	require.Equal(t, http.StatusOK, status)
	require.True(t, ok)
	assert.Equal(t, []string{"gpt-5.6-sol"}, got.Effective)
}

func TestAllowedModelsOverride_OrgOverrideCanDisableDeploymentDefault(t *testing.T) {
	installation := &auth.Installation{FlagOverrides: flags.Overrides{Bools: map[flags.Key]bool{flags.KeyAllowedModelsHeader: false}}}
	status, _, ok := runAllowedModelsMiddlewareWithDefault(t, installation, "sol", true)
	assert.Equal(t, http.StatusForbidden, status)
	assert.False(t, ok)
}
