package admin

import (
	"errors"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// discoverModelsRequest carries an unsaved key's connection details for pre-save model discovery.
// Only bearer auth is accepted; derived types (key-pair, WIF) need the stored secret and use the GET route.
type discoverModelsRequest struct {
	Provider string  `json:"provider" binding:"required"`
	Key      string  `json:"key" binding:"required"`
	BaseURL  *string `json:"base_url"`
}

// DiscoverModelsHandler lists models an endpoint publishes using unsaved bearer credentials.
// The credential is used for a single upstream call; it is never persisted or echoed back.
func DiscoverModelsHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req discoverModelsRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Provider and key are required."})
			return
		}
		baseURL, err := auth.NormalizeBaseURL(req.BaseURL)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		key := &auth.ExternalAPIKey{Provider: req.Provider, Plaintext: []byte(req.Key)}
		if baseURL != nil {
			key.BaseURL = *baseURL
		}
		creds := proxy.BuildCredentialsMap([]*auth.ExternalAPIKey{key})[req.Provider]
		models, err := proxySvc.ListUpstreamModels(c.Request.Context(), req.Provider, creds)
		if err != nil {
			abortForListingError(c, req.Provider, "", err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"models": models})
	}
}

// ListUpstreamModelsHandler returns model IDs a saved BYOK endpoint publishes.
// 501 means no listing surface; keep manual alias entry.
func ListUpstreamModelsHandler(authSvc *auth.Service, proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		id := c.Param("id")
		if id == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Missing ID."})
			return
		}
		key, err := authSvc.ExternalAPIKeyWithCredential(c.Request.Context(), installation.ID, id)
		if err != nil {
			if errors.Is(err, auth.ErrExternalAPIKeyNotFound) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Provider key not found."})
				return
			}
			if errors.Is(err, auth.ErrUpstreamCredentialUnavailable) {
				c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "Could not resolve this key's upstream credential."})
				return
			}
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

// abortForListingError maps listing errors to HTTP: 501 = no surface (keep manual entry), else 502.
func abortForListingError(c *gin.Context, provider, keyID string, err error) {
	if errors.Is(err, proxy.ErrModelListingUnsupported) || errors.Is(err, proxy.ErrProviderNotConfigured) {
		c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{"error": "This provider does not support model listing; enter aliases manually."})
		return
	}
	observability.FromGin(c).Warn("Upstream model listing failed",
		"external_api_key_id", keyID, "provider", provider, "err", err)
	c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "The endpoint did not return a model list: " + err.Error()})
}
