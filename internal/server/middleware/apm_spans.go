package middleware

import (
	"context"
	"net/http"

	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const middlewareTracerName = "weave-os/router/internal/server/middleware"

var middlewareFlowTracer = otel.Tracer(middlewareTracerName)

type billingSpanState struct {
	span   trace.Span
	parent context.Context
	ended  bool
}

type billingSpanContextKey struct{}

func startAuthSpan(ctx context.Context, clientSessionID string) (context.Context, trace.Span) {
	ctx = observability.WithClientSessionID(ctx, clientSessionID)
	return middlewareFlowTracer.Start(ctx, "router.auth",
		trace.WithAttributes(attribute.String("client.session_id", clientSessionID)),
	)
}

func finishAuthSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

func startBillingSpan(ctx context.Context) (context.Context, *billingSpanState) {
	clientSessionID := observability.ClientSessionIDFromContext(ctx)
	spanCtx, span := middlewareFlowTracer.Start(ctx, "router.billing",
		trace.WithAttributes(attribute.String("client.session_id", clientSessionID)),
	)
	state := &billingSpanState{span: span, parent: ctx}
	return context.WithValue(spanCtx, billingSpanContextKey{}, state), state
}

func finishBillingSpan(state *billingSpanState, status int) {
	if state == nil || state.ended {
		return
	}
	state.ended = true
	state.span.SetAttributes(attribute.Int("http.response.status_code", status))
	if status >= http.StatusBadRequest {
		state.span.SetStatus(codes.Error, "billing gate rejected request")
	} else {
		state.span.SetStatus(codes.Ok, "")
	}
	state.span.End()
}

// WithBillingSpan wraps the managed-mode billing admission checks in one
// bounded span. WithBillingSpanEnd ends it before routing starts.
func WithBillingSpan() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, state := startBillingSpan(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		c.Request = c.Request.WithContext(restoreRequestParent(c.Request.Context(), state.parent))
		finishBillingSpan(state, c.Writer.Status())
	}
}

// WithBillingSpanEnd closes the billing admission span before later handlers
// (including routing) execute, restoring the HTTP request span as parent.
func WithBillingSpanEnd() gin.HandlerFunc {
	return func(c *gin.Context) {
		state, _ := c.Request.Context().Value(billingSpanContextKey{}).(*billingSpanState)
		finishBillingSpan(state, c.Writer.Status())
		if state != nil {
			c.Request = c.Request.WithContext(restoreRequestParent(c.Request.Context(), state.parent))
		}
		c.Next()
	}
}

func restoreRequestParent(ctx, parent context.Context) context.Context {
	return trace.ContextWithSpan(ctx, trace.SpanFromContext(parent))
}
