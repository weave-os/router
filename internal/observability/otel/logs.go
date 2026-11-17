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

func logRecordToProto(r telemetry.LogRecord, traceID [16]byte) *logsv1.LogRecord {
	spanID := generateSpanID()
	ts := uint64(r.Time.UnixNano())
	return &logsv1.LogRecord{
		TimeUnixNano:         ts,
		ObservedTimeUnixNano: ts,
		SeverityNumber:       severityNumber(r.Severity),
		SeverityText:         severityText(r.Severity),
		Body:                 &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: r.Name}},
		Attributes:           attrsToProto(r.Attrs),
		TraceId:              traceID[:],
		SpanId:               spanID[:],
	}
}
