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

// DisplaySettingsHandler returns the calling installation's display toggles (scoped to the caller by the rk_ bearer key).
func DisplaySettingsHandler(accounts SubscriptionAccountLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation := middleware.InstallationFrom(c)
		if installation == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
			return
		}
		c.JSON(http.StatusOK, displaySettingsResponse{
			HideTerminalSurfaces: installation.HideTerminalSurfaces,
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
