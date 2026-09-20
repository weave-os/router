package middleware

import (
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

// WithSubscriberAllowance gates inference on an individual Max/Boost
// subscriber's included Router allowance. Attached after WithAuth so the
// authenticated credential subject below is populated.
//
// A request with no individual entitlement passes through untouched: Enterprise
// organization billing, BYOK, and prepaid keys keep the gates they already had,
// and this middleware is the only place the included allowance is enforced.
//
// An exhausted window answers 402 rather than silently routing onto paid
// capacity — the subscriber bought a bounded allowance, and quietly spending
// their org's balance instead is the surprise this gate exists to prevent. An
// allowance read error fails closed with 503, mirroring WithBalanceCheck: an
// allowance that admits everything while unreadable is an unbilled-usage hole.
func WithSubscriberAllowance(svc *entitlement.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		if _, ok := proxy.AgentShadowEvalFromContext(c.Request.Context()); ok {
			c.Next()
			return
		}

		apiKey := APIKeyFrom(c)
		if apiKey == nil || apiKey.CredentialSubjectID == "" {
			c.Next()
			return
		}

		subscriberID := entitlement.SubscriberID(apiKey.CredentialSubjectID)

		admission, err := svc.Admit(c.Request.Context(), subscriberID)
		if err != nil {
			log.Error("Subscriber allowance check failed; refusing request", "err", err, "subscriber_id", apiKey.CredentialSubjectID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "billing_unavailable",
				"message": "Billing system is temporarily unavailable. Retry in a few moments.",
			})
			return
		}

		switch admission.Outcome {
		case entitlement.AdmissionNotSubscribed:
			c.Next()
		case entitlement.AdmissionExhausted:
			// A request presenting a Claude/Codex credential covering this route
			// can serve at $0 on the caller's own plan without drawing included
			// Router capacity, so a spent allowance must not refuse it. Settlement
			// stays honest either way: it accounts only included_router capacity,
			// and this request carries no coverage to settle against.
			if proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath()) {
				c.Next()
				return
			}
			window := exhaustedWindow(admission)
			log.Info("Request rejected: subscriber allowance exhausted",
				"subscriber_id", apiKey.CredentialSubjectID,
				"period_kind", admission.ExhaustedPeriod,
				"consumed_usd_micros", window.ConsumedUsdMicros(),
				"allowance_usd_micros", window.LimitUsdMicros,
			)
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
				"error":                "subscription_allowance_exhausted",
				"period_kind":          string(admission.ExhaustedPeriod),
				"period_end":           window.Period.End,
				"consumed_usd_micros":  window.ConsumedUsdMicros(),
				"allowance_usd_micros": window.LimitUsdMicros,
				"message":              allowanceExhaustedMessage(admission.ExhaustedPeriod),
			})
		case entitlement.AdmissionCovered:
			c.Request = c.Request.WithContext(entitlement.WithCoverage(c.Request.Context(), admission.Coverage))
			c.Next()
		}
	}
}

// subscriberAllowanceCovers reports whether this request was admitted against
// an individual Max/Boost allowance. Such a turn debits 0 on the organization
// balance and settles against the subscriber's allowance instead, so the
// organization's prepaid and spend-cap gates do not apply to it.
func subscriberAllowanceCovers(c *gin.Context) bool {
	_, covered := entitlement.CoverageFromContext(c.Request.Context())
	return covered
}

// exhaustedWindow returns the usage of the window that rejected the request.
func exhaustedWindow(admission entitlement.Admission) entitlement.WindowUsage {
	if admission.ExhaustedPeriod == entitlement.PeriodKindSixHour {
		return admission.Usage.SixHour
	}
	return admission.Usage.Billing
}

func allowanceExhaustedMessage(kind entitlement.PeriodKind) string {
	if kind == entitlement.PeriodKindSixHour {
		return "This subscription's six-hour usage allowance is spent. Usage resets at the end of the current window."
	}
	return "This subscription's monthly usage allowance is spent. Usage resets when the billing period renews."
}
