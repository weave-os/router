package server

import (
	"context"
	"time"

	"weave-os/router/internal/api/admin"
	anthropicapi "weave-os/router/internal/api/anthropic"
	openaiapi "weave-os/router/internal/api/openai"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

const codexImagesTimeout = 300 * time.Second

func registerClientPassthroughRoutes(engine *gin.Engine, authSvc *auth.Service, services Services, byokRequiresOptIn bool, servingAdmissionMiddleware []gin.HandlerFunc, features Features) {
	codexImagesGroup := engine.Group("",
		middleware.WithTimeout(codexImagesTimeout),
		middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission),
	)
	codexImagesGroup.Use(servingAdmissionMiddleware...)
	codexImagesGroup.POST("/v1/images/generations", openaiapi.ImagesHandler(ingress.CodexImageGenerations, services.CodexImages))
	codexImagesGroup.POST("/v1/images/edits", openaiapi.ImagesHandler(ingress.CodexImageEdits, services.CodexImages))

	passthroughGroup := engine.Group("",
		middleware.WithTimeout(passthroughTimeout),
		middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission),
	)
	passthroughGroup.Use(servingAdmissionMiddleware...)
	passthroughGroup.POST("/v1/messages/count_tokens", anthropicapi.PassthroughHandler(services.Passthrough, services.ErrorVocab))
	passthroughGroup.GET("/v1/models", openaiapi.ModelsHandler(anthropicapi.PassthroughHandler(services.Passthrough, services.ErrorVocab), services.CodexModelCatalog))
	passthroughGroup.GET("/v1/models/:model", anthropicapi.PassthroughHandler(services.Passthrough, services.ErrorVocab))
	passthroughGroup.GET("/v1/codex/models", openaiapi.CodexCatalogHandler(services.CodexSelectionCatalog))
	passthroughGroup.GET("/v1/display-settings", admin.DisplaySettingsHandler(authSvc, func(c *gin.Context) context.Context {
		ctx := requestcontext.WithClientIdentity(c.Request.Context(), requestcontext.ClientIdentityFromHeaders(c.Request.Header))
		return auth.ResolveUserFromContext(ctx, authSvc, middleware.InstallationFrom(c), middleware.APIKeyFrom(c), requestcontext.CallerFrom(ctx))
	}, authSvc, features.SeatUsage))
}
