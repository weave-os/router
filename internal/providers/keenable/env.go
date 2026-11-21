package keenable

import (
	"log/slog"

	"weave-os/router/internal/websearch"
)

func FromEnv(getenv func(key, defaultValue string) string, logger *slog.Logger) websearch.Executor {
	if getenv("ROUTER_KEENABLE_WEB_SEARCH", "false") != "true" {
		return nil
	}
	client := NewClient(getenv("KEENABLE_BASE_URL", DefaultBaseURL), WithAPIKey(getenv("KEENABLE_API_KEY", "")))
	logger.Info("Keenable web-search fallback enabled", "key_configured", !client.Keyless())
	return client
}
