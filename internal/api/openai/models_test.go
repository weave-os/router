package openai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/api/openai"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestModelsHandler_CodexShapeByClientApp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name      string
		xApp      string
		wantCodex bool
	}{
		{"codex", "codex", true},
		{"eval-tagged codex keeps the codex shape", "weave-eval-codex", true},
		{"claude-code falls through", "claude-code", false},
		{"unknown falls through", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fallbackHit := false
			engine := gin.New()
			engine.GET("/v1/models", openai.ModelsHandler(func(c *gin.Context) {
				fallbackHit = true
				c.Status(http.StatusNoContent)
			}, func(context.Context, http.Header, string) ([]byte, error) {
				t.Fatal("legacy Codex requests must not fetch a catalog")
				return nil, nil
			}))

			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if tc.xApp != "" {
				req.Header.Set("X-App", tc.xApp)
			}
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)

			if tc.wantCodex {
				assert.False(t, fallbackHit)
				assert.Equal(t, http.StatusOK, rec.Code)
				assert.JSONEq(t, `{"models":[]}`, rec.Body.String())
				return
			}
			assert.True(t, fallbackHit)
			assert.Equal(t, http.StatusNoContent, rec.Code)
		})
	}
}

func TestModelsHandler_NativeModelPinCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/v1/models", openai.ModelsHandler(func(c *gin.Context) {
		t.Fatal("Codex must not use the fallback provider")
	}, func(_ context.Context, headers http.Header, version string) ([]byte, error) {
		assert.Equal(t, "1", headers.Get("X-Weave-Codex-Native-Model-Pin"))
		assert.Equal(t, "0.155.1", version)
		return []byte(`{"models":[{"slug":"gpt-6-sol"},{"slug":"weave-auto"}]}`), nil
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.155.1", nil)
	req.Header.Set("X-App", "codex")
	req.Header.Set("X-Weave-Codex-Native-Model-Pin", "1")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"models":[{"slug":"gpt-6-sol"},{"slug":"weave-auto"}]}`, rec.Body.String())
}

func TestModelsHandler_CatalogFailureKeepsBundledModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/v1/models", openai.ModelsHandler(func(c *gin.Context) {
		t.Fatal("Codex must not use the fallback provider")
	}, func(context.Context, http.Header, string) ([]byte, error) {
		return nil, errors.New("catalog unavailable")
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-App", "codex")
	req.Header.Set("X-Weave-Codex-Native-Model-Pin", "1")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	assert.JSONEq(t, `{"models":[]}`, rec.Body.String())
}
