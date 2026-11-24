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

var upstreamGatewayRoutes = map[string]struct{}{"/v1/messages": {}, "/v1/messages/count_tokens": {}}

func upstreamGatewayRefusal(c *gin.Context) (refusal.Kind, bool) {
	if _, allowed := upstreamGatewayRoutes[c.FullPath()]; !allowed {
		return refusal.UpstreamGatewaySurfaceRejected, true
	}
	if c.Request.Header.Get(requestcontext.UpstreamGatewayHopHeader) != "" {
		return refusal.UpstreamGatewayLoopDetected, true
	}
	return 0, false
}

func stripInboundCredentials(headers http.Header) {
	headers.Del("Authorization")
	headers.Del("x-api-key")
	headers.Del(auth.RouterKeyHeader)
	headers.Del("X-Weave-User-Email")
	headers.Del("X-Weave-User-Name")
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
	return requestcontext.WithUpstreamGateway(ctx, requestcontext.UpstreamGatewaySession{
		Asserted: principal.SessionID,
		Headers:  principal.SessionHeaders,
	}, inbound)
}
