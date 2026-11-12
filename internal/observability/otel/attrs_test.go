package otel

import (
	"testing"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"

	"weave-os/router/internal/telemetry"
)

func TestAttrsToProtoPreservesScalarKindsAndZeroValues(t *testing.T) {
	attrs := []telemetry.Attr{
		{Key: "empty", Value: telemetry.NewAttrBuilder(1).String("value", "").Build()[0].Value},
		{Key: "zero-int", Value: telemetry.NewAttrBuilder(1).Int64("value", 0).Build()[0].Value},
		{Key: "zero-float", Value: telemetry.NewAttrBuilder(1).Float64("value", 0).Build()[0].Value},
		{Key: "false", Value: telemetry.NewAttrBuilder(1).Bool("value", false).Build()[0].Value},
		{Key: "empty-array", Value: telemetry.NewAttrBuilder(1).IntSlice("value", nil).Build()[0].Value},
	}

	out := attrsToProto(attrs)
	if _, ok := out[0].Value.Value.(*commonv1.AnyValue_StringValue); !ok {
		t.Fatalf("empty string has unexpected type %T", out[0].Value.Value)
	}
	if got := out[0].Value.GetStringValue(); got != "" {
		t.Fatalf("empty string = %q", got)
	}
	if _, ok := out[1].Value.Value.(*commonv1.AnyValue_IntValue); !ok {
		t.Fatalf("zero int has unexpected type %T", out[1].Value.Value)
	}
	if got := out[1].Value.GetIntValue(); got != 0 {
		t.Fatalf("zero int = %d", got)
	}
	if _, ok := out[2].Value.Value.(*commonv1.AnyValue_DoubleValue); !ok {
		t.Fatalf("zero float has unexpected type %T", out[2].Value.Value)
	}
	if got := out[2].Value.GetDoubleValue(); got != 0 {
		t.Fatalf("zero float = %v", got)
	}
	if _, ok := out[3].Value.Value.(*commonv1.AnyValue_BoolValue); !ok {
		t.Fatalf("false has unexpected type %T", out[3].Value.Value)
	}
	if got := out[3].Value.GetBoolValue(); got {
		t.Fatal("false value became true")
	}
	if _, ok := out[4].Value.Value.(*commonv1.AnyValue_ArrayValue); !ok {
		t.Fatalf("empty array has unexpected type %T", out[4].Value.Value)
	}
	if got := out[4].Value.GetArrayValue(); got == nil || len(got.Values) != 0 {
		t.Fatalf("empty array = %#v", got)
	}
}

func TestAttrsToProtoConvertsIntegerArrays(t *testing.T) {
	value := telemetry.NewAttrBuilder(1).IntSlice("ids", []int{7, 0, 42}).Build()[0].Value
	out := attrsToProto([]telemetry.Attr{{Key: "ids", Value: value}})

	array := out[0].Value.GetArrayValue()
	if array == nil || len(array.Values) != 3 {
		t.Fatalf("array = %#v", array)
	}
	for i, want := range []int64{7, 0, 42} {
		if got := array.Values[i].GetIntValue(); got != want {
			t.Fatalf("array[%d] = %d, want %d", i, got, want)
		}
		if _, ok := array.Values[i].Value.(*commonv1.AnyValue_IntValue); !ok {
			t.Fatalf("array[%d] has unexpected value type %T", i, array.Values[i].Value)
		}
	}
}
