package middleware

import (
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptionlane"

	"github.com/gin-gonic/gin"
)

func WithAPIKeySpendCap(svc *billing.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		if _, ok := requestcontext.AgentShadowEvalFromContext(c.Request.Context()); ok {
			c.Next()
			return
		}

		if subscriberAllowanceCovers(c) {
			c.Next()
			return
		}

		apiKey := APIKeyFrom(c)
		if apiKey == nil || apiKey.ID == "" {
			c.Next()
			return
		}

		subscriptionExempt := proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath())

		result, err := svc.CheckAPIKeySpendCap(c.Request.Context(), apiKey.ID)
		if err != nil {
			log.Error("API key spend-cap check failed; refusing request", "err", err, "api_key_id", apiKey.ID)
			abortBillingUnavailable(c)
			return
		}

		if !result.Found || result.CapMicros == nil {
			c.Next()
			return
		}

		if result.SpentMicros >= *result.CapMicros {
			kind, refused := tripSubscriptionLane(c, subscriptionlane.KeySpendCapGate, subscriptionExempt)
			if !refused {
				log.Info("API key spend cap reached but subscription covers the route: serving subscription-only",
					"api_key_id", apiKey.ID,
					"spent_usd_micros", result.SpentMicros,
					"spend_cap_usd_micros", *result.CapMicros,
				)
				c.Next()
				return
			}
			log.Info("Request rejected: api key spend cap reached",
				"api_key_id", apiKey.ID,
				"spent_usd_micros", result.SpentMicros,
				"spend_cap_usd_micros", *result.CapMicros,
			)
			abortRefusal(c, kind, gin.H{
				"spent_usd_micros":     result.SpentMicros,
				"spend_cap_usd_micros": *result.CapMicros,
				"message":              "This router key has reached its spend cap. Mint a new key or raise the cap to continue.",
			})
			return
		}
		c.Next()
	}
}
