package main

import (
	"strings"

	"weave-os/router/internal/config"
)

func excludedModelsFromEnv() []string {
	excludedRaw := strings.TrimSpace(config.GetOr("ROUTER_EXCLUDED_MODELS", ""))
	if excludedRaw == "" {
		return nil
	}
	parts := strings.Split(excludedRaw, ",")
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned
}
