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
	"weave-os/router/internal/observability/apm"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
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

func TestClientSessionIDContextRoundTrip(t *testing.T) {
	ctx := observability.WithClientSessionID(context.Background(), "client-session-abc")

	assert.Equal(t, "client-session-abc", observability.ClientSessionIDFromContext(ctx))
	assert.Empty(t, observability.ClientSessionIDFromContext(context.Background()))
}

func TestWrapHTTPClientPropagatesTraceContextWithoutBaggage(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	otel.SetTextMapPropagator(propagation.TraceContext{})

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
	member, err := baggage.NewMember("tenant", "internal")
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)
	ctx = baggage.ContextWithBaggage(ctx, bag)

	var got http.Header
	client := observability.WrapHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Header.Clone()
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header)}, nil
	})})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", got.Get("traceparent"))
	assert.Empty(t, got.Get("baggage"))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestMiddlewareIncludesTraceContextInRequestLogs(t *testing.T) {
	t.Setenv("GCP_PROJECT_ID", "router-project")
	var buf strings.Builder
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	previous := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	otel.SetTextMapPropagator(propagation.TraceContext{})
	engine.Use(apm.Middleware(), observability.Middleware())
	engine.GET("/x", func(c *gin.Context) {
		observability.FromGin(c).Info("handler ran")
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	line := findLogLine(t, buf.String(), "handler ran")
	assert.Equal(t, "projects/router-project/traces/0123456789abcdef0123456789abcdef", line["logging.googleapis.com/trace"])
	assert.Equal(t, "0123456789abcdef", line["logging.googleapis.com/spanId"])
}
