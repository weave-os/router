package admin

import (
	"net/http"

	"weave-os/router/internal/config"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

type configResponse struct {
	StickyDecisionTTL string   `json:"sticky_decision_ttl_ms"`
	OtelEnabled       bool     `json:"otel_enabled"`
	EnvProviderKeys   []string `json:"env_provider_keys"`
}

func ConfigHandler(c *gin.Context) {
	if middleware.AdminPrincipalFrom(c) == nil && middleware.InstallationFrom(c) == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
		return
	}
	providerOrder := providers.AllProviders()
	envKeyed := make([]string, 0, len(providerOrder))
	for _, p := range providerOrder {
		if config.GetOr(providers.APIKeyEnvVar(p), "") != "" {
			envKeyed = append(envKeyed, p)
		}
	}
	c.JSON(http.StatusOK, configResponse{
		StickyDecisionTTL: config.GetOr("ROUTER_STICKY_DECISION_TTL_MS", "0"),
		OtelEnabled:       config.GetOr("OTEL_EXPORTER_OTLP_ENDPOINT", "") != "",
		EnvProviderKeys:   envKeyed,
	})
}
