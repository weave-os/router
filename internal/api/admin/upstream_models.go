package admin

import (
	"errors"
	"net/http"

	"workweave/router/internal/auth"
	"workweave/router/internal/observability"
	"workweave/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// discoverModelsRequest carries an unsaved key's connection details so the
// dashboard can list an endpoint's models before the key is persisted. Only
// bearer credentials are accepted here; derived auth types (key pair, WIF)
// need the stored key, so those flows save first and use the GET route.
type discoverModelsRequest struct {
	Provider string  `json:"provider" binding:"required"`
	Key      string  `json:"key" binding:"required"`
	BaseURL  *string `json:"base_url"`
}

// DiscoverModelsHandler lists the models an endpoint publishes using
// credentials from the request body, for keys not yet saved. The credential is
// used for the single upstream call and never persisted or echoed back.
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

// ListUpstreamModelsHandler returns the model IDs a saved BYOK key's endpoint
// publishes, so the dashboard can offer them as alias targets instead of
// hand-typed names. 501 means the provider has no model-listing surface and
// manual alias entry remains the path.
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

// abortForListingError maps a model-listing failure to an HTTP status: 501
// tells the dashboard to keep manual alias entry, anything else is the
// endpoint's fault and maps to 502.
func abortForListingError(c *gin.Context, provider, keyID string, err error) {
	if errors.Is(err, proxy.ErrModelListingUnsupported) || errors.Is(err, proxy.ErrProviderNotConfigured) {
		c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{"error": "This provider does not support model listing; enter aliases manually."})
		return
	}
	observability.FromGin(c).Warn("Upstream model listing failed",
		"external_api_key_id", keyID, "provider", provider, "err", err)
	c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "The endpoint did not return a model list: " + err.Error()})
}
