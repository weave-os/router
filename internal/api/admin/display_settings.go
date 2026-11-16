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

type displaySettingsResponse struct {
	HideTerminalSurfaces bool                        `json:"hide_terminal_surfaces"`
	DisconnectedPlans    []auth.SubscriptionProvider `json:"disconnected_plans"`
}

type SubscriptionAccountLister interface {
	ListSubscriptionAccounts(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
}

type CallerResolver func(c *gin.Context) context.Context

type InstallationExperimentChecker interface {
	InstallationBlindExperimentEnabled(ctx context.Context, installationID string) bool
}

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
