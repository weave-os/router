package main

import (
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
