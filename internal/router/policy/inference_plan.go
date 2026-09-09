package policy

import (
	"fmt"
	"slices"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// ResolutionErrorCode identifies a fail-closed policy resolution failure.
type ResolutionErrorCode string

const (
	ResolutionErrorUnknownPurpose     ResolutionErrorCode = "unknown_purpose"
	ResolutionErrorUnsupportedPurpose ResolutionErrorCode = "unsupported_purpose"
	ResolutionErrorInvalidOverride    ResolutionErrorCode = "invalid_override"
	ResolutionErrorOverrideNotAllowed ResolutionErrorCode = "override_not_allowed"
	ResolutionErrorDuplicateOverride  ResolutionErrorCode = "duplicate_override"
	ResolutionErrorMissingSelection   ResolutionErrorCode = "missing_selection"
	ResolutionErrorUnknownSelection   ResolutionErrorCode = "unknown_selection"
	ResolutionErrorNoEligibleBinding  ResolutionErrorCode = "no_eligible_binding"
	ResolutionErrorBudgetViolation    ResolutionErrorCode = "budget_violation"
)

// ResolutionError carries a stable reason code without exposing request
// content, credentials, or an unbounded candidate explanation.
type ResolutionError struct {
	Code      ResolutionErrorCode
	Purpose   Purpose
	PolicyID  PolicyID
	CatalogID string
	Source    OverrideSource
	detail    string
}

func (e *ResolutionError) Error() string {
	identity := fmt.Sprintf("purpose %q", e.Purpose)
	if e.PolicyID != "" {
		identity += fmt.Sprintf(" policy %q", e.PolicyID)
	}
	if e.CatalogID != "" {
		identity += fmt.Sprintf(" catalog model %q", e.CatalogID)
	}
	if e.Source != "" {
		identity += fmt.Sprintf(" source %q", e.Source)
	}
	return fmt.Sprintf("inference policy resolution %s for %s: %s", e.Code, identity, e.detail)
}

// CandidateSelection identifies the candidate selected by an existing router
// or policy sidecar. ArmID is authoritative when both fields are present.
type CandidateSelection struct {
	ArmID    string
	RosterID string
}

// ResolutionRequest contains the content-free inputs needed to turn a policy
// entry and existing catalog candidates into an immutable execution plan.
type ResolutionRequest struct {
	Purpose       Purpose
	RouterRequest router.Request
	Overrides     []TargetOverride
	Selection     CandidateSelection
	Budget        *BudgetOverride
}

// ResolvedPlan is immutable execution authorization. Only PlanResolver can
// construct a populated plan; slice accessors return detached copies.
type ResolvedPlan struct {
	purpose             Purpose
	dispatchClass       DispatchClass
	policyID            PolicyID
	registryRevision    string
	policyRevision      PolicyRevision
	selectionStrategy   SelectionStrategy
	selectedBinding     Binding
	alternativeBindings []Binding
	hardConstraints     []Constraint
	softPreferences     []SoftPreference
	budget              BudgetSpec
	provenance          PlanProvenance
}

func (p ResolvedPlan) Purpose() Purpose { return p.purpose }

func (p ResolvedPlan) DispatchClass() DispatchClass { return p.dispatchClass }

func (p ResolvedPlan) PolicyID() PolicyID { return p.policyID }

func (p ResolvedPlan) RegistryRevision() string { return p.registryRevision }

func (p ResolvedPlan) PolicyRevision() PolicyRevision { return p.policyRevision }

func (p ResolvedPlan) SelectionStrategy() SelectionStrategy { return p.selectionStrategy }

func (p ResolvedPlan) SelectedBinding() Binding { return p.selectedBinding }

// SelectedTarget returns the package-neutral execution target view.
func (p ResolvedPlan) SelectedTarget() inference.Target {
	return targetFromBinding(p.selectedBinding)
}

func (p ResolvedPlan) AlternativeBindings() []Binding {
	return append([]Binding(nil), p.alternativeBindings...)
}

// AlternativeTargets returns package-neutral fallback target views.
func (p ResolvedPlan) AlternativeTargets() []inference.Target {
	targets := make([]inference.Target, len(p.alternativeBindings))
	for index, binding := range p.alternativeBindings {
		targets[index] = targetFromBinding(binding)
	}
	return targets
}

func (p ResolvedPlan) HardConstraints() []Constraint {
	return append([]Constraint(nil), p.hardConstraints...)
}

func (p ResolvedPlan) SoftPreferences() []SoftPreference {
	return append([]SoftPreference(nil), p.softPreferences...)
}

func (p ResolvedPlan) Budget() BudgetSpec { return p.budget }

func (p ResolvedPlan) Provenance() PlanProvenance { return p.provenance }

// PlanResolver applies one registry to the existing catalog candidate
// resolver. It does not score models or duplicate provider eligibility logic.
type PlanResolver struct {
	registry          Registry
	candidateResolver *Resolver
}

func NewPlanResolver(registry Registry, candidateResolver *Resolver) (*PlanResolver, error) {
	if registry.Revision() == "" {
		return nil, fmt.Errorf("inference plan resolver requires a validated registry")
	}
	if candidateResolver == nil {
		return nil, fmt.Errorf("inference plan resolver requires a candidate resolver")
	}
	return &PlanResolver{registry: registry, candidateResolver: candidateResolver}, nil
}

// Resolve validates typed overrides and budgets, resolves catalog candidates,
// and returns an immutable plan. Invalid explicit inputs never fall through to
// another target.
func (r *PlanResolver) Resolve(request ResolutionRequest) (ResolvedPlan, error) {
	spec, found := r.registry.Spec(request.Purpose)
	if !found {
		return ResolvedPlan{}, resolutionError(ResolutionErrorUnknownPurpose, request.Purpose, PolicyID(""), "purpose is not registered")
	}
	if spec.SelectionStrategy == SelectionStrategyNone || spec.SelectionStrategy == SelectionStrategyPassthrough {
		return ResolvedPlan{}, resolutionError(ResolutionErrorUnsupportedPurpose, request.Purpose, spec.PolicyID, "purpose does not authorize automatic provider inference")
	}

	budget, err := resolveBudget(spec, request.Budget)
	if err != nil {
		return ResolvedPlan{}, err
	}
	override, err := selectTargetOverride(spec, request.Overrides)
	if err != nil {
		return ResolvedPlan{}, err
	}

	routerRequest := request.RouterRequest
	if routerRequest.ForceModel != "" && (override == nil || override.Source != OverrideSourceRequest || override.CatalogID != routerRequest.ForceModel) {
		return ResolvedPlan{}, &ResolutionError{
			Code:      ResolutionErrorInvalidOverride,
			Purpose:   request.Purpose,
			PolicyID:  spec.PolicyID,
			CatalogID: routerRequest.ForceModel,
			Source:    OverrideSourceRequest,
			detail:    "force-model input must be represented by a matching typed request override",
		}
	}

	restrictedModels, err := resolutionModels(spec, override)
	if err != nil {
		return ResolvedPlan{}, err
	}
	if len(restrictedModels) > 0 {
		var allowed bool
		routerRequest, allowed = restrictModels(routerRequest, restrictedModels)
		if !allowed {
			return ResolvedPlan{}, resolutionError(ResolutionErrorNoEligibleBinding, request.Purpose, spec.PolicyID, "policy targets do not intersect the request model allowlist")
		}
	}
	if override != nil && override.Provider != "" {
		routerRequest.EnabledProviders = restrictProviders(routerRequest.EnabledProviders, override.Provider)
	}

	resolved := r.candidateResolver.Resolve(routerRequest)
	if len(resolved.Candidates) == 0 {
		return ResolvedPlan{}, resolutionError(ResolutionErrorNoEligibleBinding, request.Purpose, spec.PolicyID, "no catalog binding satisfies the request constraints")
	}

	selected, provenance, err := selectBinding(spec, request, override, resolved, budget)
	if err != nil {
		return ResolvedPlan{}, err
	}
	alternatives := fallbackBindings(spec, selected, resolved, budget.MaxSpendUSD)
	return ResolvedPlan{
		purpose:             spec.Purpose,
		dispatchClass:       spec.DispatchClass,
		policyID:            spec.PolicyID,
		registryRevision:    r.registry.Revision(),
		policyRevision:      spec.PolicyRevision,
		selectionStrategy:   spec.SelectionStrategy,
		selectedBinding:     selected.Binding,
		alternativeBindings: alternatives,
		hardConstraints:     append([]Constraint(nil), spec.HardConstraints...),
		softPreferences:     append([]SoftPreference(nil), spec.SoftPreferences...),
		budget:              budget,
		provenance:          provenance,
	}, nil
}

type plannedBinding struct {
	Binding
	estimatedCostUSD float64
}

func selectBinding(spec PolicySpec, request ResolutionRequest, override *TargetOverride, resolved ResolvedCandidates, budget BudgetSpec) (plannedBinding, PlanProvenance, error) {
	if override != nil {
		binding, found := overrideBinding(resolved, *override)
		if !found {
			return plannedBinding{}, PlanProvenance{}, &ResolutionError{
				Code:      ResolutionErrorNoEligibleBinding,
				Purpose:   request.Purpose,
				PolicyID:  spec.PolicyID,
				CatalogID: override.CatalogID,
				Source:    override.Source,
				detail:    "explicit target is not eligible for this request",
			}
		}
		binding.Effort = override.Effort
		if exceedsSpendBudget(binding.estimatedCostUSD, budget.MaxSpendUSD) {
			return plannedBinding{}, PlanProvenance{}, budgetResolutionError(spec, override.CatalogID, "explicit target exceeds the spend budget")
		}
		return binding, PlanProvenance{SelectionStrategy: spec.SelectionStrategy, OverrideSource: override.Source}, nil
	}

	switch spec.SelectionStrategy {
	case SelectionStrategyFixedCatalog:
		anyFixedBinding := false
		for _, catalogID := range spec.FixedCatalogModels {
			bindings := resolved.bindingsByCatalogID[catalogID]
			for _, binding := range bindings {
				anyFixedBinding = true
				if exceedsSpendBudget(binding.estimatedCostUSD, budget.MaxSpendUSD) {
					continue
				}
				return plannedBinding{Binding: binding.Binding, estimatedCostUSD: binding.estimatedCostUSD}, PlanProvenance{
					SelectionStrategy: spec.SelectionStrategy,
					OverrideSource:    OverrideSourcePolicyDefault,
				}, nil
			}
		}
		if !anyFixedBinding {
			return plannedBinding{}, PlanProvenance{}, resolutionError(ResolutionErrorNoEligibleBinding, request.Purpose, spec.PolicyID, "no fixed policy target has an eligible binding for this request")
		}
		return plannedBinding{}, PlanProvenance{}, budgetResolutionError(spec, "", "no fixed policy target fits the spend budget")
	case SelectionStrategyRouter:
		if request.Selection.ArmID == "" && request.Selection.RosterID == "" {
			return plannedBinding{}, PlanProvenance{}, resolutionError(ResolutionErrorMissingSelection, request.Purpose, spec.PolicyID, "router-selected policy requires a candidate selection")
		}
		selectionRosterID := request.Selection.RosterID
		if request.Selection.ArmID != "" {
			selectionRosterID = ""
		}
		binding, found := resolved.BindingForSelection(request.Selection.ArmID, selectionRosterID)
		if !found {
			return plannedBinding{}, PlanProvenance{}, resolutionError(ResolutionErrorUnknownSelection, request.Purpose, spec.PolicyID, "selected arm was not offered by the candidate resolver")
		}
		planned, found := plannedBindingFor(resolved, binding)
		if !found {
			return plannedBinding{}, PlanProvenance{}, resolutionError(ResolutionErrorUnknownSelection, request.Purpose, spec.PolicyID, "selected binding has no resolved catalog identity")
		}
		planned.Effort = binding.Effort
		if exceedsSpendBudget(planned.estimatedCostUSD, budget.MaxSpendUSD) {
			return plannedBinding{}, PlanProvenance{}, budgetResolutionError(spec, binding.CatalogID, "router-selected target exceeds the spend budget")
		}
		return planned, PlanProvenance{
			SelectionStrategy: spec.SelectionStrategy,
			ArmID:             request.Selection.ArmID,
			RosterID:          request.Selection.RosterID,
		}, nil
	case SelectionStrategyDeploymentHardPin, SelectionStrategyClientAuthoritative:
		return plannedBinding{}, PlanProvenance{}, resolutionError(ResolutionErrorMissingSelection, request.Purpose, spec.PolicyID, "selection strategy requires a typed target override")
	default:
		return plannedBinding{}, PlanProvenance{}, resolutionError(ResolutionErrorUnsupportedPurpose, request.Purpose, spec.PolicyID, "selection strategy cannot produce an inference plan")
	}
}

func selectTargetOverride(spec PolicySpec, overrides []TargetOverride) (*TargetOverride, error) {
	bySource := make(map[OverrideSource]TargetOverride, len(overrides))
	allowedSources := make(map[OverrideSource]struct{}, len(spec.OverridePrecedence))
	for _, source := range spec.OverridePrecedence {
		allowedSources[source] = struct{}{}
	}
	for _, override := range overrides {
		if err := validateTargetOverride(spec, override); err != nil {
			return nil, err
		}
		if _, allowed := allowedSources[override.Source]; !allowed {
			return nil, &ResolutionError{
				Code:      ResolutionErrorOverrideNotAllowed,
				Purpose:   spec.Purpose,
				PolicyID:  spec.PolicyID,
				CatalogID: override.CatalogID,
				Source:    override.Source,
				detail:    "override source is not declared by the policy",
			}
		}
		if _, duplicate := bySource[override.Source]; duplicate {
			return nil, &ResolutionError{
				Code:     ResolutionErrorDuplicateOverride,
				Purpose:  spec.Purpose,
				PolicyID: spec.PolicyID,
				Source:   override.Source,
				detail:   "more than one target override was supplied for the same source",
			}
		}
		bySource[override.Source] = override
	}
	for _, source := range spec.OverridePrecedence {
		if override, found := bySource[source]; found {
			return &override, nil
		}
	}
	return nil, nil
}

func validateTargetOverride(spec PolicySpec, override TargetOverride) error {
	if !validOverrideSource(override.Source) || override.CatalogID == "" {
		return &ResolutionError{
			Code:      ResolutionErrorInvalidOverride,
			Purpose:   spec.Purpose,
			PolicyID:  spec.PolicyID,
			CatalogID: override.CatalogID,
			Source:    override.Source,
			detail:    "override requires a valid source and catalog model",
		}
	}
	if override.Source == OverrideSourcePolicyDefault {
		return &ResolutionError{
			Code:      ResolutionErrorInvalidOverride,
			Purpose:   spec.Purpose,
			PolicyID:  spec.PolicyID,
			CatalogID: override.CatalogID,
			Source:    override.Source,
			detail:    "policy defaults must come from the reviewed registry, not caller input",
		}
	}
	if _, found := catalog.ByID(override.CatalogID); !found {
		return &ResolutionError{
			Code:      ResolutionErrorInvalidOverride,
			Purpose:   spec.Purpose,
			PolicyID:  spec.PolicyID,
			CatalogID: override.CatalogID,
			Source:    override.Source,
			detail:    "override names an unknown catalog model",
		}
	}
	if override.Effort != "" && !router.IsValidEffort(override.Effort) {
		return &ResolutionError{
			Code:      ResolutionErrorInvalidOverride,
			Purpose:   spec.Purpose,
			PolicyID:  spec.PolicyID,
			CatalogID: override.CatalogID,
			Source:    override.Source,
			detail:    "override contains an invalid reasoning effort",
		}
	}
	return nil
}

func resolutionModels(spec PolicySpec, override *TargetOverride) ([]string, error) {
	fallbackModels := []string(nil)
	if spec.Fallback.Kind == FallbackKindPlanAlternatives {
		fallbackModels = spec.Fallback.Alternatives
	}
	if override != nil {
		if spec.SelectionStrategy == SelectionStrategyFixedCatalog && !slices.Contains(spec.FixedCatalogModels, override.CatalogID) {
			return nil, &ResolutionError{
				Code:      ResolutionErrorInvalidOverride,
				Purpose:   spec.Purpose,
				PolicyID:  spec.PolicyID,
				CatalogID: override.CatalogID,
				Source:    override.Source,
				detail:    "fixed policy override is outside the reviewed catalog model set",
			}
		}
		return append([]string{override.CatalogID}, fallbackModels...), nil
	}
	switch spec.SelectionStrategy {
	case SelectionStrategyFixedCatalog:
		return append(append([]string(nil), spec.FixedCatalogModels...), fallbackModels...), nil
	case SelectionStrategyDeploymentHardPin, SelectionStrategyClientAuthoritative:
		return nil, resolutionError(ResolutionErrorMissingSelection, spec.Purpose, spec.PolicyID, "selection strategy requires a typed target override")
	default:
		return nil, nil
	}
}

func restrictModels(request router.Request, models []string) (router.Request, bool) {
	allowed := make(map[string]struct{}, len(models))
	for _, catalogID := range models {
		if len(request.AllowedModels) > 0 {
			if _, requestAllows := request.AllowedModels[catalogID]; !requestAllows {
				continue
			}
		}
		allowed[catalogID] = struct{}{}
	}
	if len(allowed) == 0 {
		return request, false
	}
	request.AllowedModels = allowed
	return request, true
}

func restrictProviders(enabled map[string]struct{}, provider string) map[string]struct{} {
	if enabled == nil {
		return map[string]struct{}{provider: {}}
	}
	if _, found := enabled[provider]; found {
		return map[string]struct{}{provider: {}}
	}
	return map[string]struct{}{}
}

func overrideBinding(resolved ResolvedCandidates, override TargetOverride) (plannedBinding, bool) {
	for _, binding := range resolved.bindingsByCatalogID[override.CatalogID] {
		if override.Provider != "" && binding.Provider != override.Provider {
			continue
		}
		return plannedBinding{Binding: binding.Binding, estimatedCostUSD: binding.estimatedCostUSD}, true
	}
	return plannedBinding{}, false
}

func plannedBindingFor(resolved ResolvedCandidates, selected Binding) (plannedBinding, bool) {
	for _, binding := range resolved.bindingsByCatalogID[selected.CatalogID] {
		if binding.Provider == selected.Provider && binding.BindingIndex == selected.BindingIndex {
			return plannedBinding{Binding: binding.Binding, estimatedCostUSD: binding.estimatedCostUSD}, true
		}
	}
	return plannedBinding{}, false
}

func fallbackBindings(spec PolicySpec, selected plannedBinding, resolved ResolvedCandidates, maxSpendUSD float64) []Binding {
	var candidates []plannedBinding
	switch spec.Fallback.Kind {
	case FallbackKindBinding:
		for _, binding := range resolved.bindingsByCatalogID[selected.CatalogID] {
			candidates = append(candidates, plannedBinding{Binding: binding.Binding, estimatedCostUSD: binding.estimatedCostUSD})
		}
	case FallbackKindPlanAlternatives:
		for _, catalogID := range spec.Fallback.Alternatives {
			for _, binding := range resolved.bindingsByCatalogID[catalogID] {
				candidates = append(candidates, plannedBinding{Binding: binding.Binding, estimatedCostUSD: binding.estimatedCostUSD})
			}
		}
	}

	alternatives := make([]Binding, 0, len(candidates))
	for _, candidate := range candidates {
		if sameBinding(selected.Binding, candidate.Binding) || exceedsSpendBudget(candidate.estimatedCostUSD, maxSpendUSD) {
			continue
		}
		candidate.Effort = selected.Effort
		alternatives = append(alternatives, candidate.Binding)
	}
	return alternatives
}

func sameBinding(left, right Binding) bool {
	return left.CatalogID == right.CatalogID && left.Provider == right.Provider && left.BindingIndex == right.BindingIndex
}

func targetFromBinding(binding Binding) inference.Target {
	return inference.Target{
		ArmID:                        binding.ArmID,
		CatalogID:                    binding.CatalogID,
		Provider:                     binding.Provider,
		UpstreamID:                   binding.UpstreamID,
		BindingIndex:                 binding.BindingIndex,
		Endpoint:                     binding.Endpoint,
		ModelRevision:                binding.ModelRevision,
		ReasoningConfigurationSHA256: binding.ReasoningConfigurationSHA256,
		ToolConfigurationSHA256:      binding.ToolConfigurationSHA256,
		Effort:                       binding.Effort,
	}
}

var _ inference.ResolvedPlan = ResolvedPlan{}

func resolveBudget(spec PolicySpec, override *BudgetOverride) (BudgetSpec, error) {
	if override == nil {
		return spec.Budget, nil
	}
	if override.Source == BudgetSourcePolicy || override.Source == BudgetSourceLocal {
		return BudgetSpec{}, budgetResolutionError(spec, "", "policy and local budgets must come from the reviewed registry")
	}
	if !validBudgetSource(override.Source) || override.Source != spec.Budget.Source {
		return BudgetSpec{}, budgetResolutionError(spec, "", "budget override source does not match the policy budget source")
	}
	if override.MaxAttempts < 0 || override.TimeoutMillis < 0 || override.MaxOutputTokens < 0 || override.MaxSpendUSD < 0 {
		return BudgetSpec{}, budgetResolutionError(spec, "", "budget override contains a negative limit")
	}
	if exceedsIntLimit(override.MaxAttempts, spec.Budget.MaxAttempts) ||
		exceedsInt64Limit(override.TimeoutMillis, spec.Budget.TimeoutMillis) ||
		exceedsIntLimit(override.MaxOutputTokens, spec.Budget.MaxOutputTokens) ||
		exceedsFloatLimit(override.MaxSpendUSD, spec.Budget.MaxSpendUSD) {
		return BudgetSpec{}, budgetResolutionError(spec, "", "budget override exceeds the reviewed policy envelope")
	}
	resolved := spec.Budget
	if override.MaxAttempts > 0 {
		resolved.MaxAttempts = override.MaxAttempts
	}
	if override.TimeoutMillis > 0 {
		resolved.TimeoutMillis = override.TimeoutMillis
	}
	if override.MaxOutputTokens > 0 {
		resolved.MaxOutputTokens = override.MaxOutputTokens
	}
	if override.MaxSpendUSD > 0 {
		resolved.MaxSpendUSD = override.MaxSpendUSD
	}
	return resolved, nil
}

func exceedsIntLimit(value, maximum int) bool {
	return maximum > 0 && value > maximum
}

func exceedsInt64Limit(value, maximum int64) bool {
	return maximum > 0 && value > maximum
}

func exceedsFloatLimit(value, maximum float64) bool {
	return maximum > 0 && value > maximum
}

func exceedsSpendBudget(estimatedCostUSD, maxSpendUSD float64) bool {
	return maxSpendUSD > 0 && estimatedCostUSD > maxSpendUSD
}

func resolutionError(code ResolutionErrorCode, purpose Purpose, policyID PolicyID, detail string) *ResolutionError {
	return &ResolutionError{Code: code, Purpose: purpose, PolicyID: policyID, detail: detail}
}

func budgetResolutionError(spec PolicySpec, catalogID, detail string) *ResolutionError {
	return &ResolutionError{
		Code:      ResolutionErrorBudgetViolation,
		Purpose:   spec.Purpose,
		PolicyID:  spec.PolicyID,
		CatalogID: catalogID,
		detail:    detail,
	}
}
