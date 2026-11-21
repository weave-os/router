package admin

import (
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

type Config struct {
	CookieInsecure           bool
	StickyDecisionTTL        string
	OtelEnabled              bool
	HasDeploymentProviderKey func(provider string) bool
}

func (c Config) secureCookie() bool { return !c.CookieInsecure }

func (c Config) stickyDecisionTTL() string {
	if c.StickyDecisionTTL == "" {
		return "0"
	}
	return c.StickyDecisionTTL
}

func (c Config) deploymentProviderKey(provider string) bool {
	return c.HasDeploymentProviderKey != nil && c.HasDeploymentProviderKey(provider)
}

type configResponse struct {
	StickyDecisionTTL string   `json:"sticky_decision_ttl_ms"`
	OtelEnabled       bool     `json:"otel_enabled"`
	EnvProviderKeys   []string `json:"env_provider_keys"`
}

func ConfigHandler(cfg Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if middleware.AdminPrincipalFrom(c) == nil && middleware.InstallationFrom(c) == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
			return
		}
		providerOrder := providers.AllProviders()
		envKeyed := make([]string, 0, len(providerOrder))
		for _, p := range providerOrder {
			if cfg.deploymentProviderKey(p) {
				envKeyed = append(envKeyed, p)
			}
		}
		c.JSON(http.StatusOK, configResponse{
			StickyDecisionTTL: cfg.stickyDecisionTTL(),
			OtelEnabled:       cfg.OtelEnabled,
			EnvProviderKeys:   envKeyed,
		})
	}
}
