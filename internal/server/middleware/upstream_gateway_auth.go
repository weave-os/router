package middleware

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/requestcontext"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
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

func refuseUpstreamGateway(c *gin.Context, principal *auth.UpstreamGatewayPrincipal, authSpan trace.Span) bool {
	if principal == nil {
		return false
	}
	if kind, refused := upstreamGatewayRefusal(c); refused {
		finishAuthSpan(authSpan, nil)
		abortRefusal(c, kind, nil)
		return true
	}
	stripInboundCredentials(c.Request.Header)
	return false
}

func withUpstreamGatewayMode(ctx context.Context, principal *auth.UpstreamGatewayPrincipal, inbound http.Header) context.Context {
	if principal == nil {
		return ctx
	}
	return requestcontext.WithUpstreamGateway(ctx, principal.SessionHeaders, inbound)
}
