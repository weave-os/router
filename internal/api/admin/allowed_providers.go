package admin

import (
	"errors"
	"net/http"
	"sort"

	"workweave/router/internal/auth"
	"workweave/router/internal/observability"
	"workweave/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// ProviderFenceOverrideSource reports the deployment-wide ROUTER_ALLOWED_PROVIDERS
// egress fence, if active. Implemented by *proxy.Service.
type ProviderFenceOverrideSource interface {
	HasAllowedProvidersOverride() bool
	AllowedProvidersOverride() []string
}

type allowedProvidersResponse struct {
	Available         []string `json:"available"`
	Allowed           []string `json:"allowed"`
	EnvOverrideActive bool     `json:"env_override_active"`
}

type updateAllowedProvidersRequest struct {
	Allowed []string `json:"allowed"`
}

// GetAllowedProvidersHandler returns deployed providers and the installation's
// egress fence. An empty `allowed` means unfenced (every deployed provider is
// reachable); `env_override_active` tells the UI to render read-only.
func GetAllowedProvidersHandler(authSvc *auth.Service, models DeployedModelsSource, override ProviderFenceOverrideSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}

		envActive := override != nil && override.HasAllowedProvidersOverride()
		var allowed []string
		if envActive {
			allowed = override.AllowedProvidersOverride()
		} else {
			allowed = append([]string{}, installation.AllowedProviders...)
			sort.Strings(allowed)
		}
		if allowed == nil {
			allowed = []string{}
		}

		c.JSON(http.StatusOK, allowedProvidersResponse{
			Available:         deployedProvidersDTO(models),
			Allowed:           allowed,
			EnvOverrideActive: envActive,
		})
	}
}

// UpdateAllowedProvidersHandler replaces the installation's egress fence. An
// empty list removes the fence. 400 on unknown providers; 403 if the env
// override is active.
func UpdateAllowedProvidersHandler(authSvc *auth.Service, models DeployedModelsSource, override ProviderFenceOverrideSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		if override != nil && override.HasAllowedProvidersOverride() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "Egress fence is pinned by ROUTER_ALLOWED_PROVIDERS; clear the env var to edit.",
			})
			return
		}

		var req updateAllowedProvidersRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid request body."})
			return
		}

		available := deployedProvidersDTO(models)
		valid := make(map[string]struct{}, len(available))
		for _, p := range available {
			valid[p] = struct{}{}
		}

		stored, err := authSvc.SetInstallationAllowedProviders(c.Request.Context(), installation.ExternalID, installation.ID, req.Allowed, valid)
		if err != nil {
			if errors.Is(err, auth.ErrUnknownProvider) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			log.Error("Failed to update allowed providers", "err", err, "installation_id", installation.ID)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to update allowed providers."})
			return
		}

		// Sort a copy: stored is the same slice the repository was handed.
		out := append([]string{}, stored...)
		sort.Strings(out)
		c.JSON(http.StatusOK, allowedProvidersResponse{
			Available: available,
			Allowed:   out,
		})
	}
}

var _ ProviderFenceOverrideSource = (*proxy.Service)(nil)
