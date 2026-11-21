package logconfig

import (
	"os"
	"strings"

	"weave-os/router/internal/observability"
)

func FromEnv() observability.Config {
	_, noColorSet := os.LookupEnv("NO_COLOR")
	return observability.Config{
		Level:        observability.ParseLevel(os.Getenv("LOG_LEVEL")),
		ServiceName:  observability.ResolveServiceName(os.Getenv("NAME"), os.Getenv("OTEL_SERVICE_NAME")),
		Format:       observability.ParseFormat(os.Getenv("LOG_FORMAT")),
		Color:        observability.ParseColor(os.Getenv("LOG_COLOR"), noColorSet),
		GCPProjectID: strings.TrimSpace(os.Getenv("GCP_PROJECT_ID")),
	}
}

func Configure() {
	observability.Configure(FromEnv())
}
