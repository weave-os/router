package admin

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/subscriptions"
)

type subscriptionRefreshErrorCode string

const (
	refreshInvalidAccount     subscriptionRefreshErrorCode = "invalid_subscription_account"
	refreshAccountNotFound    subscriptionRefreshErrorCode = "subscription_account_not_found"
	refreshAccountUnavailable subscriptionRefreshErrorCode = "subscription_account_unavailable"
	refreshFailed             subscriptionRefreshErrorCode = "subscription_refresh_failed"
)

func InternalRefreshSubscriptionAccountHandler(refresher subscriptions.AccountRefresher) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := internalSubscriptionOwner(c)
		if !ok {
			return
		}
		accountID := c.Param("accountID")
		if _, err := uuid.Parse(accountID); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": refreshInvalidAccount})
			return
		}
		refreshResult, err := refresher.RefreshAccount(c.Request.Context(), owner, accountID)
		if errors.Is(err, auth.ErrSubscriptionAccountNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": refreshAccountNotFound})
			return
		}
		if errors.Is(err, subscriptions.ErrNoAvailableAccount) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": refreshAccountUnavailable})
			return
		}
		if err != nil {
			observability.FromGin(c).Warn("Requested subscription refresh failed", "account_id", accountID, "err", err)
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": refreshFailed})
			return
		}
		c.JSON(http.StatusOK, refreshResult)
	}
}
