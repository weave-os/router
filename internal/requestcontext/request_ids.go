package requestcontext

import "context"

func APIKeyIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	return id
}

func OpenAIAccountIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(OpenAIAccountIDContextKey{}).(string)
	return v
}
