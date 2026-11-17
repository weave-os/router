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

func TestRequestBodyBudgetReleasesStalledUploadsAfterReadTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := map[string][]gin.HandlerFunc{
		"plain writer":            nil,
		"traffic capture wrapper": {middleware.WithTrafficCapture(&exchangeRecorder{})},
	}
	for name, outer := range cases {
		t.Run(name, func(t *testing.T) {
			requireStalledUploadReleasesBudget(t, outer)
		})
	}
}

func requireStalledUploadReleasesBudget(t *testing.T, outer []gin.HandlerFunc) {
	t.Helper()
	const declaredBytes = 8 << 20
	engine := gin.New()
	engine.Use(outer...)
	engine.Use(middleware.WithRequestBodyBudget(declaredBytes*16, middleware.RequestBodyReadLimits{Total: 200 * time.Millisecond, Idle: time.Minute}))
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

func TestRequestBodyBudgetCutsOffUploadsThatStopSending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const declaredBytes = 8 << 20
	engine := gin.New()
	engine.Use(middleware.WithRequestBodyBudget(declaredBytes*16, middleware.RequestBodyReadLimits{Total: time.Minute, Idle: 300 * time.Millisecond}))
	engine.POST("/v1/responses", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestTimeout)
			return
		}
		c.Status(http.StatusOK)
	})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	send := func(t *testing.T, chunks int, pause time.Duration) int {
		t.Helper()
		var dialer net.Dialer
		conn, err := dialer.DialContext(t.Context(), "tcp", server.Listener.Addr().String())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		_, err = fmt.Fprintf(conn, "POST /v1/responses HTTP/1.1\r\nHost: router\r\nContent-Length: %d\r\n\r\n", declaredBytes)
		require.NoError(t, err)
		chunk := bytes.Repeat([]byte("a"), declaredBytes/8)
		for range chunks {
			_, err = conn.Write(chunk)
			require.NoError(t, err)
			time.Sleep(pause)
		}
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("an upload that keeps sending outlives the idle timeout", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, send(t, 8, 100*time.Millisecond))
	})
	t.Run("an upload that stops sending is cut off", func(t *testing.T) {
		started := time.Now()
		assert.Equal(t, http.StatusRequestTimeout, send(t, 2, 0))
		assert.Less(t, time.Since(started), 5*time.Second)
	})
}
