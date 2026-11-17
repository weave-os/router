package proxy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func assertAs[T any](tb testing.TB, v any) T {
	tb.Helper()
	got, ok := v.(T)
	assert.Truef(tb, ok, "value is %T, want %T", v, got)
	return got
}
