package requestcontext

import (
	"context"
	"net/http"
)

const UpstreamGatewayHopHeader = "X-Weave-Router-Hop"

type UpstreamGateway struct {
	SessionID string
}

type upstreamGatewayContextKey struct{}

func WithUpstreamGateway(ctx context.Context, sessionHeaders []string, inbound http.Header) context.Context {
	return context.WithValue(ctx, upstreamGatewayContextKey{}, UpstreamGateway{SessionID: gatewaySessionID(sessionHeaders, inbound)})
}

func UpstreamGatewayFrom(ctx context.Context) (UpstreamGateway, bool) {
	gw, ok := ctx.Value(upstreamGatewayContextKey{}).(UpstreamGateway)
	return gw, ok
}

func IsUpstreamGatewayHop(inbound http.Header) bool {
	return inbound.Get(UpstreamGatewayHopHeader) != ""
}

func gatewaySessionID(sessionHeaders []string, inbound http.Header) string {
	for _, name := range sessionHeaders {
		if v := inbound.Get(name); v != "" {
			return v
		}
	}
	return inbound.Get(ClaudeCodeSessionHeader)
}
