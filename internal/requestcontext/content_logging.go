package requestcontext

import "context"

type contentLoggingKey struct{}

func WithContentLogging(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, contentLoggingKey{}, allowed)
}

func ContentLoggingAllowed(ctx context.Context) bool {
	allowed, ok := ctx.Value(contentLoggingKey{}).(bool)
	return !ok || allowed
}
