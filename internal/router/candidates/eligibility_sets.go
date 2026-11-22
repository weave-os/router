package candidates

import (
	"slices"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

type ExclusionSources struct {
	Override             map[string]struct{}
	ProductIneligible    map[string]struct{}
	InstallationExcluded []string
	Allowed              map[string]struct{}
	Available            map[string]struct{}
	GatewayUnserved      func() map[string]struct{}
}

func ExcludedModels(src ExclusionSources) map[string]struct{} {
	if src.Override != nil {
		return MergeExcludedModels(src.Override, src.ProductIneligible)
	}
	out := make(map[string]struct{}, len(src.InstallationExcluded))
	for _, m := range src.InstallationExcluded {
		out[m] = struct{}{}
	}
	for m := range src.ProductIneligible {
		out[m] = struct{}{}
	}
	if src.Allowed != nil {
		for model := range RoutableUniverse(src.Available) {
			if _, ok := src.Allowed[model]; !ok {
				out[model] = struct{}{}
			}
		}
	}
	if src.GatewayUnserved != nil {
		for model := range src.GatewayUnserved() {
			out[model] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func RoutableUniverse(available map[string]struct{}) map[string]struct{} {
	if available != nil {
		return available
	}
	out := make(map[string]struct{}, len(catalog.Models))
	for _, m := range catalog.Models {
		out[m.ID] = struct{}{}
	}
	return out
}

func IntersectAllowed(allowed, restriction map[string]struct{}) map[string]struct{} {
	if restriction == nil {
		return allowed
	}
	if allowed == nil {
		return restriction
	}
	out := make(map[string]struct{}, len(restriction))
	for m := range restriction {
		if _, ok := allowed[m]; ok {
			out[m] = struct{}{}
		}
	}
	return out
}

func PreferredModels(subscriptionModels, installationPreferred []string) []string {
	preferred := slices.Clone(subscriptionModels)
	for _, model := range installationPreferred {
		if !slices.Contains(preferred, model) {
			preferred = append(preferred, model)
		}
	}
	return preferred
}

func RestrictToTier(excluded map[string]struct{}, tier catalog.Tier, available map[string]struct{}) (map[string]struct{}, bool) {
	if tier == catalog.TierUnknown {
		return excluded, false
	}
	out := make(map[string]struct{}, len(excluded))
	for k := range excluded {
		out[k] = struct{}{}
	}
	inTierEligible := 0
	consider := func(model string) {
		if catalog.TierFor(model) == tier {
			if _, alreadyExcluded := excluded[model]; !alreadyExcluded {
				inTierEligible++
			}
			return
		}
		out[model] = struct{}{}
	}
	if available != nil {
		for model := range available {
			consider(model)
		}
	} else {
		for _, m := range catalog.Models {
			consider(m.ID)
		}
	}
	if inTierEligible == 0 {
		return excluded, false
	}
	return out, true
}

func ExcludeCodexOAuthOnlyModels(enabledProviders, excluded map[string]struct{}, subscriptionCanAttempt func(model string) bool) map[string]struct{} {
	for _, model := range catalog.Models {
		if subscriptionCanAttempt(model.ID) {
			continue
		}
		for _, binding := range model.Providers {
			if enabledProviders != nil {
				if _, enabled := enabledProviders[binding.Provider]; !enabled {
					continue
				}
			}
			if binding.Provider == providers.ProviderOpenAI {
				excluded = ExcludingModel(excluded, model.ID)
			}
			break
		}
	}
	return excluded
}
