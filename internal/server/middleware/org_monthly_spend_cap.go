package middleware

import (
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

func WithOrgMonthlySpendCap(svc *billing.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		if _, ok := billing.InternalTestOwnerFrom(c.Request.Context()); ok {
			c.Next()
			return
		}
		if _, ok := proxy.AgentShadowEvalFromContext(c.Request.Context()); ok {
			c.Next()
			return
		}

		if subscriberAllowanceCovers(c) {
			c.Next()
			return
		}

		installation := InstallationFrom(c)
		if installation == nil || installation.ExternalID == "" {
			log.Debug("Org monthly spend cap check skipped: no installation on request context")
			c.Next()
			return
		}
		orgID := installation.ExternalID

		if billing.HasOverrideFromContext(c.Request.Context()) {
			c.Next()
			return
		}

		subscriptionExempt := proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath())

		result, err := svc.CheckOrgMonthlySpend(c.Request.Context(), orgID)
		if err != nil {
			log.Error("Org monthly spend cap check failed; refusing request", "err", err, "organization_id", orgID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "billing_unavailable",
				"message": "Billing system is temporarily unavailable. Retry in a few moments.",
			})
			return
		}

		if result.LimitReached() {
			if subscriptionExempt {
				log.Info("Org monthly cap reached but subscription covers the route: serving subscription-only",
					"organization_id", orgID,
					"spent_usd_micros", result.SpentMicros,
					"monthly_limit_usd_micros", *result.LimitMicros,
				)
				c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context(), billing.SubscriptionOnlySpendCapReached))
				c.Next()
				return
			}
			log.Info("Request rejected: org monthly spend cap reached",
				"organization_id", orgID,
				"spent_usd_micros", result.SpentMicros,
				"monthly_limit_usd_micros", *result.LimitMicros,
			)
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
				"error":                    "org_monthly_spend_limit_reached",
				"spent_usd_micros":         result.SpentMicros,
				"monthly_limit_usd_micros": *result.LimitMicros,
				"message":                  "Your organization has reached its monthly Weave Router spend limit. An org admin can raise the limit, or it resets next month.",
			})
			return
		}
		c.Next()
	}
}
