package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func runToolCheck(t *testing.T, header string) (status int, captured router.ToolCheckMode, sawHandler bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.WithToolCheckOverride())
	engine.POST("/v1/messages", func(c *gin.Context) {
		sawHandler = true
		captured = router.ToolCheckModeFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if header != "" {
		req.Header.Set(middleware.ToolCheckOverrideHeader, header)
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr.Code, captured, sawHandler
}

func TestToolCheckOverride_AbsentIsStandard(t *testing.T) {
	status, mode, _ := runToolCheck(t, "")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, router.ToolCheckStandard, mode)
}

func TestToolCheckOverride_ValuesCaseInsensitive(t *testing.T) {
	for in, want := range map[string]router.ToolCheckMode{
		"off": router.ToolCheckOff, " Semantic ": router.ToolCheckSemantic, "STANDARD": router.ToolCheckStandard,
	} {
		status, mode, _ := runToolCheck(t, in)
		assert.Equal(t, http.StatusOK, status, in)
		assert.Equal(t, want, mode, in)
	}
}

func TestToolCheckOverride_InvalidAborts400(t *testing.T) {
	status, _, sawHandler := runToolCheck(t, "aggressive")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.False(t, sawHandler)
}
