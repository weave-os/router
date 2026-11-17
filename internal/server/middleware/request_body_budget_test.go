package middleware_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/server/middleware"
)

func TestRequestBodyBudgetRejectsLargeBodiesBeyondBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	largeBody := bytes.Repeat([]byte("a"), 4<<20)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	engine := gin.New()
	engine.Use(middleware.WithRequestBodyBudget(48 << 20))
	engine.POST("/v1/responses", func(c *gin.Context) {
		if c.Query("hold") != "" {
			entered <- struct{}{}
			<-release
		}
		c.Status(http.StatusOK)
	})
	post := func(path string, body []byte) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(body)))
		return rec
	}

	held := make(chan *httptest.ResponseRecorder, 1)
	go func() { held <- post("/v1/responses?hold=1", largeBody) }()
	<-entered

	rejected := post("/v1/responses", largeBody)
	require.Equal(t, http.StatusServiceUnavailable, rejected.Code)
	assert.Equal(t, "1", rejected.Header().Get("Retry-After"))
	assert.Contains(t, rejected.Body.String(), "request_body_budget_exhausted")

	assert.Equal(t, http.StatusOK, post("/v1/responses", []byte(`{"input":"hi"}`)).Code,
		"bodies under the budget threshold are never charged")

	close(release)
	require.Equal(t, http.StatusOK, (<-held).Code)
	assert.Equal(t, http.StatusOK, post("/v1/responses", largeBody).Code, "a finished request returns its budget")
}
