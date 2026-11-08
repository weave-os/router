package admin

import (
	"context"
	"net/http"
	"slices"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// displaySettingsResponse is the per-installation toggle set read by the installer and statusline.
type displaySettingsResponse struct {
	HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
	// DisconnectedPlans lists the providers of the caller's linked plans that need a fresh login.
	DisconnectedPlans []auth.SubscriptionProvider `json:"disconnected_plans"`
}

// SubscriptionAccountLister lists the linked plans a subscription owner draws on.
type SubscriptionAccountLister interface {
	ListSubscriptionAccounts(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
}

// CallerResolver attributes a display-settings request to its router user, the same way inference requests are.
type CallerResolver func(c *gin.Context) context.Context

// InstallationExperimentChecker reports whether an installation has an enabled blind experiment.
type InstallationExperimentChecker interface {
	InstallationBlindExperimentEnabled(ctx context.Context, installationID string) bool
}

// DisplaySettingsHandler returns the calling installation's display toggles (scoped to the caller by the rk_ bearer key).
// Blind experiment subjects always get hidden surfaces so neither arm learns it is being routed. A caller
// that cannot be attributed to a router user is hidden whenever the installation runs an experiment,
// because inference may still identify it as a subject from the request body.
func DisplaySettingsHandler(accounts SubscriptionAccountLister, resolveCaller CallerResolver, experiments InstallationExperimentChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation := middleware.InstallationFrom(c)
		if installation == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
			return
		}
		hide := installation.HideTerminalSurfaces
		if !hide && resolveCaller != nil {
			callerCtx := resolveCaller(c)
			_, hide = auth.BlindExperimentFrom(callerCtx)
			if !hide && auth.UserIDFrom(callerCtx) == "" && experiments != nil {
				hide = experiments.InstallationBlindExperimentEnabled(callerCtx, installation.ID)
			}
		}
		c.JSON(http.StatusOK, displaySettingsResponse{
			HideTerminalSurfaces: hide,
			DisconnectedPlans:    disconnectedPlans(c, accounts),
		})
	}
}

// disconnectedPlans fails open to an empty list: a missing warning must not break the display toggles.
func disconnectedPlans(c *gin.Context, accounts SubscriptionAccountLister) []auth.SubscriptionProvider {
	providers := []auth.SubscriptionProvider{}
	owner := middleware.SubscriptionOwnerFrom(c)
	if !owner.Valid() {
		return providers
	}
	linked, err := accounts.ListSubscriptionAccounts(c.Request.Context(), owner)
	if err != nil {
		observability.FromContext(c.Request.Context()).Warn("Failed to list linked plans for display settings", "err", err)
		return providers
	}
	for _, account := range linked {
		if account.State == auth.SubscriptionAccountStateReconnectRequired && !slices.Contains(providers, account.Provider) {
			providers = append(providers, account.Provider)
		}
	}
	slices.Sort(providers)
	return providers
}
