package httputil_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func mustAs[T any](tb testing.TB, v any) T {
	tb.Helper()
	got, ok := v.(T)
	require.Truef(tb, ok, "value is %T, want %T", v, got)
	return got
}
