package middleware_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	engine.Use(middleware.WithRequestBodyBudget(48<<20, time.Minute))
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

func TestRequestBodyBudgetReleasesStalledUploadsAfterReadTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const declaredBytes = 8 << 20
	engine := gin.New()
	engine.Use(middleware.WithRequestBodyBudget(declaredBytes*8, 200*time.Millisecond))
	engine.POST("/v1/responses", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestTimeout)
			return
		}
		c.Status(http.StatusOK)
	})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	var dialer net.Dialer
	stalled, err := dialer.DialContext(t.Context(), "tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = stalled.Close() })
	_, err = fmt.Fprintf(stalled, "POST /v1/responses HTTP/1.1\r\nHost: router\r\nContent-Length: %d\r\n\r\n", declaredBytes)
	require.NoError(t, err)
	require.NoError(t, stalled.SetReadDeadline(time.Now().Add(10*time.Second)))
	stalledResponse, err := http.ReadResponse(bufio.NewReader(stalled), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stalledResponse.Body.Close() })
	require.Equal(t, http.StatusRequestTimeout, stalledResponse.StatusCode)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/responses", bytes.NewReader(bytes.Repeat([]byte("a"), declaredBytes)))
	require.NoError(t, err)
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a timed-out upload returns its budget")
}
