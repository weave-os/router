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

const adminAuthTestToken = "rk_admin_auth_test"

func adminAuthTestService(t *testing.T) *auth.Service {
	t.Helper()
	hash, prefix, suffix := auth.APITokenFingerprint(adminAuthTestToken)
	repo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {
			apiKey:       &auth.APIKey{ID: "key-1", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix},
			installation: &auth.Installation{ID: "inst-1", ExternalID: "ext-1"},
		},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time {
		return time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	})
	return svc.WithAdminPassword("operator-password")
}

func serveAdminAuth(t *testing.T, handler gin.HandlerFunc, cookie string) (int, *auth.Installation) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var installation *auth.Installation
	engine := gin.New()
	engine.GET("/admin/v1/probe", handler, func(c *gin.Context) {
		installation = middleware.InstallationFrom(c)
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/v1/probe", nil)
	req.Header.Set("Authorization", "Bearer "+adminAuthTestToken)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.AdminSessionCookieName, Value: cookie, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr.Code, installation
}

func TestWithAdminOnlyRejectsValidRoutingKey(t *testing.T) {
	status, installation := serveAdminAuth(t, middleware.WithAdminOnly(adminAuthTestService(t)), "")

	assert.Equal(t, http.StatusUnauthorized, status, "a leaked rk_ key must not reach admin-only mutations")
	assert.Nil(t, installation)
}

func TestWithAdminOrAuthFallsThroughToBearerOnTamperedCookie(t *testing.T) {
	status, installation := serveAdminAuth(t, middleware.WithAdminOrAuth(adminAuthTestService(t), false), "tampered-session")

	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, installation)
	assert.Equal(t, "inst-1", installation.ID)
}
