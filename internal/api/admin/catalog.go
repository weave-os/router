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

// CatalogModelsResponse is the shape returned by GET /v1/router/models.
// Kept stable so the Weave control plane can rely on the wire format without
// re-checking the artifact JSON shape on every router gitlink bump.
type CatalogModelsResponse struct {
	Models []deployedModelDTO `json:"models"`
}

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
		if strings.EqualFold(strings.TrimSpace(c.Query("scope")), CatalogScopeCatalog) {
			c.JSON(http.StatusOK, CatalogModelsResponse{Models: fullCatalogDTO()})
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

// CatalogScopeCatalog is the query value for GET /v1/router/models?scope=catalog.
// It returns every row in catalog.Models, independent of the serving strategy.
const CatalogScopeCatalog = "catalog"

// fullCatalogDTO maps catalog.Models to the same {model, provider} DTO as the
// strategy-scoped lists. Provider is the model's primary binding so grouping
// in the settings UI stays consistent with the roster view.
func fullCatalogDTO() []deployedModelDTO {
	entries := make([]cluster.DeployedEntry, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		if m.ID == "" {
			continue
		}
		entries = append(entries, cluster.DeployedEntry{Model: m.ID, Provider: m.PrimaryProvider()})
	}
	return entriesToDTO(entries)
}
