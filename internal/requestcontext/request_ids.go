package requestcontext

import (
	"context"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
)

func APIKeyIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	return id
}

func OpenAIAccountIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(OpenAIAccountIDContextKey{}).(string)
	return v
}

func RequestIDFor(ctx context.Context) string {
	if id := observability.RequestIDFromContext(ctx); id != "" {
		return id
	}
	return uuid.New().String()
}
