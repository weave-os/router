package main

import (
	"log/slog"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/routingpolicy/overrides"
)

func selectHardPinResolver(logger *slog.Logger, hardPinOverrideConfigured, byokOnly bool, hardPinModel string) proxy.HardPinResolver {
	var hardPinResolver proxy.HardPinResolver
	if !hardPinOverrideConfigured {
		hardPinResolver = overrides.CatalogUtilityHardPin
		logger.Info("Hard-pin resolver wired (per-request catalog utility target, applies excluded_models)", "default_model", policy.UtilityHardPinDefaultModel, "byok_only", byokOnly)
	} else {
		logger.Info("Hard-pin resolver not wired: ROUTER_HARD_PIN_MODEL operator override is set and absolute by design", "model", hardPinModel)
	}
	return hardPinResolver
}
