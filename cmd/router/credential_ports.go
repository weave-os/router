package main

import (
	"log/slog"
	"strings"
	"weave-os/router/internal/config"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/server"
	"weave-os/router/internal/subscriptions"
)

func withCredentialPorts(features server.Features) server.Features {
	features.SubscriptionCoverage = credentials.RequestPresentsCoveringSubscription
	features.CustomerFunding = credentials.RequestHasCustomerFunding
	return features
}

func managedSubscriptionLeaser(runtime *subscriptions.Runtime) subscriptions.Leaser {
	if runtime == nil {
		return nil
	}
	return runtime
}

func excludedProvidersFromEnv(logger *slog.Logger) map[string]struct{} {
	excludedRaw := strings.TrimSpace(config.GetOr("ROUTER_EXCLUDED_PROVIDERS", ""))
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
	logger.Info("Provider exclusion override active", "excluded_providers", cleaned)
	if len(cleaned) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(cleaned))
	for _, p := range cleaned {
		set[p] = struct{}{}
	}
	return set
}
