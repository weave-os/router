// Package subscriptions exposes authenticated subscription-account management.
package subscriptions

import (
	"errors"
	"net/http"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

type accountResponse struct {
	ID                string                        `json:"id"`
	Provider          auth.SubscriptionProvider     `json:"provider"`
	ExternalAccountID string                        `json:"external_account_id"`
	DisplayName       string                        `json:"display_name,omitempty"`
	Enabled           bool                          `json:"enabled"`
	State             auth.SubscriptionAccountState `json:"state"`
	CooldownUntil     *time.Time                    `json:"cooldown_until,omitempty"`
	CreatedAt         time.Time                     `json:"created_at"`
}

type createAccountRequest struct {
	Provider          auth.SubscriptionProvider `json:"provider" binding:"required"`
	ExternalAccountID string                    `json:"external_account_id" binding:"required"`
	DisplayName       string                    `json:"display_name,omitempty"`
	RefreshToken      string                    `json:"refresh_token" binding:"required"`
}

type updateAccountRequest struct {
	Enabled       *bool      `json:"enabled"`
	CooldownUntil *time.Time `json:"cooldown_until"`
}

// Register mounts account management endpoints on an authenticated group.
func Register(group *gin.RouterGroup, authSvc *auth.Service) {
	group.GET("/subscriptions/accounts", listAccountsHandler(authSvc))
	group.POST("/subscriptions/accounts", createAccountHandler(authSvc))
	group.PATCH("/subscriptions/accounts/:id", updateAccountHandler(authSvc))
	group.DELETE("/subscriptions/accounts/:id", deleteAccountHandler(authSvc))
}

func listAccountsHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := middleware.APIKeyFrom(c)
		if key == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "router_key_required"})
			return
		}
		accounts, err := authSvc.ListSubscriptionAccounts(c.Request.Context(), auth.SubscriptionOwnerForKey(key))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_accounts_unavailable"})
			return
		}
		response := make([]accountResponse, 0, len(accounts))
		for _, account := range accounts {
			response = append(response, accountResponse{ID: account.ID, Provider: account.Provider, ExternalAccountID: account.ExternalAccountID, DisplayName: account.DisplayName, Enabled: account.Enabled, State: account.State, CooldownUntil: account.CooldownUntil, CreatedAt: account.CreatedAt})
		}
		c.JSON(http.StatusOK, response)
	}
}

func createAccountHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := middleware.APIKeyFrom(c)
		installation := middleware.InstallationFrom(c)
		if key == nil || installation == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "router_key_required"})
			return
		}
		var request createAccountRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_subscription_account"})
			return
		}
		account, err := authSvc.AddSubscriptionAccount(c.Request.Context(), auth.CreateSubscriptionAccountParams{Owner: auth.SubscriptionOwnerForKey(key), Provider: request.Provider, ExternalAccountID: request.ExternalAccountID, DisplayName: request.DisplayName, RefreshToken: []byte(request.RefreshToken), InstallationExternalID: installation.ExternalID})
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "subscription_account_rejected"})
			return
		}
		c.JSON(http.StatusCreated, accountResponse{ID: account.ID, Provider: account.Provider, ExternalAccountID: account.ExternalAccountID, DisplayName: account.DisplayName, Enabled: account.Enabled, State: account.State, CreatedAt: account.CreatedAt})
	}
}

func updateAccountHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := middleware.APIKeyFrom(c)
		if key == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "router_key_required"})
			return
		}
		var request updateAccountRequest
		if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "enabled_is_required"})
			return
		}
		if err := authSvc.UpdateSubscriptionAccountState(c.Request.Context(), auth.SubscriptionOwnerForKey(key), c.Param("id"), *request.Enabled, request.CooldownUntil); err != nil {
			if errors.Is(err, auth.ErrSubscriptionAccountNotFound) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "subscription_account_not_found"})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_account_update_failed"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func deleteAccountHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := middleware.APIKeyFrom(c)
		if key == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "router_key_required"})
			return
		}
		if err := authSvc.DeleteSubscriptionAccount(c.Request.Context(), auth.SubscriptionOwnerForKey(key), c.Param("id")); err != nil {
			if errors.Is(err, auth.ErrSubscriptionAccountNotFound) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "subscription_account_not_found"})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_account_delete_failed"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}
