package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCustomToolInput_ErrorsAreTyped(t *testing.T) {
	for _, arguments := range []string{`{"input":"a","extra":"b"}`, `{"other":"a"}`, `{"input":1}`} {
		_, err := customToolInput(arguments)
		assert.ErrorIs(t, err, ErrCustomToolArguments, arguments)
	}
	input, err := customToolInput(`{"input":"patch"}`)
	assert.NoError(t, err)
	assert.Equal(t, "patch", input)
}

func TestCustomToolInput_PreservesNonObjectArguments(t *testing.T) {
	for _, test := range []struct {
		arguments string
		want      string
	}{
		{arguments: "not json", want: "not json"},
		{arguments: "  raw input \n", want: "  raw input \n"},
		{arguments: `[1,2]`, want: `[1,2]`},
		{arguments: `true`, want: `true`},
		{arguments: `"text(1);"`, want: "text(1);"},
	} {
		input, err := customToolInput(test.arguments)
		assert.NoError(t, err)
		assert.Equal(t, test.want, input)
	}
}

func TestCustomToolInput_ErrorNamesKeysNotValues(t *testing.T) {
	for _, test := range []struct {
		arguments string
		want      string
	}{
		{arguments: `{}`, want: "expected exactly one input field, got keys []"},
		{arguments: `{"timeout":5,"code":"secret()"}`, want: "expected exactly one input field, got keys [code timeout]"},
		{arguments: `{"code":"secret()"}`, want: "required string field input is missing, got keys [code]"},
		{arguments: `{"secret token":"x","code":"y"}`, want: "expected exactly one input field, got keys [code <redacted>]"},
	} {
		_, err := customToolInput(test.arguments)
		assert.ErrorIs(t, err, ErrCustomToolArguments)
		assert.ErrorContains(t, err, test.want)
		assert.NotContains(t, err.Error(), "secret")
	}
}
