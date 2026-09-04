package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	gcpTraceField = "logging.googleapis.com/trace"
	gcpSpanField  = "logging.googleapis.com/spanId"
)

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

// InjectTraceContext propagates the active OpenTelemetry context into an
// outbound HTTP request. It emits the globally configured W3C Trace Context
// and Baggage headers (typically traceparent, tracestate, and baggage).
func InjectTraceContext(ctx context.Context, req *http.Request) {
	if req == nil || ctx == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}
