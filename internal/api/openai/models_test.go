package openai_test

import (
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
