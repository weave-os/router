package policy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

type RoutedResolutionRequest struct {
	Purpose  Purpose
	Decision router.Decision
	Bindings []catalog.ProviderBinding
	Origin   OverrideSource
	Budget   *BudgetOverride
}

func (r *PlanResolver) ResolveRouted(request RoutedResolutionRequest) (ResolvedPlan, error) {
	spec, found := r.registry.Spec(request.Purpose)
	if !found {
		return ResolvedPlan{}, resolutionError(ResolutionErrorUnknownPurpose, request.Purpose, PolicyID(""), "purpose is not registered")
	}
	if !acceptsRoutedDecision(spec) {
		return ResolvedPlan{}, resolutionError(ResolutionErrorUnsupportedPurpose, request.Purpose, spec.PolicyID, "purpose does not accept routed decisions")
	}
	if spec.SelectionStrategy == SelectionStrategyDeploymentHardPin && request.Origin == "" {
		return ResolvedPlan{}, resolutionError(ResolutionErrorMissingSelection, request.Purpose, spec.PolicyID, "hard-pin purpose requires the override source that fixed the model")
	}
	model := request.Decision.Model
	if model == "" {
		return ResolvedPlan{}, resolutionError(ResolutionErrorMissingSelection, request.Purpose, spec.PolicyID, "routed decision names no model")
	}
	if len(request.Bindings) == 0 {
		return ResolvedPlan{}, resolutionError(ResolutionErrorNoEligibleBinding, request.Purpose, spec.PolicyID, "routed decision has no provider binding to dispatch")
	}
	if request.Origin != "" {
		if !validOverrideSource(request.Origin) || request.Origin == OverrideSourcePolicyDefault {
			return ResolvedPlan{}, &ResolutionError{Code: ResolutionErrorInvalidOverride, Purpose: request.Purpose, PolicyID: spec.PolicyID, CatalogID: model, Source: request.Origin, detail: "routed origin is not a caller override source"}
		}
		allowed := false
		for _, source := range spec.OverridePrecedence {
			allowed = allowed || source == request.Origin
		}
		if !allowed {
			return ResolvedPlan{}, &ResolutionError{Code: ResolutionErrorOverrideNotAllowed, Purpose: request.Purpose, PolicyID: spec.PolicyID, CatalogID: model, Source: request.Origin, detail: "override source is not declared by the policy"}
		}
	}

	bindings := make([]Binding, 0, len(request.Bindings))
	for index, providerBinding := range request.Bindings {
		if providerBinding.Provider == "" {
			return ResolvedPlan{}, resolutionError(ResolutionErrorNoEligibleBinding, request.Purpose, spec.PolicyID, "routed binding names no provider")
		}
		bindingIndex, upstreamID := catalogBinding(model, providerBinding)
		binding := Binding{
			CatalogID:    model,
			Provider:     providerBinding.Provider,
			UpstreamID:   catalog.UpstreamIDFor(model, upstreamID),
			BindingIndex: bindingIndex,
			Effort:       request.Decision.Effort,
		}
		if index == 0 && request.Decision.Metadata != nil {
			binding.ArmID = request.Decision.Metadata.SelectedArmID
		}
		bindings = append(bindings, binding)
	}

	budget, err := resolveBudget(spec, request.Budget)
	if err != nil {
		return ResolvedPlan{}, err
	}

	provenance := PlanProvenance{SelectionStrategy: spec.SelectionStrategy, OverrideSource: request.Origin}
	if request.Decision.Metadata != nil {
		provenance.ArmID = request.Decision.Metadata.SelectedArmID
		provenance.RosterID = request.Decision.Metadata.SelectedRosterArmID
	}
	return ResolvedPlan{
		purpose:             spec.Purpose,
		dispatchClass:       spec.DispatchClass,
		policyID:            spec.PolicyID,
		registryRevision:    r.registry.Revision(),
		policyRevision:      spec.PolicyRevision,
		selectionStrategy:   spec.SelectionStrategy,
		selectedBinding:     bindings[0],
		alternativeBindings: bindings[1:],
		hardConstraints:     append([]Constraint(nil), spec.HardConstraints...),
		softPreferences:     append([]SoftPreference(nil), spec.SoftPreferences...),
		budget:              budget,
		provenance:          provenance,
	}, nil
}

func acceptsRoutedDecision(spec PolicySpec) bool {
	switch spec.SelectionStrategy {
	case SelectionStrategyRouter:
		return spec.DispatchClass == DispatchClassMainInference
	case SelectionStrategyDeploymentHardPin:
		return spec.DispatchClass == DispatchClassAuxiliaryInference || spec.DispatchClass == DispatchClassClientAuthoritative
	case SelectionStrategyFixedCatalog, SelectionStrategyClientAuthoritative, SelectionStrategyPassthrough, SelectionStrategyNone:
		return false
	}
	return false
}

func catalogBinding(model string, binding catalog.ProviderBinding) (int, string) {
	entry, found := catalog.ByID(model)
	if !found {
		return -1, binding.UpstreamID
	}
	for index, candidate := range entry.Providers {
		if candidate.Provider == binding.Provider && (binding.UpstreamID == "" || candidate.UpstreamID == binding.UpstreamID) {
			return index, catalog.UpstreamIDFor(candidate.UpstreamID, binding.UpstreamID)
		}
	}
	return -1, binding.UpstreamID
}
