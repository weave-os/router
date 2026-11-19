package admin

import (
	"context"
	"net/http"
	"slices"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

type displaySettingsResponse struct {
	HideTerminalSurfaces bool                        `json:"hide_terminal_surfaces"`
	DisconnectedPlans    []auth.SubscriptionProvider `json:"disconnected_plans"`
	OwnSeats             []ownSeatResponse           `json:"own_seats"`
}

type ownSeatResponse struct {
	Provider               string    `json:"provider"`
	PrimaryUsedPercent     float64   `json:"primary_used_percent"`
	PrimaryWindowMinutes   int       `json:"primary_window_minutes"`
	SecondaryUsedPercent   float64   `json:"secondary_used_percent"`
	SecondaryWindowMinutes int       `json:"secondary_window_minutes"`
	OverageInUse           bool      `json:"overage_in_use"`
	ObservedAt             time.Time `json:"observed_at"`
}

type SeatUsageReader interface {
	ListSeatUsage(ctx context.Context, routerUserID string) ([]usage.SeatUsage, error)
}

type SubscriptionAccountLister interface {
	ListSubscriptionAccounts(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
}

type CallerResolver func(c *gin.Context) context.Context

type InstallationExperimentChecker interface {
	InstallationBlindExperimentEnabled(ctx context.Context, installationID string) bool
}

func DisplaySettingsHandler(accounts SubscriptionAccountLister, resolveCaller CallerResolver, experiments InstallationExperimentChecker, seats SeatUsageReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation := middleware.InstallationFrom(c)
		if installation == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
			return
		}
		hide := installation.HideTerminalSurfaces
		callerCtx := c.Request.Context()
		if resolveCaller != nil {
			callerCtx = resolveCaller(c)
		}
		if !hide && resolveCaller != nil {
			_, hide = auth.BlindExperimentFrom(callerCtx)
			if !hide && auth.UserIDFrom(callerCtx) == "" && experiments != nil {
				hide = experiments.InstallationBlindExperimentEnabled(callerCtx, installation.ID)
			}
		}
		c.JSON(http.StatusOK, displaySettingsResponse{
			HideTerminalSurfaces: hide,
			DisconnectedPlans:    disconnectedPlans(c, accounts),
			OwnSeats:             ownSeats(callerCtx, seats),
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

func ownSeats(ctx context.Context, seats SeatUsageReader) []ownSeatResponse {
	response := []ownSeatResponse{}
	userID := auth.UserIDFrom(ctx)
	if seats == nil || userID == "" {
		return response
	}
	observed, err := seats.ListSeatUsage(ctx, userID)
	if err != nil {
		observability.FromContext(ctx).Warn("Failed to list own seat usage for display settings", "err", err)
		return response
	}
	for _, seat := range observed {
		response = append(response, ownSeatResponse{
			Provider:               seat.Provider,
			PrimaryUsedPercent:     seat.Snapshot.Primary.UsedPercent * 100,
			PrimaryWindowMinutes:   seat.Snapshot.Primary.WindowMinutes,
			SecondaryUsedPercent:   seat.Snapshot.Secondary.UsedPercent * 100,
			SecondaryWindowMinutes: seat.Snapshot.Secondary.WindowMinutes,
			OverageInUse:           seat.Snapshot.OverageInUse,
			ObservedAt:             seat.Snapshot.ObservedAt,
		})
	}
	return response
}
