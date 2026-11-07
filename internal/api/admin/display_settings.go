package admin

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"time"

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
	PlanStatuses      []displayPlanStatus         `json:"plan_statuses"`
}

type displayPlanStatus struct {
	Provider      auth.SubscriptionProvider     `json:"provider"`
	State         auth.SubscriptionAccountState `json:"state"`
	CooldownUntil *time.Time                    `json:"cooldown_until,omitempty"`
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
		plans := linkedPlanStatuses(c, accounts)
		disconnected := []auth.SubscriptionProvider{}
		for _, plan := range plans {
			if plan.State == auth.SubscriptionAccountStateReconnectRequired {
				disconnected = append(disconnected, plan.Provider)
			}
		}
		c.JSON(http.StatusOK, displaySettingsResponse{
			HideTerminalSurfaces: installation.HideTerminalSurfaces,
			DisconnectedPlans:    disconnected,
			PlanStatuses:         plans,
		})
	}
}

// A linked replacement or exhausted plan must supersede an old account needing login.
func linkedPlanStatuses(c *gin.Context, accounts SubscriptionAccountLister) []displayPlanStatus {
	plans := []displayPlanStatus{}
	owner := middleware.SubscriptionOwnerFrom(c)
	if !owner.Valid() {
		return plans
	}
	linked, err := accounts.ListSubscriptionAccounts(c.Request.Context(), owner)
	if err != nil {
		observability.FromContext(c.Request.Context()).Warn("Failed to list linked plans for display settings", "err", err)
		return plans
	}
	statePrecedence := []auth.SubscriptionAccountState{
		auth.SubscriptionAccountStateActive,
		auth.SubscriptionAccountStateUnknown,
		auth.SubscriptionAccountStateCooldown,
		auth.SubscriptionAccountStateExhausted,
		auth.SubscriptionAccountStateReconnectRequired,
		auth.SubscriptionAccountStateDisabled,
	}
	for _, account := range linked {
		plan := displayPlanStatus{Provider: account.Provider, State: account.State, CooldownUntil: account.CooldownUntil}
		if !account.Enabled && account.State != auth.SubscriptionAccountStateReconnectRequired {
			plan.State = auth.SubscriptionAccountStateDisabled
			plan.CooldownUntil = nil
		}
		index := slices.IndexFunc(plans, func(existing displayPlanStatus) bool { return existing.Provider == plan.Provider })
		if index < 0 {
			plans = append(plans, plan)
			continue
		}
		existing := plans[index]
		if slices.Index(statePrecedence, plan.State) < slices.Index(statePrecedence, existing.State) ||
			(plan.State == existing.State && plan.CooldownUntil != nil &&
				(existing.CooldownUntil == nil || plan.CooldownUntil.Before(*existing.CooldownUntil))) {
			plans[index] = plan
		}
	}
	slices.SortFunc(plans, func(a, b displayPlanStatus) int { return cmp.Compare(a.Provider, b.Provider) })
	return plans
}
