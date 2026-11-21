package candidates

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

const PolicyDeadlineDefaultReason = "policy_deadline_default_model"

func PolicyDeadlineDefault(model string, req router.Request, servedProviders []string) (router.Decision, bool) {
	if model == "" {
		return router.Decision{}, false
	}
	if _, excluded := req.Candidates.ExcludedModels[model]; excluded {
		return router.Decision{}, false
	}
	if _, excluded := req.Candidates.SafetyExcludedModels[model]; excluded {
		return router.Decision{}, false
	}

	providerSet := make(map[string]struct{}, len(servedProviders))
	for _, provider := range servedProviders {
		if req.EnabledProviders != nil {
			if _, enabled := req.EnabledProviders[provider]; !enabled {
				continue
			}
		}
		providerSet[provider] = struct{}{}
	}
	binding, ok := catalog.ResolveBinding(model, providerSet)
	if !ok {
		return router.Decision{}, false
	}
	return router.Decision{
		Provider: binding.Provider,
		Model:    model,
		Reason:   PolicyDeadlineDefaultReason,
	}, true
}
