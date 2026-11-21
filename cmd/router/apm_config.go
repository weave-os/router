package main

import (
	"strings"

	"weave-os/router/internal/config"
	"weave-os/router/internal/observability/apm"
)

func apmConfigFromEnv() apm.Config {
	return apm.Config{
		Endpoint:      config.GetOr("WV_APM_OTLP_ENDPOINT", ""),
		DeploymentEnv: config.GetOr("ROUTER_DEPLOYMENT_ENV", config.GetOr("ENV", "dev")),
		Insecure:      config.GetOr("WV_APM_OTLP_INSECURE", "false") == "true",
	}
}

func parseOtelHeaders(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" {
			out[k] = v
		}
	}
	return out
}
