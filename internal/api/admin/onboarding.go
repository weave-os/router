package admin

import (
	"net/http"
	"time"

	"weave-os/router/internal/auth"

	"github.com/gin-gonic/gin"
)

type onboardingResponse struct {
	FirstRequestServedAt *time.Time `json:"first_request_served_at"`
}

func OnboardingHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, onboardingResponse{
			FirstRequestServedAt: installation.FirstRequestServedAt,
		})
	}
}
