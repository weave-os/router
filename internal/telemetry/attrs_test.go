package telemetry_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/telemetry"
)

func TestAttrBuilder_BuildsCorrectKeyValues(t *testing.T) {
	b := telemetry.NewAttrBuilder(5)
	attrs := b.
		String("str_key", "value").
		Int64("int_key", 42).
		Float64("float_key", 3.14).
		Bool("bool_key", true).
		String("str_key2", "").
		Build()

	assert.Len(t, attrs, 5)

	assert.Equal(t, "str_key", attrs[0].Key)
	assert.Equal(t, "value", attrs[0].Value.GetStringValue())

	assert.Equal(t, "int_key", attrs[1].Key)
	assert.Equal(t, int64(42), attrs[1].Value.GetIntValue())

	assert.Equal(t, "float_key", attrs[2].Key)
	assert.Equal(t, 3.14, attrs[2].Value.GetDoubleValue())

	assert.Equal(t, "bool_key", attrs[3].Key)
	assert.True(t, attrs[3].Value.GetBoolValue())

	assert.Equal(t, "str_key2", attrs[4].Key)
	assert.Equal(t, "", attrs[4].Value.GetStringValue())
}

func TestAttrBuilder_PreSizedCapacity(t *testing.T) {
	b := telemetry.NewAttrBuilder(2)
	attrs := b.String("a", "1").String("b", "2").Build()
	assert.Len(t, attrs, 2)
}

func TestAttrBuilder_ChainableAPI(t *testing.T) {
	b := telemetry.NewAttrBuilder(3)
	result := b.String("a", "1").Int64("b", 2).Bool("c", false)
	assert.IsType(t, &telemetry.AttrBuilder{}, result)

	attrs := result.Build()
	assert.Len(t, attrs, 3)
}

func TestAttrBuilder_EmptyBuildReturnsEmptySlice(t *testing.T) {
	b := telemetry.NewAttrBuilder(0)
	attrs := b.Build()
	assert.NotNil(t, attrs)
	assert.Len(t, attrs, 0)
}

func TestAttrBuilder_TypedValuesMatchProtobuf(t *testing.T) {
	b := telemetry.NewAttrBuilder(4)
	attrs := b.
		String("s", "test").
		Int64("i", -100).
		Float64("f", -2.5).
		Bool("b", false).
		Build()

	stringVal := attrs[0].Value
	assert.Equal(t, "test", stringVal.GetStringValue())
	assert.Equal(t, "string", stringVal.Kind())

	intVal := attrs[1].Value
	assert.Equal(t, int64(-100), intVal.GetIntValue())
	assert.Equal(t, "int", intVal.Kind())

	floatVal := attrs[2].Value
	assert.Equal(t, -2.5, floatVal.GetDoubleValue())
	assert.Equal(t, "float", floatVal.Kind())

	boolVal := attrs[3].Value
	assert.False(t, boolVal.GetBoolValue())
	assert.Equal(t, "bool", boolVal.Kind())
}

func TestAttrBuilder_IntSliceEmitsArrayOfInts(t *testing.T) {
	attrs := telemetry.NewAttrBuilder(1).
		IntSlice("routing.cluster_ids", []int{7, 0, 42}).
		Build()

	assert.Len(t, attrs, 1)
	assert.Equal(t, "routing.cluster_ids", attrs[0].Key)
	assert.Equal(t, "array", attrs[0].Value.Kind())

	arr := attrs[0].Value.ArrayValues()
	assert.Len(t, arr, 3)
	assert.Equal(t, int64(7), arr[0].GetIntValue())
	assert.Equal(t, int64(0), arr[1].GetIntValue())
	assert.Equal(t, int64(42), arr[2].GetIntValue())
	for _, v := range arr {
		assert.Equal(t, "int", v.Kind())
	}
}

func TestAttrBuilder_IntSliceNilEmitsEmptyArray(t *testing.T) {
	attrs := telemetry.NewAttrBuilder(1).
		IntSlice("routing.cluster_ids", nil).
		Build()

	assert.Len(t, attrs, 1)
	assert.Equal(t, "routing.cluster_ids", attrs[0].Key)
	arr := attrs[0].Value.ArrayValues()
	assert.NotNil(t, arr)
	assert.Len(t, arr, 0)
}
