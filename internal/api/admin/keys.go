package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/config"
	"weave-os/router/internal/providers"

	"github.com/gin-gonic/gin"
)

type apiKeyResponse struct {
	ID         string     `json:"id"`
	Name       *string    `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	KeySuffix  string     `json:"key_suffix"`
	Scope      string     `json:"scope"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type issueAPIKeyRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

type issueAPIKeyResponse struct {
	Key   apiKeyResponse `json:"key"`
	Token string         `json:"token"`
}

func toAPIKeyResponse(k *auth.APIKey) apiKeyResponse {
	return apiKeyResponse{
		ID:         k.ID,
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		KeySuffix:  k.KeySuffix,
		Scope:      string(k.Scope),
		LastUsedAt: k.LastUsedAt,
		CreatedAt:  k.CreatedAt,
	}
}

func ListAPIKeysHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		keys, err := authSvc.ListAPIKeys(c.Request.Context(), installation.ID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to list API keys."})
			return
		}
		out := make([]apiKeyResponse, 0, len(keys))
		for _, k := range keys {
			out = append(out, toAPIKeyResponse(k))
		}
		c.JSON(http.StatusOK, gin.H{"keys": out})
	}
}

func IssueAPIKeyHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		var req issueAPIKeyRequest
		_ = c.ShouldBindJSON(&req)
		var name *string
		if req.Name != "" {
			name = &req.Name
		}
		scope := auth.ScopeRouting
		if req.Scope != "" {
			scope = auth.APIKeyScope(req.Scope)
		}
		key, rawToken, err := authSvc.IssueScopedAPIKey(c.Request.Context(), installation.ID, scope, name, nil)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidKeyScope) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Unknown key scope."})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to issue API key."})
			return
		}
		c.JSON(http.StatusCreated, issueAPIKeyResponse{
			Key:   toAPIKeyResponse(key),
			Token: rawToken,
		})
	}
}

func RotateAPIKeyHandler(authSvc *auth.Service) gin.HandlerFunc {
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
		key, rawToken, err := authSvc.RotateAPIKey(c.Request.Context(), installation.ID, id, nil)
		if err != nil {
			if errors.Is(err, auth.ErrAPIKeyNotFound) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "API key not found."})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to rotate API key."})
			return
		}
		c.JSON(http.StatusCreated, issueAPIKeyResponse{
			Key:   toAPIKeyResponse(key),
			Token: rawToken,
		})
	}
}

func DeleteAPIKeyHandler(authSvc *auth.Service) gin.HandlerFunc {
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
		keys, err := authSvc.ListAPIKeys(c.Request.Context(), installation.ID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to look up API key."})
			return
		}
		owned := false
		for _, k := range keys {
			if k.ID == id {
				owned = true
				break
			}
		}
		if !owned {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "API key not found."})
			return
		}
		if err := authSvc.DeleteAPIKey(c.Request.Context(), installation.ID, id); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete API key."})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

type externalKeyResponse struct {
	ID                     string            `json:"id"`
	Provider               string            `json:"provider"`
	Name                   *string           `json:"name"`
	KeyPrefix              string            `json:"key_prefix"`
	KeySuffix              string            `json:"key_suffix"`
	BaseURL                string            `json:"base_url,omitempty"`
	ModelAliases           map[string]string `json:"model_aliases,omitempty"`
	IdentityHeader         string            `json:"identity_header,omitempty"`
	IdentityHeaderFormat   string            `json:"identity_header_format,omitempty"`
	ForwardedClientHeaders []string          `json:"forwarded_client_headers,omitempty"`
	BaggageHeader          string            `json:"baggage_header,omitempty"`

	AuthType         auth.AuthType                   `json:"auth_type,omitempty"`
	FederationConfig *auth.AnthropicFederationConfig `json:"federation_config,omitempty"`
	AuthAccount      string                          `json:"auth_account,omitempty"`
	AuthUser         string                          `json:"auth_user,omitempty"`
	FundingPriority  auth.FundingPriority            `json:"funding_priority"`
	LastUsedAt       *time.Time                      `json:"last_used_at"`
	CreatedAt        time.Time                       `json:"created_at"`
}

type upsertExternalKeyRequest struct {
	Provider               string                          `json:"provider" binding:"required"`
	Key                    string                          `json:"key"`
	Name                   *string                         `json:"name"`
	BaseURL                *string                         `json:"base_url"`
	ModelAliases           map[string]string               `json:"model_aliases"`
	IdentityHeader         *string                         `json:"identity_header"`
	IdentityHeaderFormat   *string                         `json:"identity_header_format"`
	ForwardedClientHeaders []string                        `json:"forwarded_client_headers"`
	BaggageHeader          *string                         `json:"baggage_header"`
	AuthType               auth.AuthType                   `json:"auth_type"`
	FederationConfig       *auth.AnthropicFederationConfig `json:"federation_config"`
	AuthAccount            *string                         `json:"auth_account"`
	AuthUser               *string                         `json:"auth_user"`
	FundingPriority        auth.FundingPriority            `json:"funding_priority"`
}

func toExternalKeyResponse(k *auth.ExternalAPIKey) externalKeyResponse {
	return externalKeyResponse{
		ID:                     k.ID,
		Provider:               k.Provider,
		Name:                   k.Name,
		KeyPrefix:              k.KeyPrefix,
		KeySuffix:              k.KeySuffix,
		BaseURL:                k.BaseURL,
		ModelAliases:           k.ModelAliases,
		IdentityHeader:         k.IdentityHeader,
		IdentityHeaderFormat:   k.IdentityHeaderFormat,
		ForwardedClientHeaders: k.ForwardedClientHeaders,
		BaggageHeader:          k.BaggageHeader,

		AuthType:         k.AuthType,
		FederationConfig: k.FederationConfig,
		AuthAccount:      k.AuthAccount,
		AuthUser:         k.AuthUser,
		FundingPriority:  k.FundingPriority,
		LastUsedAt:       k.LastUsedAt,
		CreatedAt:        k.CreatedAt,
	}
}

func ListExternalKeysHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		keys, err := authSvc.ListExternalAPIKeys(c.Request.Context(), installation.ID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to list provider keys."})
			return
		}
		out := make([]externalKeyResponse, 0, len(keys))
		for _, k := range keys {
			out = append(out, toExternalKeyResponse(k))
		}
		c.JSON(http.StatusOK, gin.H{"keys": out})
	}
}

func UpsertExternalKeyHandler(authSvc *auth.Service, models HMMRosterSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		var req upsertExternalKeyRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Provider and key are required."})
			return
		}
		if req.Key == "" && req.AuthType != auth.AuthTypeWIF && req.AuthType != auth.AuthTypeAnthropicWIF {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Provider and key are required."})
			return
		}
		anthropicFederation := req.Provider == providers.ProviderAnthropic && req.AuthType == auth.AuthTypeAnthropicWIF
		if !anthropicFederation && config.GetOr(providers.APIKeyEnvVar(req.Provider), "") != "" {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "Provider already configured via deployment environment variable. Remove the env var before adding a dashboard key."})
			return
		}
		allowed, err := deployedModelIDs(c.Request.Context(), models)
		if err != nil {
			abortRosterUnavailable(c, err)
			return
		}
		key, err := authSvc.UpsertExternalAPIKey(c.Request.Context(), installation.ID, auth.UpsertExternalAPIKeyParams{
			Provider:      req.Provider,
			RawKey:        req.Key,
			Name:          req.Name,
			BaseURL:       req.BaseURL,
			ModelAliases:  req.ModelAliases,
			AllowedModels: allowed,

			IdentityHeader:         req.IdentityHeader,
			IdentityHeaderFormat:   req.IdentityHeaderFormat,
			ForwardedClientHeaders: req.ForwardedClientHeaders,
			BaggageHeader:          req.BaggageHeader,

			AuthType:         req.AuthType,
			FederationConfig: req.FederationConfig,
			AuthAccount:      req.AuthAccount,
			AuthUser:         req.AuthUser,
			FundingPriority:  req.FundingPriority,
		})
		if err != nil {
			if errors.Is(err, auth.ErrUnknownModel) || errors.Is(err, auth.ErrInvalidModelAlias) ||
				errors.Is(err, auth.ErrInvalidIdentityHeader) || errors.Is(err, auth.ErrInvalidForwardedHeader) ||
				errors.Is(err, auth.ErrInvalidKeypairAuth) ||
				errors.Is(err, auth.ErrInvalidEntraAuth) || errors.Is(err, auth.ErrInvalidAnthropicFederation) ||
				errors.Is(err, auth.ErrInvalidFundingPriority) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			if errors.Is(err, auth.ErrInvalidBaseURL) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Base URL must be an absolute http(s) URL."})
				return
			}
			if errors.Is(err, auth.ErrBaseURLRequired) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "This provider requires a base URL — it has no default endpoint."})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to save provider key."})
			return
		}
		c.JSON(http.StatusCreated, toExternalKeyResponse(key))
	}
}

type updateExternalKeyAliasesRequest struct {
	ModelAliases map[string]string `json:"model_aliases"`
}

func UpdateExternalKeyAliasesHandler(authSvc *auth.Service, models HMMRosterSource) gin.HandlerFunc {
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
		var req updateExternalKeyAliasesRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid request body."})
			return
		}
		allowed, err := deployedModelIDs(c.Request.Context(), models)
		if err != nil {
			abortRosterUnavailable(c, err)
			return
		}
		key, err := authSvc.SetExternalAPIKeyModelAliases(c.Request.Context(), installation.ID, id, req.ModelAliases, allowed)
		if err != nil {
			if errors.Is(err, auth.ErrUnknownModel) || errors.Is(err, auth.ErrInvalidModelAlias) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			if errors.Is(err, auth.ErrExternalAPIKeyNotFound) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Provider key not found."})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to update model aliases."})
			return
		}
		c.JSON(http.StatusOK, toExternalKeyResponse(key))
	}
}

type updateExternalKeyFundingPriorityRequest struct {
	FundingPriority auth.FundingPriority `json:"funding_priority"`
}

func UpdateExternalKeyFundingPriorityHandler(authSvc *auth.Service) gin.HandlerFunc {
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
		var req updateExternalKeyFundingPriorityRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid request body."})
			return
		}
		key, err := authSvc.SetExternalAPIKeyFundingPriority(c.Request.Context(), installation.ID, id, req.FundingPriority)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidFundingPriority) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			if errors.Is(err, auth.ErrExternalAPIKeyNotFound) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Provider key not found."})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to update funding priority."})
			return
		}
		c.JSON(http.StatusOK, toExternalKeyResponse(key))
	}
}

func deployedModelIDs(ctx context.Context, models HMMRosterSource) (map[string]struct{}, error) {
	if models == nil {
		return nil, nil
	}
	deployed, err := models.HMMDeployedModels(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(deployed))
	for _, e := range deployed {
		allowed[e.Model] = struct{}{}
	}
	return allowed, nil
}

func DeleteExternalKeyHandler(authSvc *auth.Service) gin.HandlerFunc {
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
		if err := authSvc.DeleteExternalAPIKey(c.Request.Context(), installation.ID, id); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete provider key."})
			return
		}
		c.Status(http.StatusNoContent)
	}
}
