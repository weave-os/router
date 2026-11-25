package spans_test

import (
	"context"
	"testing"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry/spans"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestFinishInferenceSpanAttributesExceptionEvent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	previousTracer := spans.FlowTracer
	spans.FlowTracer = provider.Tracer("test")
	t.Cleanup(func() { spans.FlowTracer = previousTracer })

	decision := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-test", Reason: "cluster"}
	ctx, span := spans.StartInferenceSpan(context.Background(), decision, "client-session-abc")
	spans.FinishInferenceSpan(ctx, span, decision, providers.ProviderOpenAI, 1, &providers.UpstreamErrorResponse{Status: 502})

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	assert.Equal(t, codes.Error, ended[0].Status().Code)
	require.Len(t, ended[0].Events(), 1)
	event := ended[0].Events()[0]
	assert.Equal(t, "exception", event.Name)
	attrs := attribute.NewSet(event.Attributes...)
	assertAttr(t, attrs, "exception.message", attribute.StringValue("upstream returned status 502 (buffered)"))
	assertAttr(t, attrs, "client.session_id", attribute.StringValue("client-session-abc"))
	assertAttr(t, attrs, "served.provider", attribute.StringValue(providers.ProviderOpenAI))
	assertAttr(t, attrs, "served.model", attribute.StringValue("gpt-test"))
	assertAttr(t, attrs, "upstream.status_code", attribute.IntValue(502))
	assertAttr(t, attrs, "upstream.binding_index", attribute.IntValue(1))
}

func TestFinishInferenceSpanOmitsEmptySessionFromExceptionEvent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	previousTracer := spans.FlowTracer
	spans.FlowTracer = provider.Tracer("test")
	t.Cleanup(func() { spans.FlowTracer = previousTracer })

	decision := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-test"}
	ctx, span := spans.StartInferenceSpan(context.Background(), decision, "")
	require.Empty(t, observability.ClientSessionIDFromContext(ctx))
	spans.FinishInferenceSpan(ctx, span, decision, providers.ProviderOpenAI, -1, &providers.UpstreamErrorResponse{Status: 502})

	event := recorder.Ended()[0].Events()[0]
	attrs := attribute.NewSet(event.Attributes...)
	_, hasSession := attrs.Value("client.session_id")
	assert.False(t, hasSession)
	_, hasBinding := attrs.Value("upstream.binding_index")
	assert.False(t, hasBinding)
	assertAttr(t, attrs, "upstream.status_code", attribute.IntValue(502))
}

func assertAttr(t *testing.T, attrs attribute.Set, key string, want attribute.Value) {
	t.Helper()
	got, ok := attrs.Value(attribute.Key(key))
	require.True(t, ok, "missing event attribute %s", key)
	assert.Equal(t, want, got, key)
}
