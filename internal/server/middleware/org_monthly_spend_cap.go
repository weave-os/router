package middleware

import (
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"

	"github.com/gin-gonic/gin"
)

// WithOrgMonthlySpendCap enforces the org-wide monthly inference-spend cap.
// Must run after WithAuth (installation is guaranteed populated); fail-closed on read error.
func WithOrgMonthlySpendCap(svc *billing.Service) gin.HandlerFunc {
	return withOrgMonthlySpendCap(svc, nil)
}

// WithOrgMonthlySpendCapAndFailOpen uses a recent successful cap snapshot only
// during a prepared dependency outage.
func WithOrgMonthlySpendCapAndFailOpen(svc *billing.Service, cache *BillingFailOpenCache) gin.HandlerFunc {
	return withOrgMonthlySpendCap(svc, cache)
}

func withOrgMonthlySpendCap(svc *billing.Service, cache *BillingFailOpenCache) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		if _, ok := proxy.AgentShadowEvalFromContext(c.Request.Context()); ok {
			c.Next()
			return
		}

		installation := InstallationFrom(c)
		if installation == nil || installation.ExternalID == "" {
			// Should never happen: WithAuth runs first and would have 401'd.
			log.Debug("Org monthly spend cap check skipped: no installation on request context")
			c.Next()
			return
		}
		orgID := installation.ExternalID

		// Billing override is the org-wide escape hatch: WithBalanceCheck already
		// lets these orgs through (delta-0 debits), so skip the monthly cap too.
		if billing.HasOverrideFromContext(c.Request.Context()) {
			c.Next()
			return
		}

		// The cap bounds PAID spend, not free subscription usage: a request
		// presenting a Claude/Codex credential that covers this route serves at
		// $0 on the caller's own plan, so exempt it from the cap-reached 402 below
		// (mirrors WithBalanceCheck).
		subscriptionExempt := proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath())

		result, err := svc.CheckOrgMonthlySpend(c.Request.Context(), orgID)
		if err != nil && cache != nil && requestcontext.PreparationFrom(c.Request.Context()) != nil {
			if cached, ok := cache.getOrg(installation.ID); ok {
				result, err = cached, nil
			}
		}
		if err != nil {
			log.Error("Org monthly spend cap check failed; refusing request", "err", err, "organization_id", orgID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "billing_unavailable",
				"message": "Billing system is temporarily unavailable. Retry in a few moments.",
			})
			return
		}

		if cache != nil {
			cache.setOrg(installation.ID, result)
		}
		if result.LimitReached() {
			if subscriptionExempt {
				// Not 402'd: flag subscription-only so the proxy serves on the
				// caller's own subscription (or refuses a would-be-paid turn) and
				// never fails over to a paid model. Paid spend stays bounded at the cap.
				log.Info("Org monthly cap reached but subscription covers the route: serving subscription-only",
					"organization_id", orgID,
					"spent_usd_micros", result.SpentMicros,
					"monthly_limit_usd_micros", *result.LimitMicros,
				)
				c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context()))
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
