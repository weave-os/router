package server

import (
	"github.com/gin-gonic/gin"
	openaiapi "weave-os/router/internal/api/openai"
	"weave-os/router/internal/proxy"
)

func registerRandomMetadata(group *gin.RouterGroup, svc *proxy.Service) {
	group.GET("/v1/router/models/weave-random/readiness", openaiapi.RandomRouterIdentityHandler(svc))
}
