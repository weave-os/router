package otel

import (
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"

	"weave-os/router/internal/telemetry"
)

func attrsToProto(attrs []telemetry.Attr) []*commonv1.KeyValue {
	out := make([]*commonv1.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		value, ok := attrValueToProto(attr.Value)
		if !ok {
			continue
		}
		out = append(out, &commonv1.KeyValue{Key: attr.Key, Value: value})
	}
	return out
}

func attrValueToProto(value telemetry.AttrValue) (*commonv1.AnyValue, bool) {
	switch value.Kind() {
	case "string":
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value.GetStringValue()}}, true
	case "int":
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: value.GetIntValue()}}, true
	case "float":
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: value.GetDoubleValue()}}, true
	case "bool":
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: value.GetBoolValue()}}, true
	case "array":
		values := value.ArrayValues()
		array := make([]*commonv1.AnyValue, 0, len(values))
		for _, item := range values {
			converted, ok := attrValueToProto(item)
			if !ok {
				continue
			}
			array = append(array, converted)
		}
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_ArrayValue{ArrayValue: &commonv1.ArrayValue{Values: array}}}, true
	default:
		return nil, false
	}
}
