package candidates

import (
	"slices"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

const NativeWebSearchPassthroughReason = "native_web_search_passthrough"

func RequestedModelServable(req router.Request, sessionDemotedModels []string) bool {
	return !slices.Contains(sessionDemotedModels, req.RequestedModel) && catalog.PermittedBy(req.ProductEligibility, req.RequestedModel)
}

func RequestedModelDecision(req router.Request, provider, reason string) router.Decision {
	return router.Decision{Provider: provider, Model: req.RequestedModel, Reason: reason}
}

func NativeWebSearchPassthrough(req router.Request, model string) (router.Decision, bool) {
	_, excluded := req.Candidates.ExcludedModels[model]
	_, safetyExcluded := req.Candidates.SafetyExcludedModels[model]
	_, allowlisted := req.Candidates.AllowedModels[model]
	allowed := req.Candidates.AllowedModels == nil || allowlisted
	binding, bindingFound := catalog.ResolveBindingWithCustom(model, req.EnabledProviders, req.CustomBindings)
	if excluded || safetyExcluded || !allowed || !bindingFound || binding.Provider != providers.ProviderAnthropic {
		return router.Decision{}, false
	}
	return router.Decision{Provider: binding.Provider, Model: model, Reason: NativeWebSearchPassthroughReason}, true
}
