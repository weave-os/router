package middleware

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// TopUpURL is the customer-facing page where org admins buy credits.
// Returned in the 402 body so the client can surface a CTA.
const TopUpURL = "https://app.workweave.ai/organization/settings/weave-router"

// WithBalanceCheck enforces prepaid credit gating on inference routes.
// Attached only in managed mode and only after WithAuth, so the
// installation lookup below is guaranteed to be populated.
//
// Behavior (evaluated in order):
//   - Override row present → pass through; flag the request context so
//     the proxy's debit hook writes a delta=0 ledger row.
//   - Balance ≤ minBalanceMicros (or no balance row) → HTTP 402 unless
//     the request presents a validated Claude/Codex subscription credential
//     covering this route. Subscription-covered requests pass through flagged
//     subscription-only (billing.WithSubscriptionOnly), so the proxy serves them
//     on the caller's own subscription or refuses them, never on a paid model.
//     Override detection above still runs.
//   - Otherwise → pass through.
//
// The balance read is a single indexed SELECT (~2-5ms in-region). Any
// repo error fails closed with HTTP 503: in a prepaid credit system,
// allowing requests through when the gate cannot read the balance
// creates an unbilled-usage window where platform spend is incurred
// against an unknown balance. A short retry window for clients is the
// correct tradeoff vs. silently letting tenants spend without billing.
func WithBalanceCheck(svc *billing.Service, minBalanceMicros int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		if _, ok := proxy.AgentShadowEvalFromContext(c.Request.Context()); ok {
			c.Next()
			return
		}

		installation := InstallationFrom(c)
		if installation == nil || installation.ExternalID == "" {
			// Should never happen: WithAuth runs first and would have
			// 401'd. Log Debug rather than Error so a synthetic request
			// missing the auth setup doesn't page on-call.
			log.Debug("Balance check skipped: no installation on request context")
			c.Next()
			return
		}

		orgID := installation.ExternalID

		// Subscription turns debit $0 (cost.subscription_served), so gating them on
		// prepaid credits is wrong. The check depends only on whether the request
		// presents a covering credential — not on UsageBypassEnabled (routing config,
		// not billing config).
		subscriptionExempt := proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath())

		result, err := svc.CheckBalance(c.Request.Context(), orgID)
		if err != nil {
			if errors.Is(err, billing.ErrBalanceRowMissing) {
				// A subscription-only org may never have had a balance
				// row; its turns are free, so exempt them here too.
				if subscriptionExempt {
					log.Warn("Balance row missing: serving subscription-only, paid failover disabled",
						"organization_id", orgID)
					c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context()))
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
			// Infra error reading billing tables. Fail closed: a prepaid
			// gate that lets requests through on read errors creates an
			// unbilled-usage window. Return 503 so clients retry rather
			// than silently spending against an unknown balance.
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
			// A subscription-covered request at or below the prepaid threshold is not
			// 402'd: 402ing here would block traffic that serves for free on the
			// caller's own subscription. Flag it subscription-only so the proxy
			// serves on the subscription (or refuses a would-be-paid turn) and
			// never fails over to a paid model.
			if subscriptionExempt {
				log.Warn("Balance depleted: serving subscription-only, paid failover disabled",
					"organization_id", orgID,
					"balance_usd_micros", result.BalanceMicros,
					"threshold_usd_micros", threshold,
				)
				c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context()))
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

		c.Next()
	}
}
