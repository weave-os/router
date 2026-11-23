package middleware

import (
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

const RouterStrategyOverrideHeader = "x-weave-router-strategy"

type StrategyAvailability func(router.Strategy) bool

func WithRouterStrategyDefault(defaultStrategy router.Strategy, liveAvailability StrategyAvailability, available ...router.Strategy) gin.HandlerFunc {
	policy := router.NewStrategyPolicy(defaultStrategy, liveAvailability, available...)
	return func(c *gin.Context) {
		installation := InstallationFrom(c)
		if installation == nil {
			c.Next()
			return
		}

		in := router.StrategyInputs{
			Persisted:        installation.RoutingStrategy,
			Header:           c.GetHeader(RouterStrategyOverrideHeader),
			HeaderAuthorized: installation.PolicyHeaderOverridesEnabled,
		}
		identity, managedServing := requestcontext.ServingIdentityFromContext(c.Request.Context())
		in.Managed = managedServing
		if managedServing && identity.Plan != "" {
			profile, ok := entitlement.ServingProfileFor(entitlement.Plan(identity.Plan))
			if !ok {
				observability.FromGin(c).Error("Managed serving identity has an unknown subscriber plan", "plan", identity.Plan)
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "plan_routing_unavailable"})
				return
			}
			in.PlanProfile, in.PlanStrategy = profile.Key != "", profile.Strategy
		}
		decision := policy.Resolve(in)
		switch decision.Outcome {
		case router.StrategyResolved:
		case router.StrategyPlanUnavailable:
			observability.FromGin(c).Error("Plan-owned routing profile strategy is unavailable", "plan", identity.Plan)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "plan_routing_unavailable"})
			return
		case router.StrategyUnresolvable:
			observability.FromGin(c).Error(
				"Persisted or default router strategy is not registered or unavailable",
				"installation_id", installation.ID,
				"persisted_strategy", installation.RoutingStrategy,
				"effective_strategy", decision.Strategy,
				"managed_serving", managedServing,
			)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "routing_strategy_unavailable"})
			return
		}
		logStrategyHeader(c, installation.ID, decision)

		ctx := router.WithStrategy(c.Request.Context(), decision.Strategy)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func logStrategyHeader(c *gin.Context, installationID string, decision router.StrategyDecision) {
	raw := decision.RequestedHeader
	switch decision.HeaderOutcome {
	case router.StrategyHeaderAbsent:
	case router.StrategyHeaderRetiredBeta:
		observability.FromGin(c).Warn("Retired beta strategy override ignored", "effective_strategy", decision.Strategy)
	case router.StrategyHeaderUnauthorized:
		observability.FromGin(c).Warn("Router-strategy override ignored: installation is not authorized for policy headers", "installation_id", installationID)
	case router.StrategyHeaderUnselectable:
		observability.FromGin(c).Warn("Router-strategy override ignored: strategy is not registered or unavailable", "installation_id", installationID, "requested_strategy", raw)
	case router.StrategyHeaderApplied:
		observability.FromGin(c).Info("Router-strategy override applied", "installation_id", installationID, "requested_strategy", raw)
	}
}

func NormalizeRouterStrategyDefault(defaultStrategy router.Strategy, available ...router.Strategy) router.Strategy {
	return router.NormalizeDefaultStrategy(defaultStrategy, available...)
}
