package admin

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"

	"github.com/gin-gonic/gin"
)

// CatalogModelsResponse is the shape returned by GET /v1/router/models; the
// gateway answers ?scope=catalog from the same type.
type CatalogModelsResponse = catalog.ModelListingResponse

// HMMRosterSource exposes the HMM sidecar roster arms as catalog entries;
// its roster differs from the cluster artifact's DeployedModelsSource.
type HMMRosterSource interface {
	HMMDeployedModels(ctx context.Context) ([]cluster.DeployedEntry, error)
}

// CatalogModelsHandler returns the deployed-models catalog for the caller's
// routing strategy. Read-only, unauthed metadata — mounted in both selfhosted
// and managed modes. For ?strategy=hmm* returns the HMM sidecar roster;
// nil hmmModels falls back to the cluster list.
//
// The list is publicly known (we publish per-version model registries on the
// RouterArena leaderboard) so there is no leak risk from leaving this open.
func CatalogModelsHandler(models DeployedModelsSource, hmmModels HMMRosterSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.EqualFold(strings.TrimSpace(c.Query("scope")), catalog.ScopeCatalog) {
			c.JSON(http.StatusOK, CatalogModelsResponse{Models: catalog.Listing()})
			return
		}
		strategy := router.Strategy(strings.ToLower(strings.TrimSpace(c.Query("strategy"))))
		if router.IsHMMStrategy(strategy) && hmmModels != nil {
			entries, err := hmmModels.HMMDeployedModels(c.Request.Context())
			if err != nil {
				observability.FromGin(c).Error(
					"Failed to fetch HMM roster for deployed-models endpoint",
					"err", err,
					"strategy", string(strategy),
				)
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "hmm roster unavailable"})
				return
			}
			c.JSON(http.StatusOK, CatalogModelsResponse{Models: entriesToDTO(entries)})
			return
		}
		c.JSON(http.StatusOK, CatalogModelsResponse{Models: deployedModelsDTO(models)})
	}
}

// fullCatalogDTO is every catalog.Models row in the same {model, provider}
// DTO as the strategy-scoped lists, independent of the serving strategy.
func fullCatalogDTO() []deployedModelDTO {
	return catalog.Listing()
}
