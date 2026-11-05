package admin

import (
	"errors"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type subscriptionOwnerResponse struct {
	SubscriberID string `json:"subscriber_id"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	KeyPrefix    string `json:"key_prefix"`
	KeySuffix    string `json:"key_suffix"`
}

// ListSubscriptionOwnersHandler serves GET /admin/v1/subscription-owners: the
// people a dashboard admin can link a Claude or Codex subscription for. With
// pools off it reports enabled=false so the dashboard can say how to turn them on.
func ListSubscriptionOwnersHandler(authSvc *auth.Service, loginsEnabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !loginsEnabled {
			c.JSON(http.StatusOK, gin.H{"enabled": false, "owners": []subscriptionOwnerResponse{}})
			return
		}
		owners, err := authSvc.ListSubscriptionEnrollmentOwners(c.Request.Context())
		if err != nil {
			observability.FromGin(c).Error("Failed to list subscription owners", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_owners_unavailable"})
			return
		}
		response := make([]subscriptionOwnerResponse, 0, len(owners))
		for _, owner := range owners {
			response = append(response, subscriptionOwnerResponse{SubscriberID: owner.SubscriberID, DisplayName: owner.DisplayName, Email: owner.Email, KeyPrefix: owner.KeyPrefix, KeySuffix: owner.KeySuffix})
		}
		c.JSON(http.StatusOK, gin.H{"enabled": true, "owners": response})
	}
}

type subscriptionSeatResponse struct {
	AccountID          string                    `json:"account_id"`
	SubscriberID       string                    `json:"subscriber_id"`
	Provider           auth.SubscriptionProvider `json:"provider"`
	DisplayName        string                    `json:"display_name"`
	Enabled            bool                      `json:"enabled"`
	OwnerDisplayName   string                    `json:"owner_display_name"`
	OwnerEmail         string                    `json:"owner_email"`
	OwnerHasRoutingKey bool                      `json:"owner_has_routing_key"`
}

// ListSubscriptionSeatsHandler serves GET /admin/v1/subscription-accounts so an
// admin can unlink any seat, including one whose owner lost every routing key.
func ListSubscriptionSeatsHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		seats, err := authSvc.ListSubscriptionSeats(c.Request.Context())
		if err != nil {
			observability.FromGin(c).Error("Failed to list subscription seats", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_accounts_unavailable"})
			return
		}
		response := make([]subscriptionSeatResponse, 0, len(seats))
		for _, seat := range seats {
			response = append(response, subscriptionSeatResponse(seat))
		}
		c.JSON(http.StatusOK, gin.H{"seats": response})
	}
}

// AdminStartSubscriptionLoginHandler starts the same browser login the control
// plane uses, for a subscriber the dashboard admin picked.
func AdminStartSubscriptionLoginHandler(authSvc *auth.Service, flow SubscriptionLoginFlow) gin.HandlerFunc {
	return startSubscriptionLoginHandler(authSvc, flow, adminSubscriptionOwner(authSvc))
}

func AdminCompleteSubscriptionLoginHandler(authSvc *auth.Service, flow SubscriptionLoginFlow) gin.HandlerFunc {
	return completeSubscriptionLoginHandler(authSvc, flow, adminSubscriptionOwner(authSvc))
}

// adminSubscriptionOwner only accepts subscribers with a live routing key, so
// an admin cannot start a login that could never be saved.
func adminSubscriptionOwner(authSvc *auth.Service) subscriptionOwnerResolver {
	return func(c *gin.Context) (auth.SubscriptionOwner, bool) {
		subscriberID := c.Param("subscriberID")
		if _, err := uuid.Parse(subscriberID); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_subscriber"})
			return auth.SubscriptionOwner{}, false
		}
		owner, err := authSvc.SubscriberEnrollmentOwner(c.Request.Context(), subscriberID)
		if errors.Is(err, auth.ErrSubscriptionEnrollmentKeyNotFound) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "routing_key_required"})
			return auth.SubscriptionOwner{}, false
		}
		if err != nil {
			observability.FromGin(c).Error("Failed to resolve subscription owner", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_login_unavailable"})
			return auth.SubscriptionOwner{}, false
		}
		return auth.SubscriptionOwner{SubscriberID: owner.SubscriberID}, true
	}
}
