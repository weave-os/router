package otel

import (
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"

	"weave-os/router/internal/telemetry"
)

func severityNumber(s telemetry.Severity) logsv1.SeverityNumber {
	if s == telemetry.SeverityError {
		return logsv1.SeverityNumber_SEVERITY_NUMBER_ERROR
	}
	return logsv1.SeverityNumber_SEVERITY_NUMBER_INFO
}

func severityText(s telemetry.Severity) string {
	if s == telemetry.SeverityError {
		return "ERROR"
	}
	return "INFO"
}

// logRecordToProto converts a LogRecord into its OTLP wire form. The record
// shares the request's traceID so the collector can correlate it with the
// request's spans, and gets a fresh span ID for uniqueness.
func logRecordToProto(r telemetry.LogRecord, traceID [16]byte) *logsv1.LogRecord {
	spanID := generateSpanID()
	ts := uint64(r.Time.UnixNano())
	return &logsv1.LogRecord{
		TimeUnixNano:         ts,
		ObservedTimeUnixNano: ts,
		SeverityNumber:       severityNumber(r.Severity),
		SeverityText:         severityText(r.Severity),
		Body:                 &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: r.Name}},
		Attributes:           r.Attrs,
		TraceId:              traceID[:],
		SpanId:               spanID[:],
	}
}
