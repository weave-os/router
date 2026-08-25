package admin

import (
	"errors"
	"net/http"
	"sort"

	"workweave/router/internal/auth"
	"workweave/router/internal/observability"

	"github.com/gin-gonic/gin"
)

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
// 400 on unknown model IDs. Unlike the exclusion list there is no env override
// to contend with — the allowlist is purely a per-installation control.
func UpdateAllowedModelsHandler(authSvc *auth.Service, _ DeployedModelsSource) gin.HandlerFunc {
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
