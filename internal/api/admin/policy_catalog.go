package admin

import (
	"net/http"
	"sort"

	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
	"workweave/router/internal/router/policy"

	"github.com/gin-gonic/gin"
)

type policyCatalogEntry struct {
	Strategy     router.Strategy     `json:"strategy"`
	Available    bool                `json:"available"`
	Capabilities policy.Capabilities `json:"capabilities"`
}

// PolicyCatalogHandler exposes the strategy registry and optional capability
// surface to the control plane. It contains no tenant or request data.
func PolicyCatalogHandler(service *proxy.Service, defaultStrategy router.Strategy) gin.HandlerFunc {
	return func(c *gin.Context) {
		entries := []policyCatalogEntry{{
			Strategy:  router.StrategyCluster,
			Available: service != nil && service.PolicyStrategyAvailable(router.StrategyCluster),
			Capabilities: policy.Capabilities{
				SchemaVersion:          policy.SchemaVersionV1,
				HonorsPreferredModels:  true,
				HonorsQualityPriceBias: true,
				SupportsPreview:        true,
				SupportsShadow:         true,
				// The cluster scorer has no ranked fallback to re-select
				// against, so per-cluster model selections are inert on it.
				HonorsClusterModelLists: false,
			},
		}}
		if service != nil {
			for _, strategy := range service.RegisteredStrategies() {
				// Beta is a session control, not an installation strategy. Keep it
				// out of the control-plane catalog so /beta remains its only surface.
				if strategy == router.StrategyHMMBeta {
					continue
				}
				capabilities, _ := service.PolicyCapabilities(strategy)
				// Derived, not separately negotiated: ranked fallback is the
				// precondition for cluster overrides taking effect.
				capabilities.HonorsClusterModelLists = capabilities.ReportsRankedFallback
				entries = append(entries, policyCatalogEntry{
					Strategy:     strategy,
					Available:    service.PolicyStrategyAvailable(strategy),
					Capabilities: capabilities,
				})
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Strategy < entries[j].Strategy })
		c.JSON(http.StatusOK, gin.H{
			"schema_version":   policy.SchemaVersionV1,
			"default_strategy": defaultStrategy,
			"strategies":       entries,
		})
	}
}
