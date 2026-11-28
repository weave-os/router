package requestcontext

import (
	"context"
	"net/http"
)

const UpstreamGatewayHopHeader = "X-Weave-Router-Hop"

type UpstreamGateway struct {
	SessionID string
}

type UpstreamGatewaySession struct {
	Asserted string
	Headers  []string
}

type upstreamGatewayContextKey struct{}

func WithUpstreamGateway(ctx context.Context, session UpstreamGatewaySession, inbound http.Header) context.Context {
	sessionID := NormalizeClientIdentifier(session.Asserted)
	if session.Asserted == "" {
		sessionID = gatewaySessionID(session.Headers, inbound)
	}
	return context.WithValue(ctx, upstreamGatewayContextKey{}, UpstreamGateway{SessionID: sessionID})
}

func UpstreamGatewayFrom(ctx context.Context) (UpstreamGateway, bool) {
	upstreamGateway, ok := ctx.Value(upstreamGatewayContextKey{}).(UpstreamGateway)
	return upstreamGateway, ok
}

func gatewaySessionID(sessionHeaders []string, inbound http.Header) string {
	for _, name := range sessionHeaders {
		if id := NormalizeClientIdentifier(inbound.Get(name)); id != "" {
			return id
		}
	}
	return NormalizeClientIdentifier(inbound.Get(ClaudeCodeSessionHeader))
}
