package billing

import "context"

type weaveCreditsDeniedKey struct{}

// WithWeaveCreditsDenied marks a request whose installation does not let
// Weave's own provider keys, paid from Weave credits, fund inference.
func WithWeaveCreditsDenied(ctx context.Context) context.Context {
	return context.WithValue(ctx, weaveCreditsDeniedKey{}, true)
}

// WeaveCreditsDeniedFromContext reports whether WithWeaveCreditsDenied marked
// the request.
func WeaveCreditsDeniedFromContext(ctx context.Context) bool {
	denied, _ := ctx.Value(weaveCreditsDeniedKey{}).(bool)
	return denied
}
