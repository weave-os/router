package policy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// ResolveOriginal authorizes one client-authoritative request to the original
// native target. It does not consult the scoring roster: a catalog model with
// a provider-supported binding is eligible even when the roster mapper would
// exclude it. An unknown catalog ID never invents a provider.
func (r *PlanResolver) ResolveOriginal(request router.Request, target TargetOverride) (ResolvedPlan, error) {
	spec, found := r.registry.Spec(PurposeOriginalModelFallback)
	if !found {
		return ResolvedPlan{}, resolutionError(ResolutionErrorUnknownPurpose, PurposeOriginalModelFallback, PolicyID(""), "purpose is not registered")
	}
	if target.Source == "" {
		target.Source = OverrideSourceClientAuthoritative
	}
	if request.ForceModel != "" && request.ForceModel != target.CatalogID {
		return ResolvedPlan{}, &ResolutionError{
			Code:      ResolutionErrorInvalidOverride,
			Purpose:   spec.Purpose,
			PolicyID:  spec.PolicyID,
			CatalogID: request.ForceModel,
			Source:    OverrideSourceRequest,
			detail:    "force-model input must match the original target",
		}
	}
	if err := validateTargetOverride(spec, target); err != nil {
		return ResolvedPlan{}, err
	}
	if _, allowed := overrideSourceSet(spec)[target.Source]; !allowed {
		return ResolvedPlan{}, &ResolutionError{
			Code:      ResolutionErrorOverrideNotAllowed,
			Purpose:   spec.Purpose,
			PolicyID:  spec.PolicyID,
			CatalogID: target.CatalogID,
			Source:    target.Source,
			detail:    "override source is not declared by the policy",
		}
	}

	budget, err := resolveBudget(spec, nil)
	if err != nil {
		return ResolvedPlan{}, err
	}

	routerRequest, allowed := restrictModels(request, []string{target.CatalogID})
	if !allowed {
		return ResolvedPlan{}, resolutionError(ResolutionErrorNoEligibleBinding, spec.Purpose, spec.PolicyID, "original target is outside the request model allowlist")
	}
	if target.Provider != "" {
		routerRequest.EnabledProviders = restrictProviders(routerRequest.EnabledProviders, target.Provider)
	}

	selected, err := r.originalBinding(spec, routerRequest, target, budget)
	if err != nil {
		return ResolvedPlan{}, err
	}
	if err := verifyHardConstraints(spec, routerRequest, budget, []plannedBinding{selected}); err != nil {
		return ResolvedPlan{}, err
	}
	return ResolvedPlan{
		purpose:           spec.Purpose,
		dispatchClass:     spec.DispatchClass,
		policyID:          spec.PolicyID,
		registryRevision:  r.registry.Revision(),
		policyRevision:    spec.PolicyRevision,
		selectionStrategy: spec.SelectionStrategy,
		selectedBinding:   selected.Binding,
		hardConstraints:   append([]Constraint(nil), spec.HardConstraints...),
		softPreferences:   append([]SoftPreference(nil), spec.SoftPreferences...),
		budget:            budget,
		provenance: PlanProvenance{
			SelectionStrategy: spec.SelectionStrategy,
			OverrideSource:    target.Source,
		},
	}, nil
}

func overrideSourceSet(spec PolicySpec) map[OverrideSource]struct{} {
	allowed := make(map[OverrideSource]struct{}, len(spec.OverridePrecedence))
	for _, source := range spec.OverridePrecedence {
		allowed[source] = struct{}{}
	}
	return allowed
}

// originalBinding prefers the roster-resolved binding so a routable model keeps
// its arm identity, then falls back to the native catalog binding for models
// the roster mapper excludes.
func (r *PlanResolver) originalBinding(spec PolicySpec, request router.Request, target TargetOverride, budget BudgetSpec) (plannedBinding, error) {
	resolved := r.candidateResolver.Resolve(request)
	binding, found := overrideBinding(resolved, target)
	if !found {
		binding, found = nativeCatalogBinding(r.candidateResolver, request, target)
	}
	if !found {
		return plannedBinding{}, resolutionError(ResolutionErrorNoEligibleBinding, spec.Purpose, spec.PolicyID, "no enabled provider binding serves the original native target")
	}
	if exceedsSpendBudget(binding.estimatedCostUSD, budget.MaxSpendUSD) {
		return plannedBinding{}, budgetResolutionError(spec, target.CatalogID, "original target exceeds the spend budget")
	}
	binding.Effort = target.Effort
	return binding, nil
}

// nativeCatalogBinding applies the candidate resolver's tenant model filters,
// context-window check, provider policy, enabled-provider set, gateway
// isolation, and custom bindings to one catalog model without requiring a
// roster mapping. Deployment-wide automatic exclusions are deliberately not
// applied: like an explicit pin, the caller named this model.
func nativeCatalogBinding(resolver *Resolver, request router.Request, target TargetOverride) (plannedBinding, bool) {
	model, ok := catalog.ByID(target.CatalogID)
	if !ok {
		return plannedBinding{}, false
	}
	if len(request.AllowedModels) > 0 {
		if _, allowed := request.AllowedModels[target.CatalogID]; !allowed {
			return plannedBinding{}, false
		}
	}
	if _, excluded := request.ExcludedModels[target.CatalogID]; excluded {
		return plannedBinding{}, false
	}
	if requiredContextTokens(request) > catalog.ContextWindowFor(target.CatalogID) {
		return plannedBinding{}, false
	}
	providerSet := request.EnabledProviders
	if providerSet == nil {
		providerSet = resolver.available
	}
	providerSet = resolver.allowedProviders(providerSet)

	var listed []catalog.IndexedBinding
	if len(request.GatewayProviders) > 0 {
		listed = gatewayBindings(target.CatalogID, request.GatewayProviders, request.CustomBindings)
	} else {
		listed = catalog.EnumerateBindingsWithCustom(target.CatalogID, providerSet, request.CustomBindings)
	}
	for _, candidate := range listed {
		if target.Provider != "" && candidate.Provider != target.Provider {
			continue
		}
		upstreamID := candidate.UpstreamID
		if upstreamID == "" {
			upstreamID = model.ID
		}
		return plannedBinding{
			Binding: Binding{
				CatalogID:    target.CatalogID,
				Provider:     candidate.Provider,
				UpstreamID:   upstreamID,
				BindingIndex: candidate.Index,
			},
			estimatedCostUSD: estimatedCostUSD(request, candidate.Price),
		}, true
	}
	return plannedBinding{}, false
}
