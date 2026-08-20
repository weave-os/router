package admin

import (
	"net/http"
	"time"

	"workweave/router/internal/auth"

	"github.com/gin-gonic/gin"
)

type onboardingResponse struct {
	// FirstRequestServedAt is when this installation first routed a request;
	// null until then. Set once and never cleared, so it survives key rotation.
	FirstRequestServedAt *time.Time `json:"first_request_served_at"`
}

// OnboardingHandler reports whether this installation has ever served a routed
// request, which is what the dashboard uses to decide between the first-run
// onboarding flow and the metrics view.
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
