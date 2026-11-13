package proxy

import (
	"context"
	"maps"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

// deploymentFundedWalk reports whether the dispatch walk is funded by
// deployment credentials: deployment keys exist, the deployment is not
// BYOK-only, and no credential was resolved for the decision's provider.
// An unrelated BYOK or client key does not lift the catalog check.
func deploymentFundedWalk(ctx context.Context, deploymentKeyedProviders map[string]struct{}, byokOnly bool) bool {
	return deploymentKeyedProviders != nil && !byokOnly && CredentialsFromContext(ctx) == nil
}

// resolveBindingsForDispatch returns the ordered binding list to walk.
// When failover is disabled or unavailable, returns a single-element
// slice carrying the already-resolved decision provider.
func (s *Service) resolveBindingsForDispatch(ctx context.Context, decision router.Decision) []catalog.ProviderBinding {
	primary := catalog.ProviderBinding{Provider: decision.Provider, ServesAliases: providers.IsGateway(decision.Provider)}
	imageCount := requestImageCountFromContext(ctx)
	if !s.shouldFailover(ctx) {
		if binding, ok := catalog.BindingFor(decision.Model, decision.Provider); ok &&
			!binding.AcceptsImageCount(imageCount) && !isUserForcedReason(decision.Reason) {
			return nil
		}
		return []catalog.ProviderBinding{primary}
	}
	available := s.deploymentKeyedProviders
	if available == nil {
		// Legacy "all registered" mode — fall back to single-attempt to
		// avoid retrying on providers whose keys aren't actually wired.
		if binding, ok := catalog.BindingFor(decision.Model, decision.Provider); ok &&
			!binding.AcceptsImageCount(imageCount) && !isUserForcedReason(decision.Reason) {
			return nil
		}
		return []catalog.ProviderBinding{primary}
	}
	// A linked subscription makes the selected native binding available
	// without a deployment API key. Extend only this request's binding set;
	// lease/funding guards still control subscription exhaustion and fallback.
	if managedSubscriptionCanServe(ctx, decision.Provider, decision.Model) && s.supportsSubscriptionTransport(decision.Provider) {
		available = maps.Clone(available)
		available[decision.Provider] = struct{}{}
	}
	// Exclusions must hold during failover too, or a fallback binding could
	// resurrect a provider the scorer already filtered out.
	excluded := s.excludedProvidersForRequest(ctx)
	if len(excluded) > 0 {
		filtered := make(map[string]struct{}, len(available))
		for p := range available {
			if _, drop := excluded[p]; !drop {
				filtered[p] = struct{}{}
			}
		}
		available = filtered
	}
	_, primaryExcluded := excluded[decision.Provider]
	indexedBindings := catalog.EnumerateBindings(decision.Model, available)
	unfilteredBindings := indexedBindings
	imagePrimaryExcluded := false
	imageFiltered := false
	if imageCount > 0 {
		filtered := make([]catalog.IndexedBinding, 0, len(indexedBindings))
		for _, binding := range indexedBindings {
			if binding.AcceptsImageCount(imageCount) {
				filtered = append(filtered, binding)
				continue
			}
			imageFiltered = true
			if binding.Provider == decision.Provider {
				imagePrimaryExcluded = true
			}
		}
		indexedBindings = filtered
	}
	if imageFiltered && len(indexedBindings) == 0 {
		if isUserForcedReason(decision.Reason) {
			indexedBindings = unfilteredBindings
		} else {
			return nil
		}
	} else {
		primaryExcluded = primaryExcluded || imagePrimaryExcluded
	}
	bindings := make([]catalog.ProviderBinding, 0, len(indexedBindings))
	for _, binding := range indexedBindings {
		bindings = append(bindings, binding.ProviderBinding)
	}
	if len(bindings) == 0 {
		if primaryExcluded {
			// Decision names an excluded provider with no other bindings (a
			// bug upstream) — return nil so dispatch 502s instead of
			// dispatching to the forbidden provider.
			return nil
		}
		if decision.Reason == translate.ReasonUserForceModel {
			if _, known := catalog.ByID(decision.Model); known {
				return nil
			}
		}
		return []catalog.ProviderBinding{primary}
	}
	if decision.Metadata != nil && decision.Metadata.SelectedArmID != "" {
		bindings, found := prioritizeSelectedArmBinding(
			indexedBindings,
			decision.Model,
			decision.Provider,
			decision.Metadata.SelectedUpstreamID,
			decision.Metadata.BindingIndex,
		)
		if found {
			return bindings
		}
	}
	if len(bindings) == 1 && !primaryExcluded {
		return []catalog.ProviderBinding{primary}
	}
	// Defensive: if the runtime decision's provider differs from
	// bindings[0], keep the decision's as primary and dedupe the rest —
	// unless it's excluded, in which case serve only eligible bindings.
	if bindings[0].Provider != decision.Provider {
		if primaryExcluded {
			return bindings
		}
		out := []catalog.ProviderBinding{primary}
		for _, b := range bindings {
			if b.Provider != decision.Provider {
				out = append(out, b)
			}
		}
		return out
	}
	return bindings
}

func prioritizeSelectedArmBinding(
	bindings []catalog.IndexedBinding,
	catalogID string,
	provider string,
	upstreamID string,
	bindingIndex int,
) ([]catalog.ProviderBinding, bool) {
	ordered := make([]catalog.ProviderBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Index != bindingIndex || binding.Provider != provider {
			continue
		}
		if catalog.UpstreamIDFor(catalogID, binding.UpstreamID) != upstreamID {
			return nil, false
		}
		ordered = append(ordered, binding.ProviderBinding)
		break
	}
	if len(ordered) == 0 {
		return nil, false
	}
	for _, binding := range bindings {
		if binding.Index != bindingIndex {
			ordered = append(ordered, binding.ProviderBinding)
		}
	}
	return ordered, true
}
