package middleware_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/timing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type waitFrame struct {
	Type    string      `json:"type"`
	Waiting bool        `json:"waiting"`
	Status  int         `json:"status"`
	Headers http.Header `json:"headers"`
	Body    []byte      `json:"body"`
}

func waitRequest(t *testing.T, optedIn bool, serve gin.HandlerFunc) (*httptest.ResponseRecorder, []waitFrame) {
	t.Helper()
	engine := gin.New()
	engine.POST("/v1/messages", middleware.WithProviderWait(), serve)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if optedIn {
		req.Header.Set(middleware.ProviderWaitHeader, "1")
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	var frames []waitFrame
	if optedIn {
		decoder := json.NewDecoder(rec.Body)
		for {
			var frame waitFrame
			err := decoder.Decode(&frame)
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			frames = append(frames, frame)
		}
	}
	return rec, frames
}

func TestProviderWaitPreservesNativeResponseAndRetryWindow(t *testing.T) {
	rec, frames := waitRequest(t, true, func(c *gin.Context) {
		ctx := c.Request.Context()
		assert.Empty(t, c.GetHeader(middleware.ProviderWaitHeader))
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("X-Request-Id", "fixture-id")
		finish := timing.BeginProviderWait(ctx)
		assert.False(t, c.Writer.Written(), "wait state must not commit the provider response")
		finish() // failed attempt: still no native response
		finish = timing.BeginProviderWait(ctx)
		timing.FirstOutputMark(ctx, func() {})()
		_, err := c.Writer.Write([]byte("data: fixture\n\n"))
		require.NoError(t, err)
		finish()
	})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, middleware.ProviderWaitContentType, rec.Header().Get("Content-Type"))
	require.Len(t, frames, 7)
	assert.Equal(t, []waitFrame{
		{Type: "waiting", Waiting: true},
		{Type: "waiting"},
		{Type: "waiting", Waiting: true},
		{Type: "waiting"},
	}, frames[:4])
	assert.Equal(t, http.StatusOK, frames[4].Status)
	assert.Equal(t, "text/event-stream", frames[4].Headers.Get("Content-Type"))
	assert.Equal(t, "fixture-id", frames[4].Headers.Get("X-Request-Id"))
	assert.Equal(t, []byte("data: fixture\n\n"), frames[5].Body)
	assert.Equal(t, "done", frames[6].Type)
}

func TestProviderWaitPreservesErrorStatusHeadersAndBody(t *testing.T) {
	_, frames := waitRequest(t, true, func(c *gin.Context) {
		defer timing.BeginProviderWait(c.Request.Context())()
		c.Header("Retry-After", "3")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "fixture rejection"})
	})
	require.Len(t, frames, 5)
	assert.Equal(t, http.StatusTooManyRequests, frames[1].Status)
	assert.Equal(t, "3", frames[1].Headers.Get("Retry-After"))
	assert.JSONEq(t, `{"error":"fixture rejection"}`, string(frames[2].Body))
	assert.Equal(t, waitFrame{Type: "waiting"}, frames[3])
	assert.Equal(t, "done", frames[4].Type)
}

func TestProviderWaitDoesNotChangeOrdinaryClients(t *testing.T) {
	rec, _ := waitRequest(t, false, func(c *gin.Context) {
		defer timing.BeginProviderWait(c.Request.Context())()
		c.JSON(http.StatusBadRequest, gin.H{"error": "fixture rejection"})
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"error":"fixture rejection"}`, rec.Body.String())
}

func TestProviderWaitWithoutDispatchNeverShowsWaiting(t *testing.T) {
	_, frames := waitRequest(t, true, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	require.Len(t, frames, 2)
	assert.Equal(t, http.StatusNoContent, frames[0].Status)
	assert.Equal(t, "response", frames[0].Type)
	assert.Equal(t, "done", frames[1].Type)
}
