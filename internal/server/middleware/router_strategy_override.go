package middleware

import (
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
)

// RouterStrategyOverrideHeader selects the routing strategy for a request.
// Accepted values are "cluster" (the default scorer), "rl" (the trained
// RL/DPO policy router), and "bandit" (Thompson sampling over a frozen
// posterior). Absent or unrecognized values fall through to the deployment
// default.
const RouterStrategyOverrideHeader = "x-weave-router-strategy"

// WithRouterStrategyOverride applies the persisted installation strategy and permits
// an eval header override when authorized; available is injected by the proxy registry.
func WithRouterStrategyOverride(available ...router.Strategy) gin.HandlerFunc {
	return WithRouterStrategyDefault(router.StrategyCluster, available...)
}

// WithRouterStrategyDefault applies a deployment-level default for installations
// with no explicit override, enabling allowlist-first then one-flag global rollout.
// hmm_beta may be pinned per installation (or requested by an authorized header)
// but never becomes the deployment default, so a broken beta lane cannot take
// every installation with it.
func WithRouterStrategyDefault(defaultStrategy router.Strategy, available ...router.Strategy) gin.HandlerFunc {
	if len(available) == 0 {
		available = []router.Strategy{
			router.StrategyRL,
			router.StrategyHMM,
			router.StrategyHMMEmbedding,
			router.StrategyBandit,
		}
	}
	allowed := make(map[router.Strategy]struct{}, len(available)+1)
	allowed[router.StrategyCluster] = struct{}{}
	for _, strategy := range available {
		allowed[strategy] = struct{}{}
	}
	defaultStrategy = NormalizeRouterStrategyDefault(defaultStrategy, available...)
	return func(c *gin.Context) {
		installation := InstallationFrom(c)
		if installation == nil {
			c.Next()
			return
		}

		strategy := router.Strategy(strings.ToLower(strings.TrimSpace(string(installation.RoutingStrategy))))
		if strategy == "" {
			strategy = defaultStrategy
		}
		if _, ok := allowed[strategy]; !ok {
			observability.FromGin(c).Warn(
				"Persisted router strategy is not registered; using cluster",
				"installation_id", installation.ID,
				"persisted_strategy", strategy,
			)
			strategy = router.StrategyCluster
		}

		raw := strings.ToLower(strings.TrimSpace(c.GetHeader(RouterStrategyOverrideHeader)))
		if raw != "" {
			requested := router.Strategy(raw)
			switch {
			case !installation.PolicyHeaderOverridesEnabled:
				observability.FromGin(c).Warn("Router-strategy override ignored: installation is not authorized for policy headers", "installation_id", installation.ID)
			case !strategyAllowed(requested, allowed):
				observability.FromGin(c).Warn("Router-strategy override ignored: strategy is not registered", "installation_id", installation.ID, "requested_strategy", raw)
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

// NormalizeRouterStrategyDefault clamps an unregistered deployment default to
// cluster. hmm_beta is never a valid default: it is an opt-in lane pinned per
// installation or per session, not a fleet-wide policy.
func NormalizeRouterStrategyDefault(defaultStrategy router.Strategy, available ...router.Strategy) router.Strategy {
	allowed := make(map[router.Strategy]struct{}, len(available)+1)
	allowed[router.StrategyCluster] = struct{}{}
	for _, strategy := range available {
		if strategy == router.StrategyHMMBeta {
			continue
		}
		allowed[strategy] = struct{}{}
	}
	if !strategyAllowed(defaultStrategy, allowed) {
		return router.StrategyCluster
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
