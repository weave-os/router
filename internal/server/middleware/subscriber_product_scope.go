package middleware

import (
	"weave-os/router/internal/observability"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

func WithSubscriberProductScope(svc *entitlement.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		subscriberID := allowanceSubscriber(c)
		if subscriberID == "" {
			c.Next()
			return
		}

		plan, err := svc.ProductScope(c.Request.Context(), subscriberID)
		if err != nil {
			observability.FromGin(c).Error("Subscriber product scope check failed; refusing request", "err", err, "subscriber_id", subscriberID)
			abortBillingUnavailable(c)
			return
		}
		if plan != "" {
			c.Request = c.Request.WithContext(entitlement.WithProductScope(c.Request.Context(), plan))
		}
		c.Next()
	}
}
