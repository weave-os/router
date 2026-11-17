package billing

import "context"

type weaveCreditsDeniedKey struct{}

func WithWeaveCreditsDenied(ctx context.Context) context.Context {
	return context.WithValue(ctx, weaveCreditsDeniedKey{}, true)
}

func WeaveCreditsDeniedFromContext(ctx context.Context) bool {
	denied, _ := ctx.Value(weaveCreditsDeniedKey{}).(bool)
	return denied
}
