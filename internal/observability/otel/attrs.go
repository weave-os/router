package otel

import (
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"

	"weave-os/router/internal/telemetry"
)

func attrsToProto(attrs []telemetry.Attr) []*commonv1.KeyValue {
	out := make([]*commonv1.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		out = append(out, &commonv1.KeyValue{Key: attr.Key, Value: attrValueToProto(attr.Value)})
	}
	return out
}

func attrValueToProto(value telemetry.AttrValue) *commonv1.AnyValue {
	switch value.Kind() {
	case "string":
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value.GetStringValue()}}
	case "int":
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: value.GetIntValue()}}
	case "float":
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: value.GetDoubleValue()}}
	case "bool":
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: value.GetBoolValue()}}
	case "array":
		values := value.ArrayValues()
		array := make([]*commonv1.AnyValue, 0, len(values))
		for _, item := range values {
			array = append(array, attrValueToProto(item))
		}
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_ArrayValue{ArrayValue: &commonv1.ArrayValue{Values: array}}}
	default:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: ""}}
	}
}
