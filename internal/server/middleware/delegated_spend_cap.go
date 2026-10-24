package middleware

import (
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

type DelegatedSpendScope string

const (
	DelegatedSpendScopeSession DelegatedSpendScope = "session"
	DelegatedSpendScopeTask    DelegatedSpendScope = "task"
)

// WithDelegatedSpendCap enforces the per-session and per-task USD caps a
// delegated session token carries. Spend is read fresh each request, like
// WithAPIKeySpendCap, with the same exemptions and the same fail-closed 503.
// Requests from any other credential, or tokens without caps, pass through
// without a read.
func WithDelegatedSpendCap(svc *billing.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		session := auth.DelegatedSessionFrom(ctx)
		if session == nil || (session.SessionCapMicros == nil && session.TaskCapMicros == nil) {
			c.Next()
			return
		}
		if _, ok := proxy.AgentShadowEvalFromContext(ctx); ok || subscriberAllowanceCovers(c) {
			c.Next()
			return
		}
		log := observability.FromGin(c).With("api_key_id", session.IssuerKeyID, "delegated_session_id", session.SessionID, "delegated_task_id", session.TaskID)
		sessionSpent, taskSpent, err := svc.CheckDelegatedSpend(ctx, session.IssuerKeyID, session.SessionID, session.TaskID)
		if err != nil {
			log.Error("Delegated spend-cap check failed; refusing request", "err", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "billing_unavailable",
				"message": "Billing system is temporarily unavailable. Retry in a few moments.",
			})
			return
		}
		scope, spent, capMicros, reached := delegatedCapReached(session, sessionSpent, taskSpent)
		if !reached {
			c.Next()
			return
		}
		if proxy.RequestPresentsCoveringSubscription(ctx, c.Request.Header, c.FullPath()) {
			log.Info("Delegated spend cap reached but subscription covers the route: serving subscription-only", "scope", scope)
			c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlySpendCapReached))
			c.Next()
			return
		}
		log.Info("Request rejected: delegated spend cap reached", "scope", scope, "spent_usd_micros", spent, "spend_cap_usd_micros", capMicros)
		c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
			"error":                "key_spend_cap_reached",
			"scope":                scope,
			"spent_usd_micros":     spent,
			"spend_cap_usd_micros": capMicros,
			"message":              "This session has reached its spend cap.",
		})
	}
}

// delegatedCapReached reports the first cap the session has reached, task before session.
func delegatedCapReached(session *auth.DelegatedSession, sessionSpent, taskSpent int64) (DelegatedSpendScope, int64, int64, bool) {
	if session.TaskCapMicros != nil && taskSpent >= *session.TaskCapMicros {
		return DelegatedSpendScopeTask, taskSpent, *session.TaskCapMicros, true
	}
	if session.SessionCapMicros != nil && sessionSpent >= *session.SessionCapMicros {
		return DelegatedSpendScopeSession, sessionSpent, *session.SessionCapMicros, true
	}
	return "", 0, 0, false
}
