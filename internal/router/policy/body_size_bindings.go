package policy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func requestEligibleBindings(bindings []catalog.IndexedBinding, req router.Request) ([]catalog.IndexedBinding, ExclusionReason) {
	bindings = bodySizeEligibleBindings(bindings, req.RequestBodyBytes)
	if len(bindings) == 0 {
		return nil, ExclusionRequestBodySize
	}
	return imageEligibleBindings(bindings, req.ImageCount), ExclusionImageLimit
}

func bodySizeEligibleBindings(bindings []catalog.IndexedBinding, bodyBytes int) []catalog.IndexedBinding {
	filtered := make([]catalog.IndexedBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.AcceptsRequestBodyBytes(bodyBytes) {
			filtered = append(filtered, binding)
		}
	}
	return filtered
}

func imageEligibleBindings(bindings []catalog.IndexedBinding, imageCount int) []catalog.IndexedBinding {
	if imageCount <= 0 {
		return bindings
	}
	filtered := make([]catalog.IndexedBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.AcceptsImageCount(imageCount) {
			filtered = append(filtered, binding)
		}
	}
	return filtered
}
