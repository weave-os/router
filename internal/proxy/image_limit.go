package proxy

import (
	"context"
)

type requestImageCountContextKey struct{}

func withRequestImageCount(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, requestImageCountContextKey{}, n)
}

func requestImageCountFromContext(ctx context.Context) int {
	count, _ := ctx.Value(requestImageCountContextKey{}).(int)
	return count
}
