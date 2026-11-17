package dispatch

import "weave-os/router/internal/router/catalog"

func bodySizeEligibleBindings(indexedBindings []catalog.IndexedBinding, req BindingRequest) (_ []catalog.IndexedBinding, primaryExcluded, ok bool) {
	filtered := make([]catalog.IndexedBinding, 0, len(indexedBindings))
	for _, binding := range indexedBindings {
		if binding.AcceptsRequestBodyBytes(req.RequestBodyBytes) {
			filtered = append(filtered, binding)
			continue
		}
		if binding.Provider == req.Decision.Provider {
			primaryExcluded = true
		}
	}
	if len(filtered) == 0 && len(indexedBindings) > 0 {
		return nil, false, false
	}
	return filtered, primaryExcluded, true
}
