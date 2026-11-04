// Package telemetry is the inner-ring port for per-request spans, log records
// and usage extraction; internal/observability/otel implements Recorder and is
// wired in cmd/router.
package telemetry

import commonv1 "go.opentelemetry.io/proto/otlp/common/v1"

// AttrBuilder constructs OTLP KeyValue attributes directly without intermediate
// map allocations. Not safe for concurrent use.
type AttrBuilder struct {
	attrs []*commonv1.KeyValue
}

// NewAttrBuilder returns a builder pre-sized for cap attributes.
func NewAttrBuilder(cap int) *AttrBuilder {
	return &AttrBuilder{attrs: make([]*commonv1.KeyValue, 0, cap)}
}

func (b *AttrBuilder) String(key, val string) *AttrBuilder {
	b.attrs = append(b.attrs, &commonv1.KeyValue{
		Key:   key,
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: val}},
	})
	return b
}

func (b *AttrBuilder) Int64(key string, val int64) *AttrBuilder {
	b.attrs = append(b.attrs, &commonv1.KeyValue{
		Key:   key,
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: val}},
	})
	return b
}

func (b *AttrBuilder) Float64(key string, val float64) *AttrBuilder {
	b.attrs = append(b.attrs, &commonv1.KeyValue{
		Key:   key,
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: val}},
	})
	return b
}

func (b *AttrBuilder) Bool(key string, val bool) *AttrBuilder {
	b.attrs = append(b.attrs, &commonv1.KeyValue{
		Key:   key,
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: val}},
	})
	return b
}

// IntSlice appends an int slice as an OTLP array of int64 values. A nil or
// empty slice still emits the attribute with an empty array, so downstream
// consumers can distinguish "field present, no clusters" from "field absent"
// (e.g. pinned-route turns where Metadata is nil).
func (b *AttrBuilder) IntSlice(key string, vals []int) *AttrBuilder {
	values := make([]*commonv1.AnyValue, len(vals))
	for i, v := range vals {
		values[i] = &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: int64(v)}}
	}
	b.attrs = append(b.attrs, &commonv1.KeyValue{
		Key:   key,
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_ArrayValue{ArrayValue: &commonv1.ArrayValue{Values: values}}},
	})
	return b
}

// Build returns the accumulated KeyValue slice. The slice is valid until the
// next method call on the builder.
func (b *AttrBuilder) Build() []*commonv1.KeyValue {
	return b.attrs
}
