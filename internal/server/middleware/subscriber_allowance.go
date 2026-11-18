package middleware

import (
	"weave-os/router/internal/billing"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

type SubscriptionErrorCode string

const SubscriptionPlanConflict SubscriptionErrorCode = "subscription_plan_conflict"

func WithSubscriberAllowance(svc *entitlement.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		subscriberID := allowanceSubscriber(c)
		if subscriberID == "" {
			c.Next()
			return
		}

		admission, err := svc.Admit(c.Request.Context(), subscriberID)
		if err != nil {
			log.Error("Subscriber allowance check failed; refusing request", "err", err, "subscriber_id", subscriberID)
			abortBillingUnavailable(c)
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
			ctx, _ := billing.StepSubscriptionLane(c.Request.Context(), subscriptionlane.LinkedPlanPreferred)
			c.Request = c.Request.WithContext(ctx)
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

func allowanceSubscriber(c *gin.Context) entitlement.SubscriberID {
	if owner := SubscriptionOwnerFrom(c); owner.SubscriberID != "" {
		return entitlement.SubscriberID(owner.SubscriberID)
	}
	installation := InstallationFrom(c)
	if installation == nil || !flags.BoolOr(c.Request.Context(), flags.KeyInstallationPoolAllowance, false) {
		return ""
	}
	return entitlement.SubscriberID(installation.PoolSubjectID)
}
