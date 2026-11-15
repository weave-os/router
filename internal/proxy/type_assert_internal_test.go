package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustAs stops the test unless v holds a T.
func mustAs[T any](tb testing.TB, v any) T {
	tb.Helper()
	got, ok := v.(T)
	require.Truef(tb, ok, "value is %T, want %T", v, got)
	return got
}

// assertAs records a failure unless v holds a T; unlike mustAs it is safe off the test goroutine.
func assertAs[T any](tb testing.TB, v any) T {
	tb.Helper()
	got, ok := v.(T)
	assert.Truef(tb, ok, "value is %T, want %T", v, got)
	return got
}
