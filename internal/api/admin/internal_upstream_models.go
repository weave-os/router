package admin

import (
	"errors"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// internalUpstreamModelsRequest names the saved key to list models for. The
// control plane owns the installation row, so it passes the installation ID it
// already holds rather than the router re-deriving one from a session.
type internalUpstreamModelsRequest struct {
	InstallationID string `json:"installation_id" binding:"required"`
	KeyID          string `json:"key_id" binding:"required"`
}

// InternalListUpstreamModelsHandler lists the models a saved BYOK endpoint
// publishes, for the Weave control plane. Key-pair and workload-identity
// credentials are minted per request and never leave the router, so the
// control plane cannot make this call itself.
func InternalListUpstreamModelsHandler(authSvc *auth.Service, proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req internalUpstreamModelsRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Installation ID and key ID are required."})
			return
		}
		key, err := authSvc.ExternalAPIKeyWithCredential(c.Request.Context(), req.InstallationID, req.KeyID)
		if err != nil {
			if errors.Is(err, auth.ErrExternalAPIKeyNotFound) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Provider key not found."})
				return
			}
			if errors.Is(err, auth.ErrUpstreamCredentialUnavailable) {
				c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "Could not resolve this key's upstream credential."})
				return
			}
			observability.FromGin(c).Error("Failed to load provider key for model discovery",
				"installation_id", req.InstallationID, "external_api_key_id", req.KeyID, "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to load provider key."})
			return
		}
		creds := proxy.BuildCredentialsMap([]*auth.ExternalAPIKey{key})[key.Provider]
		models, err := proxySvc.ListUpstreamModels(c.Request.Context(), key.Provider, creds)
		if err != nil {
			abortForListingError(c, key.Provider, key.ID, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"models": models})
	}
}
