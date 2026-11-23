package server

import (
	"context"
	"time"

	"weave-os/router/internal/api/admin"
	anthropicapi "weave-os/router/internal/api/anthropic"
	openaiapi "weave-os/router/internal/api/openai"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

const codexImagesTimeout = 300 * time.Second

func registerClientPassthroughRoutes(engine *gin.Engine, authSvc *auth.Service, proxySvc *proxy.Service, byokRequiresOptIn bool, servingAdmissionMiddleware []gin.HandlerFunc, features Features) {
	codexImagesGroup := engine.Group("",
		middleware.WithTimeout(codexImagesTimeout),
		middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission),
	)
	codexImagesGroup.Use(servingAdmissionMiddleware...)
	codexImages := proxy.NewCodexImages(proxySvc)
	codexImagesGroup.POST("/v1/images/generations", openaiapi.ImagesHandler(proxy.CodexImageGenerations, codexImages.ForwardWithCallerCredential))
	codexImagesGroup.POST("/v1/images/edits", openaiapi.ImagesHandler(proxy.CodexImageEdits, codexImages.ForwardWithCallerCredential))

	passthroughGroup := engine.Group("",
		middleware.WithTimeout(passthroughTimeout),
		middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission),
	)
	passthroughGroup.Use(servingAdmissionMiddleware...)
	passthroughGroup.POST("/v1/messages/count_tokens", anthropicapi.PassthroughHandler(proxySvc, features.ErrorVocab))
	passthroughGroup.GET("/v1/models", openaiapi.ModelsHandler(anthropicapi.PassthroughHandler(proxySvc, features.ErrorVocab), proxySvc.CodexModelCatalog))
	passthroughGroup.GET("/v1/models/:model", anthropicapi.PassthroughHandler(proxySvc, features.ErrorVocab))
	passthroughGroup.GET("/v1/codex/models", openaiapi.CodexCatalogHandler(proxySvc.CodexSelectionCatalog))
	passthroughGroup.GET("/v1/display-settings", admin.DisplaySettingsHandler(authSvc, func(c *gin.Context) context.Context {
		ctx := requestcontext.WithClientIdentity(c.Request.Context(), requestcontext.ClientIdentityFromHeaders(c.Request.Header))
		return auth.ResolveUserFromContext(ctx, authSvc, middleware.InstallationFrom(c), middleware.APIKeyFrom(c), requestcontext.CallerFrom(ctx))
	}, authSvc, features.SeatUsage))
}
