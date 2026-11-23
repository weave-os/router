package requestcontext

import (
	"context"
	"net/http"
)

const UpstreamGatewayHopHeader = "X-Weave-Router-Hop"

type UpstreamGateway struct {
	SessionID   string
	Email       string
	DisplayName string
}

type UpstreamGatewayHeaders struct {
	Session   []string
	UserEmail []string
	UserName  []string
}

type upstreamGatewayContextKey struct{}

func WithUpstreamGateway(ctx context.Context, headers UpstreamGatewayHeaders, inbound http.Header) context.Context {
	upstreamGateway := UpstreamGateway{
		SessionID: gatewaySessionID(headers.Session, inbound),
		Email:     firstHeader(headers.UserEmail, inbound, NormalizeEmail),
	}
	if upstreamGateway.Email != "" {
		upstreamGateway.DisplayName = firstHeader(headers.UserName, inbound, NormalizeDisplayName)
	}
	return context.WithValue(ctx, upstreamGatewayContextKey{}, upstreamGateway)
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

func firstHeader(names []string, inbound http.Header, normalize func(string) string) string {
	for _, name := range names {
		if value := normalize(inbound.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
