package observability_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/observability"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiddlewareBindsRequestIDToGinAndRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var fromGin, fromCtx string
	engine := gin.New()
	engine.Use(observability.Middleware())
	engine.GET("/x", func(c *gin.Context) {
		fromCtx = observability.RequestIDFromContext(c.Request.Context())
		// Both views must resolve to one logger, or a FromGin caller loses
		// the correlation fields.
		if observability.FromGin(c) == observability.FromContext(c.Request.Context()) {
			fromGin = fromCtx
		}
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	require.NotEmpty(t, fromCtx, "request id must be bound to the request context")
	assert.Equal(t, fromCtx, fromGin, "gin and context loggers must be the same instance")
	assert.Equal(t, fromCtx, rec.Header().Get("X-Request-Id"), "response must echo the id operators will search by")
}

// A client-supplied id must not become the router's request_id: one caller
// reusing a value would make request_id stop identifying a single request.
func TestMiddlewareDoesNotAdoptInboundRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const inbound = "client-supplied-constant"
	var buf strings.Builder
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	var seen string
	engine := gin.New()
	engine.Use(observability.Middleware())
	engine.GET("/x", func(c *gin.Context) {
		seen = observability.RequestIDFromContext(c.Request.Context())
		observability.FromGin(c).Info("handler ran")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-Id", inbound)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.NotEmpty(t, seen)
	assert.NotEqual(t, inbound, seen, "router must mint its own id, not trust the client's")
	assert.Equal(t, seen, rec.Header().Get("X-Request-Id"))

	// The client's value is still recorded, just under its own field.
	line := findLogLine(t, buf.String(), "handler ran")
	assert.Equal(t, inbound, line["upstream_request_id"])
	assert.Equal(t, seen, line["request_id"])
}

func TestMiddlewareBoundsInboundRequestIDValue(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf strings.Builder
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	engine := gin.New()
	engine.Use(observability.Middleware())
	engine.GET("/x", func(c *gin.Context) {
		observability.FromGin(c).Info("handler ran")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-Id", strings.Repeat("A", 5000)+"\ninjected=1")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	line := findLogLine(t, buf.String(), "handler ran")
	got, ok := line["upstream_request_id"].(string)
	require.True(t, ok)
	assert.LessOrEqual(t, len(got), 200, "a pathological header must not bloat every line")
	assert.NotContains(t, got, "\n", "newlines must not reach a log field")
}

// The access log is the one line guaranteed to exist per request. Before the
// bridge it carried only method/path, so it could not be joined to anything.
func TestAccessLogCarriesRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf strings.Builder
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	engine := gin.New()
	engine.Use(observability.Middleware(), observability.AccessLog())
	engine.GET("/x", func(c *gin.Context) { c.Status(http.StatusTeapot) })

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	line := findLogLine(t, buf.String(), "http request")
	assert.NotEmpty(t, line["request_id"], "access log must carry request_id")
	assert.Equal(t, rec.Header().Get("X-Request-Id"), line["request_id"])
	assert.Equal(t, float64(http.StatusTeapot), line["status"])
}

func TestRequestIDFromContextEmptyWithoutMiddleware(t *testing.T) {
	assert.Empty(t, observability.RequestIDFromContext(context.TODO()))
	assert.Empty(t, observability.RequestIDFromContext(t.Context()))
}

// swapDefaultLogger points slog.Default at buf as JSON so tests can assert on
// emitted fields, restoring the prior logger on cleanup. Middleware reads
// slog.Default() per request, so this is observed without touching initOnce.
func swapDefaultLogger(t *testing.T, buf io.Writer) func() {
	t.Helper()
	// Force the package's lazy init first, so it cannot later overwrite the
	// test handler on the first Middleware call.
	observability.Get()

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { slog.SetDefault(prev) }
}

// findLogLine returns the first JSON log record whose msg matches.
func findLogLine(t *testing.T, out, msg string) map[string]any {
	t.Helper()
	for raw := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if raw == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			continue
		}
		if rec["msg"] == msg || rec["message"] == msg {
			return rec
		}
	}
	t.Fatalf("no log line with msg %q in:\n%s", msg, out)
	return nil
}

// The access log is the one record guaranteed per request, and it reads the
// gin-bound logger. A field derived after Middleware (session_key, known only
// once the body parses) must reach it, or a session-filtered query silently
// misses the request's own summary line.
func TestPromoteRequestLoggerReachesAccessLogAndFromGin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf strings.Builder
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	engine := gin.New()
	engine.Use(observability.Middleware(), observability.AccessLog())
	engine.GET("/x", func(c *gin.Context) {
		// What bindRequestLogger does once the envelope has parsed.
		enriched := observability.FromContext(c.Request.Context()).With("session_key", "abc123")
		c.Request = c.Request.WithContext(
			observability.PromoteRequestLogger(c.Request.Context(), enriched),
		)
		// A handler that grabbed its logger via FromGin must see it too.
		observability.FromGin(c).Info("handler after promote")
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	access := findLogLine(t, buf.String(), "http request")
	assert.Equal(t, "abc123", access["session_key"],
		"access log must carry the promoted session_key")
	assert.Equal(t, rec.Header().Get("X-Request-Id"), access["request_id"],
		"promoting must not drop the id bound by Middleware")

	handler := findLogLine(t, buf.String(), "handler after promote")
	assert.Equal(t, "abc123", handler["session_key"], "FromGin must observe the promotion")
}

// Promotion is a no-op without Middleware rather than a panic, so a non-HTTP
// caller (background job, test) can share the same code path.
func TestPromoteRequestLoggerWithoutMiddleware(t *testing.T) {
	log := slog.Default().With("session_key", "abc123")
	ctx := observability.PromoteRequestLogger(t.Context(), log)
	assert.Equal(t, log, observability.FromContext(ctx))
}
