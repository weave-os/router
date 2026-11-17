package proxy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func CatalogUtilityHardPin(req HardPinRequest) (provider, model string, ok bool) {
	return policy.UtilityTarget(policy.UtilityTargetRequest{
		EnabledProviders: req.EnabledProviders,
		ExcludedModels:   req.ExcludedModels,
		Bindings:         router.RequestBindings{Custom: req.CustomBindings, Gateways: req.GatewayProviders},
	})
}
