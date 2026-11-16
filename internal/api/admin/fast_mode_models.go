package admin

import (
	"errors"
	"net/http"
	"sort"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

type fastModeModelsResponse struct {
	Available []deployedModelDTO `json:"available"`
	FastMode  []string           `json:"fast_mode"`
}

type updateFastModeModelsRequest struct {
	FastMode []string `json:"fast_mode"`
}

func fastCapableCatalogDTO() []deployedModelDTO {
	out := make([]deployedModelDTO, 0)
	for _, e := range fullCatalogDTO() {
		if e.FastMode {
			out = append(out, e)
		}
	}
	return out
}

func GetFastModeModelsHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}

		fastMode := append([]string{}, installation.FastModeModels...)
		sort.Strings(fastMode)

		c.JSON(http.StatusOK, fastModeModelsResponse{
			Available: fastCapableCatalogDTO(),
			FastMode:  fastMode,
		})
	}
}

func UpdateFastModeModelsHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}

		var req updateFastModeModelsRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid request body."})
			return
		}

		fastCapable := make(map[string]struct{})
		for _, e := range fastCapableCatalogDTO() {
			fastCapable[e.Model] = struct{}{}
		}

		stored, err := authSvc.SetInstallationFastModeModels(c.Request.Context(), installation.ExternalID, installation.ID, req.FastMode, fastCapable)
		if err != nil {
			if errors.Is(err, auth.ErrModelNotFastCapable) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			log.Error("Failed to update fast-mode models", "err", err, "installation_id", installation.ID)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to update fast-mode models."})
			return
		}

		sort.Strings(stored)
		c.JSON(http.StatusOK, fastModeModelsResponse{
			Available: fastCapableCatalogDTO(),
			FastMode:  stored,
		})
	}
}
