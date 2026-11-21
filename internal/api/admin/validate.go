package admin

import (
	"net/http"

	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

type validateResponse struct {
	Valid        bool                  `json:"valid"`
	Installation validatedInstallation `json:"installation"`
	Shared       bool                  `json:"shared"`
}

type validatedInstallation struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func ValidateHandler(c *gin.Context) {
	installation := middleware.InstallationFrom(c)
	key := middleware.APIKeyFrom(c)
	if installation == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
		return
	}
	c.JSON(http.StatusOK, validateResponse{
		Valid: true,
		Installation: validatedInstallation{
			ID:   installation.ID,
			Name: installation.Name,
		},
		Shared: key == nil || key.CredentialSubjectID == "",
	})
}
