package admin

import (
	"errors"
	"net/http"
	"sort"

	"workweave/router/internal/auth"
	"workweave/router/internal/observability"
	"workweave/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// RoutableModelsSource reports the models this deployment can route;
// nil skips the intersection guard.
type RoutableModelsSource interface {
	RoutableModels() map[string]struct{}
}

type allowedModelsResponse struct {
	Available []deployedModelDTO `json:"available"`
	Allowed   []string           `json:"allowed"`
}

type updateAllowedModelsRequest struct {
	Allowed []string `json:"allowed"`
}

// GetAllowedModelsHandler returns deployed models and the installation's
// positive allowlist. An empty list means no restriction — every deployed
// model is routable, subject to the separate exclusion list.
func GetAllowedModelsHandler(authSvc *auth.Service, _ DeployedModelsSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}

		allowed := append([]string{}, installation.AllowedModels...)
		sort.Strings(allowed)

		c.JSON(http.StatusOK, allowedModelsResponse{
			Available: fullCatalogDTO(),
			Allowed:   allowed,
		})
	}
}

// UpdateAllowedModelsHandler replaces the installation's positive allowlist.
// 400 on unknown model IDs, and on an allowlist with no routable member.
// Unlike the exclusion list there is no env override to contend with.
func UpdateAllowedModelsHandler(authSvc *auth.Service, _ DeployedModelsSource, routable RoutableModelsSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}

		var req updateAllowedModelsRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid request body."})
			return
		}

		// Membership is catalog-wide so force-model can name non-roster rows.
		allowed := make(map[string]struct{})
		for _, e := range fullCatalogDTO() {
			allowed[e.Model] = struct{}{}
		}

		// A list naming only non-routable rows empties the candidate pool;
		// require at least one routable survivor.
		if allowlistLosesRoutability(req.Allowed, allowed, routable) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "Allowlist selects no model this deployment can route. Add at least one routable model; the rest are reachable only via force-model or passthrough.",
			})
			return
		}

		stored, err := authSvc.SetInstallationAllowedModels(c.Request.Context(), installation.ExternalID, installation.ID, req.Allowed, allowed)
		if err != nil {
			if errors.Is(err, auth.ErrUnknownModel) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			log.Error("Failed to update allowed models", "err", err, "installation_id", installation.ID)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to update allowed models."})
			return
		}

		sort.Strings(stored)
		c.JSON(http.StatusOK, allowedModelsResponse{
			Available: fullCatalogDTO(),
			Allowed:   stored,
		})
	}
}

// allowlistLosesRoutability reports whether saving models would leave
// the routed candidate pool empty. Returns false (no objection) when
// the list is empty, any ID is unknown, or the routable universe is nil.
func allowlistLosesRoutability(models []string, catalogIDs map[string]struct{}, routable RoutableModelsSource) bool {
	if len(models) == 0 || routable == nil {
		return false
	}
	universe := routable.RoutableModels()
	if len(universe) == 0 {
		return false
	}
	for _, m := range models {
		if _, known := catalogIDs[m]; !known {
			return false
		}
		if _, ok := universe[m]; ok {
			return false
		}
	}
	return true
}

var _ RoutableModelsSource = (*proxy.Service)(nil)
