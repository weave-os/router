package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prepaidConcurrencyEngine(guard gin.HandlerFunc, organizationID, apiKeyID string, prepaid bool, handler gin.HandlerFunc) *gin.Engine {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		withInstallation(c, organizationID)
		if apiKeyID != "" {
			c.Set("router_api_key", &auth.APIKey{ID: apiKeyID})
		}
		if prepaid {
			c.Request = c.Request.WithContext(billing.WithOrganizationPrepaid(c.Request.Context()))
		}
		c.Next()
	}, guard)
	engine.GET("/", handler)
	return engine
}

func servePrepaidRequest(engine *gin.Engine) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestOrganizationPrepaidConcurrencyLimitSharedAcrossProtocolGroups(t *testing.T) {
	gin.SetMode(gin.TestMode)
	guard := middleware.WithOrganizationPrepaidConcurrencyLimit(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan *httptest.ResponseRecorder, 1)

	blocking := prepaidConcurrencyEngine(guard, "org_x", "key_x", true, func(c *gin.Context) {
		close(entered)
		<-release
		c.Status(http.StatusNoContent)
	})
	secondProtocol := prepaidConcurrencyEngine(guard, "org_x", "key_x", true, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	go func() { firstDone <- servePrepaidRequest(blocking) }()
	<-entered

	rejected := servePrepaidRequest(secondProtocol)
	assert.Equal(t, http.StatusTooManyRequests, rejected.Code)
	assert.Equal(t, "1", rejected.Header().Get("Retry-After"))
	assert.JSONEq(t, `{
		"error":"organization_prepaid_concurrency_limit_reached",
		"max_in_flight":1,
		"message":"Too many paid requests are already in progress for this organization. Retry after one finishes."
	}`, rejected.Body.String())

	otherOrg := prepaidConcurrencyEngine(guard, "org_y", "key_x", true, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	assert.Equal(t, http.StatusNoContent, servePrepaidRequest(otherOrg).Code)

	otherKey := prepaidConcurrencyEngine(guard, "org_x", "key_y", true, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	assert.Equal(t, http.StatusNoContent, servePrepaidRequest(otherKey).Code)

	unbilled := prepaidConcurrencyEngine(guard, "org_x", "key_x", false, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	assert.Equal(t, http.StatusNoContent, servePrepaidRequest(unbilled).Code)

	close(release)
	first := <-firstDone
	require.Equal(t, http.StatusNoContent, first.Code)
	assert.Equal(t, http.StatusNoContent, servePrepaidRequest(secondProtocol).Code)
}

func TestOrganizationPrepaidConcurrencyLimitRequiresPositiveLimit(t *testing.T) {
	assert.Panics(t, func() {
		middleware.WithOrganizationPrepaidConcurrencyLimit(0)
	})
}
