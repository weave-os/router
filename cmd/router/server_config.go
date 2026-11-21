package main

import (
	"os"
	"strings"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/config"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
)

func defaultStrategyFromEnv() (router.Strategy, error) {
	return server.ParseDefaultStrategy(os.Getenv("ROUTER_DEFAULT_STRATEGY"))
}

func internalServiceTokenFromEnv() string {
	return strings.TrimSpace(os.Getenv("ROUTER_INTERNAL_SERVICE_TOKEN"))
}

func adminConfigFromEnv() admin.Config {
	return admin.Config{
		CookieInsecure:           os.Getenv("ROUTER_COOKIE_INSECURE") == "true",
		StickyDecisionTTL:        config.GetOr("ROUTER_STICKY_DECISION_TTL_MS", "0"),
		OtelEnabled:              config.GetOr("OTEL_EXPORTER_OTLP_ENDPOINT", "") != "",
		HasDeploymentProviderKey: deploymentProviderKeyFromEnv,
	}
}

func deploymentProviderKeyFromEnv(provider string) bool {
	return config.GetOr(providers.APIKeyEnvVar(provider), "") != ""
}

func middlewareConfigFromEnv() middleware.Config {
	return middleware.Config{
		InferenceOrganizationAllowlist: middleware.ParseOrganizationAllowlist(config.GetOr("ROUTER_INFERENCE_ORGANIZATION_ALLOWLIST", "")),
	}
}
