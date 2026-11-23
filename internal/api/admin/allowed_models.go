package admin

import (
	"errors"
	"net/http"
	"sort"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

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

func GetAllowedModelsHandler(authSvc *auth.Service) gin.HandlerFunc {
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

func UpdateAllowedModelsHandler(authSvc *auth.Service, routable RoutableModelsSource) gin.HandlerFunc {
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

		allowed := make(map[string]struct{})
		for _, e := range fullCatalogDTO() {
			allowed[e.Model] = struct{}{}
		}

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
