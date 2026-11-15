package otel

import (
	"crypto/rand"
	mathrand "math/rand/v2"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/telemetry"
)

var cryptoRandRead = rand.Read

func generateTraceID() [16]byte {
	var id [16]byte
	if _, err := cryptoRandRead(id[:]); err != nil {
		observability.Get().Warn("OTel: crypto/rand failed generating trace ID; falling back to math/rand/v2", "err", err)
		fillMathRand(id[:])
	}
	return id
}

func generateSpanID() [8]byte {
	var id [8]byte
	if _, err := cryptoRandRead(id[:]); err != nil {
		observability.Get().Warn("OTel: crypto/rand failed generating span ID; falling back to math/rand/v2", "err", err)
		fillMathRand(id[:])
	}
	return id
}

func fillMathRand(buf []byte) {
	for i := range buf {
		buf[i] = byte(mathrand.IntN(256))
	}
}

func spanToProto(s telemetry.Span, traceID [16]byte) *tracev1.Span {
	spanID := generateSpanID()
	return &tracev1.Span{
		TraceId:           traceID[:],
		SpanId:            spanID[:],
		Name:              s.Name,
		StartTimeUnixNano: uint64(s.Start.UnixNano()),
		EndTimeUnixNano:   uint64(s.End.UnixNano()),
		Attributes:        attrsToProto(s.Attrs),
		Kind:              tracev1.Span_SPAN_KIND_INTERNAL,
	}
}

func buildResource(serviceName string, extraAttrs map[string]string) *resourcev1.Resource {
	kvs := make([]*commonv1.KeyValue, 0, 1+len(extraAttrs))
	kvs = append(kvs, &commonv1.KeyValue{
		Key:   "service.name",
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: serviceName}},
	})
	for k, v := range extraAttrs {
		kvs = append(kvs, &commonv1.KeyValue{
			Key:   k,
			Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}},
		})
	}
	return &resourcev1.Resource{Attributes: kvs}
}
