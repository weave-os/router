package policy

import (
	"cmp"
	"slices"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// RecoveryMaxAttempts and RecoveryRetryWindow bound new provider attempts across recovery plans.
const (
	RecoveryMaxAttempts = 8
	RecoveryRetryWindow = 30 * time.Second
)

// ResolveRecovery independently authorizes local catalog targets. No field from
// a failed classifier response participates in resolution or preference order.
func (r *PlanResolver) ResolveRecovery(request ResolutionRequest, previousModel, configuredModel string, fitsBinding func(Binding) bool) ([]inference.ResolvedPlan, error) {
	spec, ok := r.registry.Spec(request.Purpose)
	if !ok || !spec.ServingRecovery || request.RouterRequest.ShadowMode || request.RouterRequest.ForceModel != "" || request.RouterRequest.ForceCluster != "" {
		return nil, resolutionError(ResolutionErrorUnsupportedPurpose, request.Purpose, spec.PolicyID, "local serving recovery does not authorize this operation")
	}
	if _, err := resolveBudget(spec, request.Budget); err != nil {
		return nil, err
	}
	if fitsBinding != nil {
		contextFit := router.DispatchContext{}
		if request.RouterRequest.DispatchContext != nil {
			contextFit = *request.RouterRequest.DispatchContext
		}
		contextFit.FitsBinding = func(model, provider string) bool { return fitsBinding(Binding{CatalogID: model, Provider: provider}) }
		request.RouterRequest.DispatchContext = &contextFit
	}
	resolved := r.candidateResolver.Resolve(request.RouterRequest)
	candidates := slices.Clone(resolved.Candidates)
	previousModel, previousEffort := router.SplitServedIdentity(previousModel)
	requestedTier := catalog.TierFor(request.RouterRequest.RequestedModel)
	preference := func(candidate Candidate) int {
		switch candidate.CatalogID {
		case previousModel:
			return 0
		case configuredModel:
			return 1
		}
		if requestedTier != catalog.TierUnknown && catalog.TierFor(candidate.CatalogID) == requestedTier {
			return 2
		}
		return 3
	}
	slices.SortStableFunc(candidates, func(a, b Candidate) int {
		if order := cmp.Compare(preference(a), preference(b)); order != 0 {
			return order
		}
		if order := cmp.Compare(a.EstimatedCostUSD, b.EstimatedCostUSD); order != 0 {
			return order
		}
		return cmp.Compare(a.CatalogID, b.CatalogID)
	})
	plans := make([]inference.ResolvedPlan, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := seen[candidate.CatalogID]; duplicate {
			continue
		}
		seen[candidate.CatalogID] = struct{}{}
		model, _ := catalog.ByID(candidate.CatalogID)
		if model.Tier == catalog.TierUnknown && !model.HMMTarget {
			continue
		}
		candidateRequest := request
		candidateRequest.RouterRequest.EnabledProviders = make(map[string]struct{})
		for _, binding := range resolved.bindingsByCatalogID[candidate.CatalogID] {
			if request.RouterRequest.EnabledProviders != nil {
				if _, enabled := request.RouterRequest.EnabledProviders[binding.Provider]; !enabled {
					continue
				}
			}
			if fitsBinding == nil || fitsBinding(binding.Binding) {
				candidateRequest.RouterRequest.EnabledProviders[binding.Provider] = struct{}{}
			}
		}
		if len(candidateRequest.RouterRequest.EnabledProviders) == 0 {
			continue
		}
		if len(candidateRequest.RouterRequest.GatewayProviders) > 0 {
			candidateRequest.RouterRequest.GatewayProviders = candidateRequest.RouterRequest.EnabledProviders
		}
		candidateRequest.Selection = CandidateSelection{RosterID: candidate.RosterID}
		if candidate.CatalogID == previousModel && previousEffort != "" && router.IsValidEffort(previousEffort) {
			candidateRequest.Overrides = []TargetOverride{{Source: OverrideSourceSession, CatalogID: candidate.CatalogID, Effort: previousEffort}}
		}
		plan, err := r.Resolve(candidateRequest)
		if err != nil {
			continue
		}
		plan.selectionStrategy = inference.SelectionStrategyRecovery
		plan.provenance = PlanProvenance{SelectionStrategy: inference.SelectionStrategyRecovery}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return nil, resolutionError(ResolutionErrorNoEligibleBinding, request.Purpose, spec.PolicyID, "no authorized local recovery target satisfies this request")
	}
	// Keep continuity/tier preferences first; prefer an independent provider for rescue.
	if len(plans) > 1 {
		primaryProvider := plans[0].SelectedTarget().Provider
		slices.SortStableFunc(plans[1:], func(a, b inference.ResolvedPlan) int {
			aUsesPrimaryProvider := a.SelectedTarget().Provider == primaryProvider
			bUsesPrimaryProvider := b.SelectedTarget().Provider == primaryProvider
			if aUsesPrimaryProvider == bUsesPrimaryProvider {
				return 0
			}
			if aUsesPrimaryProvider {
				return 1
			}
			return -1
		})
	}
	return plans, nil
}
