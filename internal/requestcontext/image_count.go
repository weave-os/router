package requestcontext

import (
	"context"
)

type requestImageCountContextKey struct{}

func WithRequestImageCount(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, requestImageCountContextKey{}, n)
}

func RequestImageCountFromContext(ctx context.Context) int {
	count, _ := ctx.Value(requestImageCountContextKey{}).(int)
	return count
}
