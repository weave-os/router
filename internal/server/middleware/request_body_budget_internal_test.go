package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

func TestRequestBodyBudgetCapsEachKeyAtHalfThePool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	largeBody := bytes.Repeat([]byte("a"), requestBodyBudgetThresholdBytes)
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(ctxKeyAPIKey, &auth.APIKey{ID: c.GetHeader("X-Test-Key")})
	}, WithRequestBodyBudget(2*requestBodyBudgetThresholdBytes*requestBodyProcessingAmplification, DefaultRequestBodyReadLimits))
	engine.POST("/v1/responses", func(c *gin.Context) {
		if c.Query("hold") != "" {
			entered <- struct{}{}
			<-release
		}
		c.Status(http.StatusOK)
	})
	post := func(key, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("X-Test-Key", key)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}

	held := make(chan *httptest.ResponseRecorder, 2)
	go func() { held <- post("key-a", "/v1/responses?hold=1", largeBody) }()
	<-entered

	sameKey := post("key-a", "/v1/responses", largeBody)
	require.Equal(t, http.StatusServiceUnavailable, sameKey.Code, "one key cannot hold more than half the pool")
	assert.Equal(t, "1", sameKey.Header().Get("Retry-After"))
	assert.Contains(t, sameKey.Body.String(), "request_body_budget_exhausted")

	go func() { held <- post("key-b", "/v1/responses?hold=1", largeBody) }()
	<-entered

	require.Equal(t, http.StatusServiceUnavailable, post("key-c", "/v1/responses", largeBody).Code, "a full pool refuses every key")
	assert.Equal(t, http.StatusOK, post("key-c", "/v1/responses", []byte(`{"input":"hi"}`)).Code,
		"bodies under the budget threshold are never charged")

	close(release)
	require.Equal(t, http.StatusOK, (<-held).Code)
	require.Equal(t, http.StatusOK, (<-held).Code)
	assert.Equal(t, http.StatusOK, post("key-a", "/v1/responses", largeBody).Code, "a finished request returns its budget")
}
