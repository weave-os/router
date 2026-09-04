package admin

import (
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
	Name string `json:"name"`
	// Scope defaults to routing when omitted, so existing clients keep issuing
	// data-plane keys.
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

// IssueAPIKeyHandler creates a new router API key for the installation. An
// installation may hold multiple active keys at a time; callers issue, rotate,
// and revoke them individually. The optional scope picks between a routing
// (rk_) key and a read-only analytics (ra_) key.
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

// RotateAPIKeyHandler soft-deletes the specified key and issues a replacement
// against the same installation, carrying forward the previous key's name.
// 404 when the id is not owned by the caller's installation.
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

// DeleteAPIKeyHandler soft-deletes a router API key. Returns 404 for keys
// owned by another installation so a tenant who learns a foreign key UUID
// cannot revoke it.
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

	AuthType    string     `json:"auth_type,omitempty"`
	AuthAccount string     `json:"auth_account,omitempty"`
	AuthUser    string     `json:"auth_user,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

type upsertExternalKeyRequest struct {
	Provider string `json:"provider" binding:"required"`
	// Key is required for every auth type but "wif", which carries no secret;
	// the binding can't express that, so the emptiness check lives below.
	Key  string  `json:"key"`
	Name *string `json:"name"`
	// BaseURL points this key at a non-default endpoint. Required for gateway
	// providers, which have no deployment default to fall back to.
	BaseURL *string `json:"base_url"`
	// ModelAliases maps catalog model IDs to the IDs this endpoint publishes
	// them under, for endpoints with their own naming scheme.
	ModelAliases map[string]string `json:"model_aliases"`
	// IdentityHeader names the header carrying the caller's identity to this endpoint;
	// for service-authenticated endpoints that attribute spend per user. Format: "email" or "json".
	IdentityHeader       *string `json:"identity_header"`
	IdentityHeaderFormat *string `json:"identity_header_format"`
	// ForwardedClientHeaders and BaggageHeader configure client-header passthrough to this endpoint.
	ForwardedClientHeaders []string `json:"forwarded_client_headers"`
	BaggageHeader          *string  `json:"baggage_header"`
	// AuthType is "bearer" (default), "keypair_jwt" (Key is RSA; router signs a short-lived JWT
	// for AuthAccount/AuthUser), or "wif" (no secret; router attests its own workload identity).
	AuthType    string  `json:"auth_type"`
	AuthAccount *string `json:"auth_account"`
	AuthUser    *string `json:"auth_user"`
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

		AuthType:    k.AuthType,
		AuthAccount: k.AuthAccount,
		AuthUser:    k.AuthUser,
		LastUsedAt:  k.LastUsedAt,
		CreatedAt:   k.CreatedAt,
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

// UpsertExternalKeyHandler stores a BYOK provider key; models (non-nil) validates aliases
// against the deployed catalog so a typo'd ID fails at write time, not silently at runtime.
func UpsertExternalKeyHandler(authSvc *auth.Service, models DeployedModelsSource) gin.HandlerFunc {
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
		if req.Key == "" && req.AuthType != auth.AuthTypeWIF {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Provider and key are required."})
			return
		}
		// A provider configured via the deployment's env var (e.g. ANTHROPIC_API_KEY)
		// must not be shadowed by a dashboard BYOK key — credential resolution
		// prefers BYOK, so the stored key would silently win on every outbound call.
		// The frontend grays out env-keyed providers, but that guard is derived from
		// GET /admin/v1/config and fails open if that fetch errors; this is the only
		// backend enforcement. Mirrors the env-key check in ConfigHandler.
		if config.GetOr(providers.APIKeyEnvVar(req.Provider), "") != "" {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "Provider already configured via deployment environment variable. Remove the env var before adding a dashboard key."})
			return
		}
		allowed := deployedModelIDs(models)
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

			AuthType:    req.AuthType,
			AuthAccount: req.AuthAccount,
			AuthUser:    req.AuthUser,
		})
		if err != nil {
			if errors.Is(err, auth.ErrUnknownModel) || errors.Is(err, auth.ErrInvalidModelAlias) ||
				errors.Is(err, auth.ErrInvalidIdentityHeader) || errors.Is(err, auth.ErrInvalidForwardedHeader) ||
				errors.Is(err, auth.ErrInvalidKeypairAuth) ||
				errors.Is(err, auth.ErrInvalidEntraAuth) {
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
	// ModelAliases replaces the key's whole map; an empty object clears it.
	ModelAliases map[string]string `json:"model_aliases"`
}

// UpdateExternalKeyAliasesHandler edits a stored key's model aliases without re-entering
// the credential; returns 404 for missing or cross-tenant ids.
func UpdateExternalKeyAliasesHandler(authSvc *auth.Service, models DeployedModelsSource) gin.HandlerFunc {
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
		key, err := authSvc.SetExternalAPIKeyModelAliases(c.Request.Context(), installation.ID, id, req.ModelAliases, deployedModelIDs(models))
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

// deployedModelIDs is the alias-validation set; nil source means skip validation.
func deployedModelIDs(models DeployedModelsSource) map[string]struct{} {
	if models == nil {
		return nil
	}
	deployed := models.DefaultDeployedModels()
	allowed := make(map[string]struct{}, len(deployed))
	for _, e := range deployed {
		allowed[e.Model] = struct{}{}
	}
	return allowed
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
