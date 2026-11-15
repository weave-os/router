package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustAs[T any](tb testing.TB, v any) T {
	tb.Helper()
	got, ok := v.(T)
	require.Truef(tb, ok, "value is %T, want %T", v, got)
	return got
}

func assertAs[T any](tb testing.TB, v any) T {
	tb.Helper()
	got, ok := v.(T)
	assert.Truef(tb, ok, "value is %T, want %T", v, got)
	return got
}
