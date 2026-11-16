package admin

import (
	"net/http"
	"sort"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"

	"github.com/gin-gonic/gin"
)

type policyCatalogEntry struct {
	Strategy     router.Strategy     `json:"strategy"`
	Available    bool                `json:"available"`
	Capabilities policy.Capabilities `json:"capabilities"`
}

func PolicyCatalogHandler(service *proxy.Service, defaultStrategy router.Strategy) gin.HandlerFunc {
	return func(c *gin.Context) {
		entries := []policyCatalogEntry{}
		if service != nil {
			for _, strategy := range service.RegisteredStrategies() {
				capabilities, _ := service.PolicyCapabilitiesForRequest(c.Request.Context(), strategy)
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
