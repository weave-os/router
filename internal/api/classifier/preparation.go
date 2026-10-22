package classifier

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func PrepareModelHandler(service *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if c.Request.Method == http.MethodPost {
			var request *struct{}
			decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil || request == nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_preparation_request"})
				return
			}
			if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_preparation_request"})
				return
			}
		}
		preparation, err := service.PrepareRouterModel(c.Request.Context(), router.RoutingModel(c.Param("model")), c.Param("operation"))
		if err != nil {
			observability.FromGin(c).Warn("Router model preparation failed", "err", err)
			status, code := http.StatusServiceUnavailable, "model_preparation_unavailable"
			if errors.Is(err, proxy.ErrModelPreparationForbidden) {
				status, code = http.StatusForbidden, "model_preparation_not_permitted"
			} else if errors.Is(err, proxy.ErrModelPreparationOperation) {
				status, code = http.StatusConflict, "model_preparation_release_mismatch"
			}
			c.AbortWithStatusJSON(status, gin.H{"error": code})
			return
		}
		status := http.StatusOK
		if preparation.State == policy.PreparationWarming {
			status = http.StatusAccepted
			c.Header("Retry-After", "2")
		}
		c.JSON(status, preparation)
	}
}
