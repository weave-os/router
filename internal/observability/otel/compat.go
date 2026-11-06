package otel

import (
	"net/http"

	"weave-os/router/internal/telemetry"
)

// These aliases preserve callers of the observability adapter.
type Span = telemetry.Span
type AttrBuilder = telemetry.AttrBuilder
type Severity = telemetry.Severity
type LogRecord = telemetry.LogRecord
type UsageExtractor = telemetry.UsageExtractor

const (
	SeverityInfo  = telemetry.SeverityInfo
	SeverityError = telemetry.SeverityError
)

func NewAttrBuilder(capacity int) *AttrBuilder {
	return telemetry.NewAttrBuilder(capacity)
}

func NewUsageExtractor(inner http.ResponseWriter, provider string) *UsageExtractor {
	return telemetry.NewUsageExtractor(inner, provider)
}
