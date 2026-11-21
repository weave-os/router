package middleware

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/requestcontext"

	"github.com/gin-gonic/gin"
)

const upstreamGatewayRoute = "/v1/messages"

func upstreamGatewayRefusal(c *gin.Context) (refusal.Kind, bool) {
	if c.FullPath() != upstreamGatewayRoute {
		return refusal.UpstreamGatewaySurfaceRejected, true
	}
	if requestcontext.IsUpstreamGatewayHop(c.Request.Header) {
		return refusal.UpstreamGatewayLoopDetected, true
	}
	return 0, false
}

func stripInboundCredentials(headers http.Header) {
	headers.Del("Authorization")
	headers.Del("x-api-key")
	headers.Del(auth.RouterKeyHeader)
}

func withUpstreamGatewayMode(ctx context.Context, principal *auth.UpstreamGatewayPrincipal, inbound http.Header) context.Context {
	ctx = requestcontext.WithUpstreamGateway(ctx, principal.SessionHeaders, inbound)
	return context.WithValue(ctx, requestcontext.InstallationHideTerminalSurfacesContextKey{}, true)
}
