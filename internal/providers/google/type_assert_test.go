package google_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// assertAs records a failure unless v holds a T; unlike mustAs it is safe off the test goroutine.
func assertAs[T any](tb testing.TB, v any) T {
	tb.Helper()
	got, ok := v.(T)
	assert.Truef(tb, ok, "value is %T, want %T", v, got)
	return got
}
