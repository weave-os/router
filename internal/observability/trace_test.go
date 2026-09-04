package observability_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestLoggerWithTraceContextUsesCloudLoggingFields(t *testing.T) {
	t.Setenv("GCP_PROJECT_ID", "router-project")
	var buf strings.Builder
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	ctx := oteltrace.ContextWithRemoteSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	}))

	observability.LoggerWithTraceContext(ctx, log).Info("trace-linked")
	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &record))
	assert.Equal(t, "projects/router-project/traces/0123456789abcdef0123456789abcdef", record["logging.googleapis.com/trace"])
	assert.Equal(t, "0123456789abcdef", record["logging.googleapis.com/spanId"])
}

func TestInjectTraceContextAddsW3CHeaders(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	ctx := oteltrace.ContextWithRemoteSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	}))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	observability.InjectTraceContext(ctx, req)
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", req.Header.Get("traceparent"))
}

func TestMiddlewareIncludesTraceContextInRequestLogs(t *testing.T) {
	t.Setenv("GCP_PROJECT_ID", "router-project")
	var buf strings.Builder
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	ctx := oteltrace.ContextWithRemoteSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	}))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(observability.Middleware())
	engine.GET("/x", func(c *gin.Context) {
		observability.FromGin(c).Info("handler ran")
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/x", nil)
	engine.ServeHTTP(httptest.NewRecorder(), req)

	line := findLogLine(t, buf.String(), "handler ran")
	assert.Equal(t, "projects/router-project/traces/0123456789abcdef0123456789abcdef", line["logging.googleapis.com/trace"])
	assert.Equal(t, "0123456789abcdef", line["logging.googleapis.com/spanId"])
}
