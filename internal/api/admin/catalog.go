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

type CatalogModelsResponse = catalog.ModelListingResponse

type HMMRosterSource interface {
	HMMDeployedModels(ctx context.Context) ([]router.DeployedEntry, error)
}

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

func fullCatalogDTO() []deployedModelDTO {
	return catalog.Listing()
}
