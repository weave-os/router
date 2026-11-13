package observability

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	gcpTraceField = "logging.googleapis.com/trace"
	gcpSpanField  = "logging.googleapis.com/spanId"
)

type clientSessionIDContextKey struct{}

// WithClientSessionID attaches the caller's session identifier to ctx for
// request-scoped trace instrumentation.
func WithClientSessionID(ctx context.Context, clientSessionID string) context.Context {
	if clientSessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, clientSessionIDContextKey{}, clientSessionID)
}

// ClientSessionIDFromContext returns the client session identifier attached
// for request-scoped trace instrumentation, or "" when none is known.
func ClientSessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	clientSessionID, _ := ctx.Value(clientSessionIDContextKey{}).(string)
	return clientSessionID
}

// LoggerWithTraceContext adds the active OpenTelemetry trace and span IDs to a
// logger using the field names Cloud Logging recognizes for trace correlation.
// When GCP_PROJECT_ID is set, the trace field is the fully-qualified resource
// name expected by Logs Explorer.
func LoggerWithTraceContext(ctx context.Context, log *slog.Logger) *slog.Logger {
	if log == nil {
		return nil
	}
	if ctx == nil {
		return log
	}
	spanCtx := oteltrace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return log
	}

	traceID := spanCtx.TraceID().String()
	if projectID := strings.TrimSpace(os.Getenv("GCP_PROJECT_ID")); projectID != "" {
		traceID = fmt.Sprintf("projects/%s/traces/%s", projectID, traceID)
	}
	log = log.With(gcpTraceField, traceID)
	if spanCtx.HasSpanID() {
		log = log.With(gcpSpanField, spanCtx.SpanID().String())
	}
	return log
}
