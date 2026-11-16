package server

import (
	"context"
	"github.com/gin-gonic/gin"
	"weave-os/router/internal/api/admin"
	anthropicapi "weave-os/router/internal/api/anthropic"
	openaiapi "weave-os/router/internal/api/openai"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/server/middleware"
)

func registerRandomResponses(engine *gin.Engine, svc *proxy.Service, authSvc *auth.Service, handlers []gin.HandlerFunc) {
	group := engine.Group("", append([]gin.HandlerFunc(nil), handlers...)...)
	group.POST("/v1/random-router/responses", openaiapi.RandomRouterGate(svc), openaiapi.ResponsesHandler(svc, authSvc))
}
func registerRandomMetadata(group *gin.RouterGroup, svc *proxy.Service) {
	group.GET("/v1/random-router/identity", openaiapi.RandomRouterIdentityHandler(svc))
	group.POST("/v1/random-router/preflight", openaiapi.RandomRouterGate(svc), openaiapi.RandomRouterIdentityHandler(svc))
}
func registerModelDiscovery(passthroughGroup *gin.RouterGroup, proxySvc *proxy.Service, authSvc *auth.Service) {
	passthroughGroup.GET("/v1/models/:model", anthropicapi.PassthroughHandler(proxySvc))
	passthroughGroup.GET("/v1/codex/models", openaiapi.CodexCatalogHandler(proxySvc.CodexSelectionCatalog))
	passthroughGroup.GET("/v1/display-settings", admin.DisplaySettingsHandler(authSvc, func(c *gin.Context) context.Context {
		ctx := requestcontext.WithClientIdentity(c.Request.Context(), proxy.ClientIdentityFromHeaders(c.Request.Header))
		return proxy.ResolveUserFromContext(ctx, authSvc, middleware.InstallationFrom(c), middleware.APIKeyFrom(c))
	}, authSvc))

}
