package translate

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCustomToolInput_ErrorsAreTyped(t *testing.T) {
	for _, arguments := range []string{`not json`, `{"input":"a","extra":"b"}`, `{"other":"a"}`, `{"input":1}`} {
		_, err := customToolInput(arguments)
		assert.True(t, errors.Is(err, ErrCustomToolArguments), arguments)
	}
	input, err := customToolInput(`{"input":"patch"}`)
	assert.NoError(t, err)
	assert.Equal(t, "patch", input)
}
