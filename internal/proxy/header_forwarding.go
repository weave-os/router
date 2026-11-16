package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
)

const ClaudeCodeSessionHeader = requestcontext.ClaudeCodeSessionHeader

type ForwardedHeaderSnapshotContextKey = requestcontext.ForwardedHeaderSnapshotContextKey

func WithForwardedHeaderSnapshot(ctx context.Context, keys []*auth.ExternalAPIKey, inbound http.Header) context.Context {
	return requestcontext.WithForwardedHeaderSnapshot(ctx, keys, inbound)
}

func ForwardedHeaderSnapshotFrom(ctx context.Context) http.Header {
	return requestcontext.ForwardedHeaderSnapshotFrom(ctx)
}

func ApplyForwardedClientHeaders(ctx context.Context, upstream *http.Request, inbound http.Header) {
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, inbound)
}
