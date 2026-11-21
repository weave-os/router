package observability_test

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/observability"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type captureTransport struct{ traceparent string }

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.traceparent = r.Header.Get("traceparent")
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
}

func tracedContext(t *testing.T) context.Context {
	t.Helper()
	previous := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	otel.SetTextMapPropagator(propagation.TraceContext{})
	traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	return oteltrace.ContextWithRemoteSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: oteltrace.FlagsSampled, Remote: true,
	}))
}

func roundTrip(t *testing.T, ctx context.Context, traceparent string) string {
	t.Helper()
	base := &captureTransport{}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.example/v1/messages", http.NoBody)
	require.NoError(t, err)
	if traceparent != "" {
		req.Header.Set("traceparent", traceparent)
	}
	resp, err := observability.NewTracingTransport(base).RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return base.traceparent
}

func TestTracingTransportKeepsAnExplicitTraceparent(t *testing.T) {
	ctx := tracedContext(t)
	explicit := "00-fedcba9876543210fedcba9876543210-fedcba9876543210-01"
	assert.Equal(t, explicit, roundTrip(t, ctx, explicit))
}

func TestTracingTransportInjectsTraceparentWhenAbsent(t *testing.T) {
	got := roundTrip(t, tracedContext(t), "")
	assert.Equal(t, "0123456789abcdef0123456789abcdef", got[3:35])
}
