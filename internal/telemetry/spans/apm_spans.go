package spans

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const proxyTracerName = "weave-os/router/internal/proxy"

var FlowTracer = otel.Tracer(proxyTracerName)

func StartRoutingSpan(ctx context.Context, strategy router.Strategy, req router.Request) (context.Context, trace.Span) {
	ctx = observability.WithClientSessionID(ctx, req.ClientSessionID)
	return FlowTracer.Start(ctx, "router.routing",
		trace.WithAttributes(
			attribute.String("client.session_id", req.ClientSessionID),
			attribute.String("requested.model", req.RequestedModel),
			attribute.Int("request.estimated_input_tokens", req.EstimatedInputTokens),
			attribute.Bool("request.has_tools", req.HasTools),
			attribute.String("router.strategy", string(strategy)),
		),
	)
}

func FinishRoutingSpan(span trace.Span, decision router.Decision, err error) {
	attrs := []attribute.KeyValue{
		attribute.String("decision.model", decision.Model),
		attribute.String("decision.provider", decision.Provider),
		attribute.String("decision.reason", decision.Reason),
	}
	span.SetAttributes(attrs...)
	finishFlowSpan(span, err, attrs)
}

func StartInferenceSpan(ctx context.Context, decision router.Decision, clientSessionID string) (context.Context, trace.Span) {
	ctx = observability.WithClientSessionID(ctx, clientSessionID)
	return FlowTracer.Start(ctx, "router.inference",
		trace.WithAttributes(
			attribute.String("client.session_id", clientSessionID),
			attribute.String("decision.model", decision.Model),
			attribute.String("decision.provider", decision.Provider),
			attribute.String("decision.reason", decision.Reason),
		),
	)
}

func FinishInferenceSpan(ctx context.Context, span trace.Span, decision router.Decision, provider string, attempt int, err error) {
	attrs := []attribute.KeyValue{
		attribute.String("served.model", decision.Model),
		attribute.String("served.provider", provider),
	}
	if attempt >= 0 {
		attrs = append(attrs, attribute.Int("upstream.binding_index", attempt))
	}
	if status := providers.UpstreamStatus(err); status != 0 {
		attrs = append(attrs, attribute.Int("upstream.status_code", status))
	}
	span.SetAttributes(attrs...)
	eventAttrs := attrs
	if clientSessionID := observability.ClientSessionIDFromContext(ctx); clientSessionID != "" {
		eventAttrs = append(eventAttrs, attribute.String("client.session_id", clientSessionID))
	}
	finishFlowSpan(span, err, eventAttrs)
}

func finishFlowSpan(span trace.Span, err error, eventAttrs []attribute.KeyValue) {
	if err != nil {
		span.RecordError(err, trace.WithAttributes(eventAttrs...))
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

func RestoreParentSpan(ctx, parent context.Context) context.Context {
	return trace.ContextWithSpan(ctx, trace.SpanFromContext(parent))
}
