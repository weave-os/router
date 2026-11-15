package middleware

import (
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

type SubscriptionErrorCode string

const SubscriptionPlanConflict SubscriptionErrorCode = "subscription_plan_conflict"

func WithSubscriberAllowance(svc *entitlement.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		owner := SubscriptionOwnerFrom(c)
		if owner.SubscriberID == "" {
			c.Next()
			return
		}

		subscriberID := entitlement.SubscriberID(owner.SubscriberID)

		admission, err := svc.Admit(c.Request.Context(), subscriberID)
		if err != nil {
			log.Error("Subscriber allowance check failed; refusing request", "err", err, "subscriber_id", subscriberID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "billing_unavailable",
				"message": "Billing system is temporarily unavailable. Retry in a few moments.",
			})
			return
		}

		if admission.Plan != "" {
			c.Request = c.Request.WithContext(entitlement.WithProductScope(c.Request.Context(), admission.Plan))
		}
		if admission.Outcome == entitlement.AdmissionNotSubscribed {
			c.Next()
			return
		}
		if admission.Outcome == entitlement.AdmissionExhausted {
			c.Request = c.Request.WithContext(entitlement.WithSpentWindow(c.Request.Context(), entitlement.SpentWindow{
				Plan:     admission.Plan,
				Kind:     admission.ExhaustedPeriod,
				ResetsAt: admission.ResetsAt,
			}))
		}

		if _, shadow := proxy.AgentShadowEvalFromContext(c.Request.Context()); shadow {
			c.Next()
			return
		}

		if admission.Outcome == entitlement.AdmissionCovered {
			c.Request = c.Request.WithContext(entitlement.WithCoverage(c.Request.Context(), admission.Coverage))
		}

		if proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath()) {
			log.Info("Subscriber request prefers linked subscription funding", "reason", billing.SubscriptionOnlyLinkedFirst, "subscriber_id", subscriberID, "admission_outcome", admission.Outcome)
			c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context(), billing.SubscriptionOnlyLinkedFirst))
			c.Next()
			return
		}

		c.Next()
	}
}

func subscriberAllowanceCovers(c *gin.Context) bool {
	if _, covered := entitlement.CoverageFromContext(c.Request.Context()); covered {
		return true
	}
	return false
}
