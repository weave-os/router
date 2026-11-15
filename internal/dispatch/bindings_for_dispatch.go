package dispatch

import (
	"maps"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// BindingRequest carries the request facts OrderedBindings needs to choose
// which provider bindings one decision may walk.
type BindingRequest struct {
	Decision router.Decision
	// DeploymentKeyedProviders is nil when the
	// request may not fail over (caller credentials, failover disabled, or
	// the legacy all-registered mode), which serves the decision's provider only.
	DeploymentKeyedProviders map[string]struct{}
	// SubscriptionServes reports that a linked subscription serves the
	// decision's provider natively without a deployment key.
	SubscriptionServes bool
	// Excluded lists the providers this request may not reach.
	Excluded   map[string]struct{}
	ImageCount int
	// UserForced reports a /force-model decision (any forced reason), which
	// keeps an image-capped binding instead of failing.
	UserForced bool
	// ForceModel reports the plain /force-model reason, which never falls
	// back to an unbound provider for a known catalog model.
	ForceModel bool
}

// OrderedBindings returns the ordered binding list to walk. With no failover
// it returns a single-element slice carrying the decision's provider, and nil
// when no binding may serve the decision.
func OrderedBindings(req BindingRequest) []catalog.ProviderBinding {
	decision := req.Decision
	primary := catalog.ProviderBinding{Provider: decision.Provider}
	if req.DeploymentKeyedProviders == nil {
		if binding, ok := catalog.BindingFor(decision.Model, decision.Provider); ok &&
			!binding.AcceptsImageCount(req.ImageCount) && !req.UserForced {
			return nil
		}
		return []catalog.ProviderBinding{primary}
	}
	_, primaryExcluded := req.Excluded[decision.Provider]
	indexedBindings, imagePrimaryExcluded, ok := imageCapableBindings(catalog.EnumerateBindings(decision.Model, eligibleProviders(req)), req)
	if !ok {
		return nil
	}
	primaryExcluded = primaryExcluded || imagePrimaryExcluded
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
		if req.ForceModel {
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
	return withPrimaryFirst(bindings, primary, primaryExcluded)
}

// eligibleProviders narrows the keyed providers to the ones this request may
// reach. A linked subscription makes the selected native binding available
// without a deployment API key; lease/funding guards still control
// subscription exhaustion and fallback. Exclusions must hold during failover
// too, or a fallback binding could resurrect a provider the scorer already
// filtered out.
func eligibleProviders(req BindingRequest) map[string]struct{} {
	available := req.DeploymentKeyedProviders
	if req.SubscriptionServes {
		available = maps.Clone(available)
		available[req.Decision.Provider] = struct{}{}
	}
	if len(req.Excluded) == 0 {
		return available
	}
	filtered := make(map[string]struct{}, len(available))
	for p := range available {
		if _, drop := req.Excluded[p]; !drop {
			filtered[p] = struct{}{}
		}
	}
	return filtered
}

// imageCapableBindings drops bindings whose per-request image cap is below
// the request's image count, reporting whether that dropped the decision's
// own provider. ok is false when the cap leaves nothing to serve; a forced
// decision instead keeps every binding.
func imageCapableBindings(indexedBindings []catalog.IndexedBinding, req BindingRequest) (_ []catalog.IndexedBinding, primaryExcluded, ok bool) {
	if req.ImageCount <= 0 {
		return indexedBindings, false, true
	}
	filtered := make([]catalog.IndexedBinding, 0, len(indexedBindings))
	imageFiltered := false
	for _, binding := range indexedBindings {
		if binding.AcceptsImageCount(req.ImageCount) {
			filtered = append(filtered, binding)
			continue
		}
		imageFiltered = true
		if binding.Provider == req.Decision.Provider {
			primaryExcluded = true
		}
	}
	if !imageFiltered || len(filtered) > 0 {
		return filtered, primaryExcluded, true
	}
	if req.UserForced {
		return indexedBindings, false, true
	}
	return nil, false, false
}

// withPrimaryFirst keeps the decision's provider first. Defensive: if the
// runtime decision's provider differs from bindings[0], keep the decision's
// as primary and dedupe the rest — unless it's excluded, in which case serve
// only eligible bindings.
func withPrimaryFirst(bindings []catalog.ProviderBinding, primary catalog.ProviderBinding, primaryExcluded bool) []catalog.ProviderBinding {
	if len(bindings) == 1 && !primaryExcluded {
		return []catalog.ProviderBinding{primary}
	}
	if bindings[0].Provider == primary.Provider {
		return bindings
	}
	if primaryExcluded {
		return bindings
	}
	out := []catalog.ProviderBinding{primary}
	for _, b := range bindings {
		if b.Provider != primary.Provider {
			out = append(out, b)
		}
	}
	return out
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
