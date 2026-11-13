package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func internalServiceEngine(token string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/internal/v1/ping", middleware.WithInternalServiceAuth(token), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return engine
}

func internalServiceResponse(t *testing.T, token, presented string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/ping", nil)
	if presented != "" {
		req.Header.Set("X-Weave-Internal-Token", presented)
	}
	rec := httptest.NewRecorder()
	internalServiceEngine(token).ServeHTTP(rec, req)
	return rec.Code
}

func TestWithInternalServiceAuth_AcceptsMatchingToken(t *testing.T) {
	assert.Equal(t, http.StatusNoContent, internalServiceResponse(t, "s3cret", "s3cret"))
}

func TestWithInternalServiceAuth_RejectsWrongOrMissingToken(t *testing.T) {
	assert.Equal(t, http.StatusUnauthorized, internalServiceResponse(t, "s3cret", "nope"))
	assert.Equal(t, http.StatusUnauthorized, internalServiceResponse(t, "s3cret", ""))
}

func TestWithInternalServiceAuth_RejectsEverythingWhenUnconfigured(t *testing.T) {
	assert.Equal(t, http.StatusUnauthorized, internalServiceResponse(t, "", ""))
}
