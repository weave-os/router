package admin

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"

	"github.com/gin-gonic/gin"
)

// CatalogModelsResponse is the shape returned by GET /v1/router/models; the
// gateway answers ?scope=catalog from the same type.
type CatalogModelsResponse = catalog.ModelListingResponse

// HMMRosterSource exposes the serving HMM policy roster arms as catalog
// entries: the deployed-model universe for discovery and admin validation.
type HMMRosterSource interface {
	HMMDeployedModels(ctx context.Context) ([]router.DeployedEntry, error)
}

// CatalogModelsHandler returns the deployed-models catalog: the serving HMM
// policy roster, or the catalog projections for ?scope=selection|catalog.
// Read-only, unauthed metadata, mounted whenever a roster source is wired.
//
// The list is publicly known (we publish per-version model registries on the
// RouterArena leaderboard) so there is no leak risk from leaving this open.
func CatalogModelsHandler(models HMMRosterSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.EqualFold(strings.TrimSpace(c.Query("scope")), string(catalog.ScopeSelection)) {
			c.JSON(http.StatusOK, catalog.ModelChoicesResponse{Models: catalog.ModelChoices()})
			return
		}
		if strings.EqualFold(strings.TrimSpace(c.Query("scope")), catalog.ScopeCatalog) {
			c.JSON(http.StatusOK, CatalogModelsResponse{Models: catalog.Listing()})
			return
		}
		entries, err := models.HMMDeployedModels(c.Request.Context())
		if err != nil {
			observability.FromGin(c).Error(
				"Failed to fetch HMM roster for deployed-models endpoint",
				"err", err,
				"strategy", strings.TrimSpace(c.Query("strategy")),
			)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "hmm roster unavailable"})
			return
		}
		c.JSON(http.StatusOK, CatalogModelsResponse{Models: entriesToDTO(entries)})
	}
}

// fullCatalogDTO is every catalog.Models row in the same {model, provider}
// DTO as the strategy-scoped lists, independent of the serving strategy.
func fullCatalogDTO() []deployedModelDTO {
	return catalog.Listing()
}
