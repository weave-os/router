package middleware

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

const TopUpURL = "https://app.weaveos.com/organization/settings/weave-router/credits"

func WithBalanceCheck(svc *billing.Service, minBalanceMicros int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		if owner, ok := billing.InternalTestOwnerFrom(c.Request.Context()); ok {
			balance, err := svc.PrepaidBalance(c.Request.Context(), owner)
			if err != nil {
				log.Error("Internal test budget unavailable", "subject_id", owner.TestSubjectID, "err", err)
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "test_budget_unavailable"})
				return
			}
			if balance <= minBalanceMicros {
				c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{"error": "test_budget_depleted"})
				return
			}
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
			log.Debug("Balance check skipped: no installation on request context")
			c.Next()
			return
		}

		orgID := installation.ExternalID
		if admittedWithoutCredits(c, svc, installation) {
			return
		}

		subscriptionExempt := proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath())

		result, err := svc.CheckBalance(c.Request.Context(), orgID)
		if err != nil {
			if errors.Is(err, billing.ErrBalanceRowMissing) {
				if subscriptionExempt {
					log.Warn("Balance row missing: serving subscription-only, paid failover disabled",
						"organization_id", orgID)
					c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context(), billing.SubscriptionOnlyCreditsDepleted))
					c.Next()
					return
				}
				log.Info("Balance check rejected: balance row missing", "organization_id", orgID)
				c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
					"error":              "insufficient_credits",
					"top_up_url":         TopUpURL,
					"balance_usd_micros": 0,
					"message":            "Your organization's prepaid credits are depleted. Contact your org admin to add credits.",
				})
				return
			}
			log.Error("Balance check failed; refusing request", "err", err, "organization_id", orgID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "billing_unavailable",
				"message": "Billing system is temporarily unavailable. Retry in a few moments.",
			})
			return
		}

		if result.HasOverride {
			ctx := context.WithValue(c.Request.Context(), billing.HasOverrideContextKey, true)
			c.Request = c.Request.WithContext(ctx)
			c.Next()
			return
		}

		threshold := minBalanceMicros

		if result.BalanceMicros <= threshold {
			if subscriptionExempt {
				log.Warn("Balance depleted: serving subscription-only, paid failover disabled",
					"organization_id", orgID,
					"balance_usd_micros", result.BalanceMicros,
					"threshold_usd_micros", threshold,
				)
				c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context(), billing.SubscriptionOnlyCreditsDepleted))
				c.Next()
				return
			}
			log.Info("Balance check rejected: balance at or below threshold",
				"organization_id", orgID,
				"balance_usd_micros", result.BalanceMicros,
				"threshold_usd_micros", threshold,
				"subscription_exempt", subscriptionExempt,
			)
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
				"error":              "insufficient_credits",
				"top_up_url":         TopUpURL,
				"balance_usd_micros": result.BalanceMicros,
				"message":            "Your organization's prepaid credits are depleted. Contact your org admin to add credits.",
			})
			return
		}

		c.Request = c.Request.WithContext(billing.WithOrganizationPrepaid(c.Request.Context()))
		c.Next()
	}
}

func admittedWithoutCredits(c *gin.Context, svc *billing.Service, installation *auth.Installation) bool {
	if installation.WeaveCreditInferenceEnabled {
		return false
	}
	ctx := billing.WithWeaveCreditsDenied(c.Request.Context())
	if result, err := svc.CheckBalance(ctx, installation.ExternalID); err == nil && result.HasOverride {
		ctx = context.WithValue(ctx, billing.HasOverrideContextKey, true)
	}
	c.Request = c.Request.WithContext(ctx)
	c.Next()
	return true
}
