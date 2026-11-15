package middleware

import (
	"weave-os/router/internal/observability"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

// WithSubscriberProductScope applies a subscriber's model boundary without
// reserving or consuming included allowance.
func WithSubscriberProductScope(svc *entitlement.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner := SubscriptionOwnerFrom(c)
		if owner.SubscriberID == "" {
			c.Next()
			return
		}

		plan, err := svc.ProductScope(c.Request.Context(), entitlement.SubscriberID(owner.SubscriberID))
		if err != nil {
			observability.FromGin(c).Error("Subscriber product scope check failed; refusing request", "err", err, "subscriber_id", owner.SubscriberID)
			abortBillingUnavailable(c)
			return
		}
		if plan != "" {
			c.Request = c.Request.WithContext(entitlement.WithProductScope(c.Request.Context(), plan))
		}
		c.Next()
	}
}
