package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
)

// ClaudeCodeSessionHeader carries Claude Code's session id.
const ClaudeCodeSessionHeader = requestcontext.ClaudeCodeSessionHeader

// ForwardedHeaderSnapshotContextKey is the request-context key for the inbound
// correlation headers captured at ingress.
type ForwardedHeaderSnapshotContextKey = requestcontext.ForwardedHeaderSnapshotContextKey

// WithForwardedHeaderSnapshot captures inbound values of every header the
// installation's external keys forward.
func WithForwardedHeaderSnapshot(ctx context.Context, keys []*auth.ExternalAPIKey, inbound http.Header) context.Context {
	return requestcontext.WithForwardedHeaderSnapshot(ctx, keys, inbound)
}

// ForwardedHeaderSnapshotFrom reads the ingress header snapshot stashed on ctx.
func ForwardedHeaderSnapshotFrom(ctx context.Context) http.Header {
	return requestcontext.ForwardedHeaderSnapshotFrom(ctx)
}

// ApplyForwardedClientHeaders copies configured inbound headers and re-emits the baggage header
// with the resolved email.
func ApplyForwardedClientHeaders(ctx context.Context, upstream *http.Request, inbound http.Header) {
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, inbound)
}
