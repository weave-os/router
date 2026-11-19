package proxy

import (
	"context"
	"fmt"
	"maps"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/turntype"
)

func utilityHardPinTarget(ctx context.Context, req router.Request, turnType turntype.TurnType, resolver HardPinResolver, provider, model string) (string, string, error) {
	log := observability.FromContext(ctx)
	hardPinReq := HardPinRequest{
		EnabledProviders: req.EnabledProviders,
		ExcludedModels:   req.ExcludedModels,
		CustomBindings:   req.CustomBindings,
		GatewayProviders: req.GatewayProviders,
	}
	switch {
	case resolver != nil:
		p, m, ok := resolver(withAutomaticExclusions(hardPinReq, req.AutomaticExcludedModels))
		if !ok {
			p, m, ok = resolver(hardPinReq)
			if ok {
				log.Warn("Hard-pin: automatic-routing exclusions left no candidate; ignoring them for this turn",
					"turn_type", string(turnType),
					"hard_pin_model", m,
					"hard_pin_provider", p,
				)
			}
		}
		if !ok {
			hardPinErr := router.ErrNoEligibleProvider
			switch {
			case hardPinBlockedByContextWindow(resolver, hardPinReq, req):
				hardPinErr = policy.ErrContextWindowExceeded
			case len(req.GatewayProviders) > 0:
				hardPinErr = policy.ErrGatewayServesNoDeployedModel
			}
			log.Warn(
				"Hard-pin: no eligible provider for request",
				"turn_type", string(turnType),
				"enabled_providers", sortedEnabledKeys(req.EnabledProviders),
				"err", hardPinErr,
			)
			return "", "", fmt.Errorf("hard-pin: no eligible provider for %s: %w", turnType, hardPinErr)
		}
		provider, model = p, m
	default:
		if req.EnabledProviders != nil {
			if _, enabled := req.EnabledProviders[provider]; !enabled {
				return "", "", fmt.Errorf("hard-pin provider %q is ineligible for %s: %w", provider, turnType, router.ErrNoEligibleProvider)
			}
		}
		if _, excluded := req.ExcludedModels[model]; excluded {
			return "", "", fmt.Errorf("hard-pin model %q is ineligible for %s: %w", model, turnType, router.ErrNoEligibleProvider)
		}
		if automaticallyDisabled(req, model) {
			log.Warn("Hard-pin model is disabled for automatic routing; serving it anyway",
				"turn_type", string(turnType),
				"hard_pin_model", model,
				"hard_pin_provider", provider,
			)
		}
	}
	return provider, model, nil
}

func hardPinBlockedByContextWindow(resolver HardPinResolver, hardPinReq HardPinRequest, req router.Request) bool {
	if len(req.ContextWindowExcludedModels) == 0 {
		return false
	}
	nonSizeExcluded := maps.Clone(req.ExcludedModels)
	maps.DeleteFunc(nonSizeExcluded, func(model string, _ struct{}) bool {
		_, sized := req.ContextWindowExcludedModels[model]
		return sized
	})
	hardPinReq.ExcludedModels = nonSizeExcluded
	_, _, ok := resolver(hardPinReq)
	return ok
}
