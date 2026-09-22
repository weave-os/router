package admin

import (
	"errors"
	"net/http"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type internalSubscriptionAccountResponse struct {
	ID                string                        `json:"id"`
	Provider          auth.SubscriptionProvider     `json:"provider"`
	ExternalAccountID string                        `json:"external_account_id"`
	DisplayName       string                        `json:"display_name,omitempty"`
	Enabled           bool                          `json:"enabled"`
	State             auth.SubscriptionAccountState `json:"state"`
	CooldownUntil     *time.Time                    `json:"cooldown_until,omitempty"`
	CreatedAt         time.Time                     `json:"created_at"`
}

type internalUpdateSubscriptionAccountRequest struct {
	Enabled *bool `json:"enabled"`
}

// InternalListSubscriptionAccountsHandler exposes credential-free account
// health to the Weave control plane.
func InternalListSubscriptionAccountsHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := internalSubscriptionOwner(c)
		if !ok {
			return
		}
		accounts, err := authSvc.ListSubscriptionAccounts(c.Request.Context(), owner)
		if err != nil {
			observability.FromGin(c).Error("Failed to list subscription accounts", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_accounts_unavailable"})
			return
		}
		response := make([]internalSubscriptionAccountResponse, 0, len(accounts))
		for _, account := range accounts {
			response = append(response, internalSubscriptionAccountResponse{
				ID:                account.ID,
				Provider:          account.Provider,
				ExternalAccountID: account.ExternalAccountID,
				DisplayName:       account.DisplayName,
				Enabled:           account.Enabled,
				State:             account.State,
				CooldownUntil:     account.CooldownUntil,
				CreatedAt:         account.CreatedAt,
			})
		}
		c.JSON(http.StatusOK, response)
	}
}

// InternalUpdateSubscriptionAccountHandler enables or disables one account
// owned by the requested subscriber.
func InternalUpdateSubscriptionAccountHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := internalSubscriptionOwner(c)
		if !ok {
			return
		}
		accountID := c.Param("accountID")
		if _, err := uuid.Parse(accountID); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_subscription_account"})
			return
		}
		var request internalUpdateSubscriptionAccountRequest
		if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "enabled_is_required"})
			return
		}
		err := authSvc.UpdateSubscriptionAccountState(c.Request.Context(), owner, accountID, *request.Enabled, nil)
		if errors.Is(err, auth.ErrSubscriptionAccountNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "subscription_account_not_found"})
			return
		}
		if err != nil {
			observability.FromGin(c).Error("Failed to update subscription account", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_account_update_failed"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// InternalDeleteSubscriptionAccountHandler removes one account owned by the
// requested subscriber.
func InternalDeleteSubscriptionAccountHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := internalSubscriptionOwner(c)
		if !ok {
			return
		}
		accountID := c.Param("accountID")
		if _, err := uuid.Parse(accountID); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_subscription_account"})
			return
		}
		err := authSvc.DeleteSubscriptionAccount(c.Request.Context(), owner, accountID)
		if errors.Is(err, auth.ErrSubscriptionAccountNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "subscription_account_not_found"})
			return
		}
		if err != nil {
			observability.FromGin(c).Error("Failed to delete subscription account", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_account_delete_failed"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func internalSubscriptionOwner(c *gin.Context) (auth.SubscriptionOwner, bool) {
	subscriberID := c.Param("subscriberID")
	if _, err := uuid.Parse(subscriberID); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_subscriber"})
		return auth.SubscriptionOwner{}, false
	}
	return auth.SubscriptionOwner{SubscriberID: subscriberID}, true
}
