package middleware

import (
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

// SubscriptionErrorCode identifies a subscription admission refusal.
type SubscriptionErrorCode string

// SubscriptionPlanConflict is retained for clients that classify legacy plan conflicts.
const SubscriptionPlanConflict SubscriptionErrorCode = "subscription_plan_conflict"

// WithSubscriberAllowance gates inference on an individual Max/Boost
// subscriber's included Router allowance. Attached after WithAuth so the
// authenticated credential subject below is populated.
//
// A request with no individual entitlement passes through untouched: Enterprise
// organization billing, BYOK, and prepaid keys keep the gates they already had,
// and this middleware is the only place the included allowance is enforced.
//
// Max never treats a consumer subscription as a funding source: covering
// detection is suppressed after PlanMax is stamped, so included allowance
// (then prepaid) pays. A subject whose entitlement has ended is not subscribed:
// it keeps its plan's model boundary but otherwise passes through untouched, so
// its linked subscription is neither refused nor preferred. Entitled Boost
// callers prefer compatible linked-provider capacity. When linked and included
// capacity are unavailable, requests continue through the existing organization
// balance and spend-limit gates. Allowance read errors fail closed because
// treating an unreadable meter as exhausted would incorrectly authorize
// organization spending.
//
// Nothing is reserved before dispatch, so a subscriber's concurrent agents
// never wait on or refuse each other. They can together overrun a window by
// what they have in flight; settlement books each turn's actual cost, and the
// next turn after the window is spent moves to organization billing.
func WithSubscriberAllowance(svc *entitlement.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		// The allowance follows the person the request identified itself as,
		// not the key it authenticated with: a key shared across an
		// organization must spend each caller's own included capacity.
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

		// The plan's hard model boundary is stamped before the allowance
		// verdict is acted on, so it governs the turn no matter which book
		// ends up paying for it — included allowance, organization credits, or the caller's
		// own covering subscription.
		if admission.Plan != "" {
			c.Request = c.Request.WithContext(entitlement.WithProductScope(c.Request.Context(), admission.Plan))
		}
		if admission.Outcome == entitlement.AdmissionNotSubscribed {
			c.Next()
			return
		}

		// An agent-shadow evaluation draws no included allowance — it is Weave's
		// own traffic, not the subscriber's turn — but it dispatches a forced
		// model, so it runs after the boundary above is stamped.
		if _, shadow := proxy.AgentShadowEvalFromContext(c.Request.Context()); shadow {
			c.Next()
			return
		}

		// Retain included capacity for models the linked subscription cannot serve.
		// Settlement skips this allowance when the linked subscription pays.
		if admission.Outcome == entitlement.AdmissionCovered {
			c.Request = c.Request.WithContext(entitlement.WithCoverage(c.Request.Context(), admission.Coverage))
		}

		// Max suppresses linked-subscription funding; Boost prefers it while
		// retaining included capacity for any model it cannot cover.
		if proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath()) {
			log.Info("Subscriber request prefers linked subscription funding", "reason", billing.SubscriptionOnlyLinkedFirst, "subscriber_id", subscriberID, "admission_outcome", admission.Outcome)
			c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context(), billing.SubscriptionOnlyLinkedFirst))
			c.Next()
			return
		}

		c.Next()
	}
}

// subscriberAllowanceCovers reports whether subscriber-owned capacity pays for
// this turn, so organization billing gates must not inspect it.
func subscriberAllowanceCovers(c *gin.Context) bool {
	if _, covered := entitlement.CoverageFromContext(c.Request.Context()); covered {
		return true
	}
	return false
}
