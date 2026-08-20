package admin

import (
	"net/http"
	"time"

	"workweave/router/internal/auth"

	"github.com/gin-gonic/gin"
)

type onboardingResponse struct {
	// FirstRequestServedAt is when this installation first routed a request,
	// or null if it never has. Set once and never cleared, so it stays put
	// across key rotation — the dashboard gates first-run onboarding on it
	// rather than on any single key's last_used_at, which disappears with the
	// key that earned it.
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
