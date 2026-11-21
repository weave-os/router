package admin_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func adminLoginEngine() *gin.Engine {
	return adminLoginEngineWithConfig(admin.Config{})
}

func adminLoginEngineWithConfig(cfg admin.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).WithAdminPassword("operator-password")
	engine := gin.New()
	engine.POST("/admin/v1/auth/login", admin.LoginHandler(svc, cfg))
	return engine
}

func postAdminLogin(t *testing.T, engine *gin.Engine, password, forwardedFor string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.7:4321"
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr
}

func TestLoginHandlerThrottlesTCPPeerDespiteRotatedForwardedFor(t *testing.T) {
	engine := adminLoginEngine()
	for attempt, forwarded := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4", "198.51.100.5"} {
		require.Equal(t, http.StatusUnauthorized, postAdminLogin(t, engine, "wrong", forwarded).Code, "attempt %d", attempt)
	}

	assert.Equal(t, http.StatusTooManyRequests, postAdminLogin(t, engine, "wrong", "198.51.100.6").Code)
}

func TestLoginHandlerSetsSecureHTTPOnlyCookieByDefault(t *testing.T) {
	rr := postAdminLogin(t, adminLoginEngine(), "operator-password", "")

	require.Equal(t, http.StatusOK, rr.Code)
	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, auth.AdminSessionCookieName, cookies[0].Name)
	assert.True(t, cookies[0].Secure)
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
}

func TestLoginHandlerCookieInsecureDropsSecureFlag(t *testing.T) {
	rr := postAdminLogin(t, adminLoginEngineWithConfig(admin.Config{CookieInsecure: true}), "operator-password", "")

	require.Equal(t, http.StatusOK, rr.Code)
	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.False(t, cookies[0].Secure)
	assert.True(t, cookies[0].HttpOnly)
}
