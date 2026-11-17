package middleware

import (
	"net/http"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

const RouterStrategyOverrideHeader = "x-weave-router-strategy"

type StrategyAvailability func(router.Strategy) bool

func WithRouterStrategyDefault(defaultStrategy router.Strategy, liveAvailability StrategyAvailability, available ...router.Strategy) gin.HandlerFunc {
	if len(available) == 0 {
		available = []router.Strategy{
			router.StrategyRL,
			router.StrategyHMM,
			router.StrategyHMMEmbedding,
		}
	}
	allowed := make(map[router.Strategy]struct{}, len(available))
	for _, strategy := range available {
		allowed[strategy] = struct{}{}
	}
	managedDefaultStrategy := defaultStrategy
	defaultStrategy = NormalizeRouterStrategyDefault(defaultStrategy, available...)
	selectable := func(strategy router.Strategy) bool {
		if strategy == router.StrategyLLMClassifier {
			return false
		}
		if !strategyAllowed(strategy, allowed) {
			return false
		}
		return !router.IsBetaStrategy(strategy) || (liveAvailability != nil && liveAvailability(strategy))
	}
	return func(c *gin.Context) {
		installation := InstallationFrom(c)
		if installation == nil {
			c.Next()
			return
		}

		strategy := router.Strategy(strings.ToLower(strings.TrimSpace(string(installation.RoutingStrategy))))
		if router.IsRetiredStrategy(strategy) {
			strategy = ""
		}
		identity, managedServing := requestcontext.ServingIdentityFromContext(c.Request.Context())
		if managedServing && identity.Plan != "" {
			profile, ok := entitlement.ServingProfileFor(entitlement.Plan(identity.Plan))
			if !ok {
				observability.FromGin(c).Error("Managed serving identity has an unknown subscriber plan", "plan", identity.Plan)
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "plan_routing_unavailable"})
				return
			}
			if profile.Key != "" && !selectable(profile.Strategy) {
				observability.FromGin(c).Error("Plan-owned routing profile strategy is unavailable", "plan", identity.Plan)
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "plan_routing_unavailable"})
				return
			}
			if profile.Key != "" {
				c.Request = c.Request.WithContext(router.WithStrategy(c.Request.Context(), profile.Strategy))
				c.Next()
				return
			}
		}
		switch {
		case managedServing && (router.IsBetaStrategy(strategy) || strategy == ""):
			strategy = managedDefaultStrategy
		case strategy == "":
			strategy = defaultStrategy
		}
		if !selectable(strategy) {
			observability.FromGin(c).Error(
				"Persisted or default router strategy is not registered or unavailable",
				"installation_id", installation.ID,
				"persisted_strategy", installation.RoutingStrategy,
				"effective_strategy", strategy,
				"managed_serving", managedServing,
			)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "routing_strategy_unavailable"})
			return
		}

		raw := strings.ToLower(strings.TrimSpace(c.GetHeader(RouterStrategyOverrideHeader)))
		if raw != "" {
			requested := router.Strategy(raw)
			switch {
			case managedServing && router.IsBetaStrategy(requested):
				observability.FromGin(c).Warn("Retired beta strategy override ignored", "effective_strategy", strategy)
			case !installation.PolicyHeaderOverridesEnabled:
				observability.FromGin(c).Warn("Router-strategy override ignored: installation is not authorized for policy headers", "installation_id", installation.ID)
			case !selectable(requested):
				observability.FromGin(c).Warn("Router-strategy override ignored: strategy is not registered or unavailable", "installation_id", installation.ID, "requested_strategy", raw)
			default:
				strategy = requested
				observability.FromGin(c).Info("Router-strategy override applied", "installation_id", installation.ID, "requested_strategy", raw)
			}
		}

		ctx := router.WithStrategy(c.Request.Context(), strategy)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func NormalizeRouterStrategyDefault(defaultStrategy router.Strategy, available ...router.Strategy) router.Strategy {
	allowed := make(map[router.Strategy]struct{}, len(available))
	for _, strategy := range available {
		if router.IsBetaStrategy(strategy) || strategy == router.StrategyLLMClassifier {
			continue
		}
		allowed[strategy] = struct{}{}
	}
	if !strategyAllowed(defaultStrategy, allowed) {
		return ""
	}
	return defaultStrategy
}

func strategyAllowed(strategy router.Strategy, allowed map[router.Strategy]struct{}) bool {
	if strategy == "" {
		return false
	}
	_, ok := allowed[strategy]
	return ok
}
