package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"workweave/router/internal/auth"
	"workweave/router/internal/proxy"
)

type validationRequest struct {
	Provider string `json:"provider"`
	KeyID    string `json:"key_id"`
	Model    string `json:"model"`
}

type providerValidationResponse struct {
	Provider   string `json:"provider"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	ModelCount int    `json:"model_count,omitempty"`
}

type modelValidationResponse struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	Status        string `json:"status"`
	Message       string `json:"message"`
}

type routeTestRequest struct {
	Prompt string `json:"prompt" binding:"required"`
}

type routeTestResponse struct {
	Model            string   `json:"model"`
	Provider         string   `json:"provider"`
	Reason           string   `json:"reason,omitempty"`
	CredentialSource string   `json:"credential_source,omitempty"`
	Candidates       []string `json:"candidates,omitempty"`
}

func validationCredentials(c *gin.Context, authSvc *auth.Service, provider, keyID string) (*proxy.Credentials, *auth.ExternalAPIKey, bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Provider is required."})
		return nil, nil, false
	}
	if keyID == "" {
		return nil, nil, true
	}
	installation, ok := resolveInstallation(c, authSvc)
	if !ok {
		return nil, nil, false
	}
	key, err := authSvc.ExternalAPIKeyWithCredential(c.Request.Context(), installation.ID, keyID)
	if err != nil {
		if errors.Is(err, auth.ErrExternalAPIKeyNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Provider key not found."})
		} else {
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "Could not resolve provider credential."})
		}
		return nil, nil, false
	}
	if key.Provider != provider {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Provider does not match the selected key."})
		return nil, nil, false
	}
	creds := proxy.BuildCredentialsMap([]*auth.ExternalAPIKey{key})[provider]
	return creds, key, true
}

// ValidateProviderHandler checks a configured provider through its safe model
// catalog endpoint. It never sends an inference prompt or spends tokens.
func ValidateProviderHandler(authSvc *auth.Service, proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req validationRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Provider is required."})
			return
		}
		creds, _, ok := validationCredentials(c, authSvc, req.Provider, req.KeyID)
		if !ok {
			return
		}
		models, err := proxySvc.ListUpstreamModels(c.Request.Context(), strings.TrimSpace(req.Provider), creds)
		response := providerValidationResponse{Provider: strings.TrimSpace(req.Provider)}
		switch {
		case err == nil:
			response.Status = "valid"
			response.Message = "Provider responded successfully."
			response.ModelCount = len(models)
		case errors.Is(err, proxy.ErrModelListingUnsupported):
			response.Status = "unknown"
			response.Message = "Provider is configured, but it does not expose a safe model-list endpoint."
		case errors.Is(err, proxy.ErrProviderNotConfigured):
			response.Status = "invalid"
			response.Message = "Provider is not registered or has no usable credential."
		default:
			response.Status = "invalid"
			response.Message = "Provider rejected the validation request: " + err.Error()
		}
		c.JSON(http.StatusOK, response)
	}
}

// ValidateModelHandler checks whether a model is published by a configured
// provider. For BYOK endpoints, model aliases are resolved before comparison.
func ValidateModelHandler(authSvc *auth.Service, proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req validationRequest
		if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Model) == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Provider and model are required."})
			return
		}
		provider := strings.TrimSpace(req.Provider)
		creds, key, ok := validationCredentials(c, authSvc, provider, req.KeyID)
		if !ok {
			return
		}
		models, err := proxySvc.ListUpstreamModels(c.Request.Context(), provider, creds)
		response := modelValidationResponse{Provider: provider, Model: strings.TrimSpace(req.Model)}
		if key != nil {
			response.UpstreamModel = key.ModelAliases[response.Model]
		}
		if response.UpstreamModel == "" {
			response.UpstreamModel = response.Model
		}
		if err != nil {
			if errors.Is(err, proxy.ErrModelListingUnsupported) {
				response.Status = "unknown"
				response.Message = "This provider does not expose a safe model-list endpoint."
			} else {
				response.Status = "invalid"
				response.Message = "Could not read the provider model catalog: " + err.Error()
			}
			c.JSON(http.StatusOK, response)
			return
		}
		for _, model := range models {
			if model == response.UpstreamModel {
				response.Status = "valid"
				response.Message = "Model is published by the provider."
				c.JSON(http.StatusOK, response)
				return
			}
		}
		response.Status = "invalid"
		response.Message = "Model was not found in the provider's published catalog."
		c.JSON(http.StatusOK, response)
	}
}

// RouteTestHandler previews the router's model decision for a prompt without
// dispatching an upstream request or recording serving usage.
func RouteTestHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req routeTestRequest
		if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Prompt is required."})
			return
		}
		body, _ := json.Marshal(map[string]any{
			"model":      "auto",
			"max_tokens": 256,
			"messages": []map[string]string{{
				"role":    "user",
				"content": strings.TrimSpace(req.Prompt),
			}},
		})
		decision, err := proxySvc.RouteAnthropicRequest(c.Request.Context(), body, c.Request.Header)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "Route preview failed: " + err.Error()})
			return
		}
		response := routeTestResponse{
			Model:            decision.Model,
			Provider:         decision.Provider,
			Reason:           decision.Reason,
			CredentialSource: string(decision.CredentialSource),
		}
		if decision.Metadata != nil {
			response.Candidates = decision.Metadata.CandidateModels
		}
		c.JSON(http.StatusOK, response)
	}
}
