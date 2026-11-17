package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
	hmmselection "weave-os/router/internal/router/hmm/selection"

	"github.com/gin-gonic/gin"
)

type DistributionRosterSource interface {
	DistributionRoster(context.Context) (*rosterdata.Roster, error)
}

const maxDistributionGrid = 101

type routingDistributionResponse struct {
	Points []router.DistributionPoint `json:"points"`
}

func RoutingDistributionHandler(source DistributionRosterSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		gridN := 0
		if raw := c.Query("grid"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 2 || n > maxDistributionGrid {
				c.JSON(http.StatusBadRequest, gin.H{"error": "grid must be an integer in [2, 101]"})
				return
			}
			gridN = n
		}
		excludedModels := parseCSVSet(c.Query("excluded_models"))
		excludedProviders := parseCSVSet(c.Query("excluded_providers"))
		strategy := router.Strategy(strings.ToLower(strings.TrimSpace(c.Query("strategy"))))
		if strategy != "" && strategy != router.StrategyHMM && strategy != router.StrategyHMMEmbedding {
			c.JSON(http.StatusBadRequest, gin.H{"error": "routing distribution requires an HMM strategy"})
			return
		}
		ctx := c.Request.Context()
		if strategy != "" {
			ctx = router.WithStrategy(ctx, strategy)
		}
		roster, err := source.DistributionRoster(ctx)
		if err != nil {
			observability.FromGin(c).Error("Routing distribution roster unavailable", "strategy", strategy, "grid", gridN, "err", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "routing distribution unavailable"})
			return
		}
		points, err := hmmselection.RoutingDistribution(roster, gridN, excludedModels, excludedProviders)
		if err != nil {
			if errors.Is(err, router.ErrNoEligibleProvider) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "exclusions leave no eligible models"})
				return
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "routing distribution unavailable"})
			return
		}
		c.JSON(http.StatusOK, routingDistributionResponse{Points: points})
	}
}

func parseCSVSet(raw string) map[string]struct{} {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out[v] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
