package middleware

import (
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/subscriptionlane"

	"github.com/gin-gonic/gin"
)

type DelegatedSpendScope string

const (
	DelegatedSpendScopeSession DelegatedSpendScope = "session"
	DelegatedSpendScopeTask    DelegatedSpendScope = "task"
)

func WithDelegatedSpendCap(svc *billing.Service, coverage credentials.SubscriptionCoverage) gin.HandlerFunc {
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
			abortBillingUnavailable(c)
			return
		}
		scope, spent, capMicros, reached := delegatedCapReached(session, sessionSpent, taskSpent)
		if !reached {
			c.Next()
			return
		}
		kind, refused := tripSubscriptionLane(c, subscriptionlane.DelegatedSpendCapGate,
			coverage(ctx, c.Request.Header, c.FullPath()))
		if !refused {
			log.Info("Delegated spend cap reached but subscription covers the route: serving subscription-only", "scope", scope)
			c.Next()
			return
		}
		log.Info("Request rejected: delegated spend cap reached", "scope", scope, "spent_usd_micros", spent, "spend_cap_usd_micros", capMicros)
		abortRefusal(c, kind, gin.H{
			"scope":                scope,
			"spent_usd_micros":     spent,
			"spend_cap_usd_micros": capMicros,
			"message":              "This session has reached its spend cap.",
		})
	}
}

func delegatedCapReached(session *auth.DelegatedSession, sessionSpent, taskSpent int64) (DelegatedSpendScope, int64, int64, bool) {
	if session.TaskCapMicros != nil && taskSpent >= *session.TaskCapMicros {
		return DelegatedSpendScopeTask, taskSpent, *session.TaskCapMicros, true
	}
	if session.SessionCapMicros != nil && sessionSpent >= *session.SessionCapMicros {
		return DelegatedSpendScopeSession, sessionSpent, *session.SessionCapMicros, true
	}
	return "", 0, 0, false
}
