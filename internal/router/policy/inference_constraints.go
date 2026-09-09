package policy

import (
	"fmt"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// coreConstraints are the constraints the candidate resolver applies to every
// request, so every policy that selects a target must declare them: a spec
// that omits one would under-claim what resolution actually guarantees.
var coreConstraints = []Constraint{
	ConstraintCatalogBinding,
	ConstraintContextWindow,
	ConstraintModelExclusions,
	ConstraintProviderExclusions,
}

func selectsTarget(strategy SelectionStrategy) bool {
	switch strategy {
	case SelectionStrategyRouter, SelectionStrategyFixedCatalog, SelectionStrategyDeploymentHardPin, SelectionStrategyClientAuthoritative:
		return true
	default:
		return false
	}
}

// spendCapPossible reports whether a policy's resolved budget can ever carry a
// spend cap: either the reviewed budget sets one or the budget source accepts
// caller overrides that may.
func spendCapPossible(budget BudgetSpec) bool {
	return budget.MaxSpendUSD > 0 || budget.Source == BudgetSourceRequest || budget.Source == BudgetSourceDeployment
}

func validateConstraintContract(spec PolicySpec) error {
	declared := make(map[Constraint]struct{}, len(spec.HardConstraints))
	for _, constraint := range spec.HardConstraints {
		declared[constraint] = struct{}{}
	}
	if !selectsTarget(spec.SelectionStrategy) {
		if len(spec.HardConstraints) > 0 || len(spec.SoftPreferences) > 0 {
			return fmt.Errorf("non-selecting policy %q declares constraints or preferences that no resolution verifies", spec.PolicyID)
		}
		return nil
	}
	for _, constraint := range coreConstraints {
		if _, found := declared[constraint]; !found {
			return fmt.Errorf("selecting policy %q must declare resolver-enforced constraint %q", spec.PolicyID, constraint)
		}
	}
	_, declaresSpend := declared[ConstraintSpend]
	if spec.Budget.MaxSpendUSD > 0 && !declaresSpend {
		return fmt.Errorf("policy %q sets a spend cap without declaring constraint %q", spec.PolicyID, ConstraintSpend)
	}
	if declaresSpend && !spendCapPossible(spec.Budget) {
		return fmt.Errorf("policy %q declares constraint %q but its budget can never carry a spend cap", spec.PolicyID, ConstraintSpend)
	}
	return nil
}

// verifyHardConstraints re-checks every declared constraint against the
// bindings a plan is about to emit. The candidate resolver already filtered
// on these inputs; this is the fail-closed proof that the plan's declared
// constraints describe the plan rather than annotate it.
func verifyHardConstraints(spec PolicySpec, request router.Request, budget BudgetSpec, bindings []plannedBinding) error {
	for _, binding := range bindings {
		for _, constraint := range spec.HardConstraints {
			if detail := constraintViolation(constraint, request, budget, binding); detail != "" {
				return &ResolutionError{
					Code:      ResolutionErrorConstraintViolation,
					Purpose:   spec.Purpose,
					PolicyID:  spec.PolicyID,
					CatalogID: binding.CatalogID,
					detail:    fmt.Sprintf("constraint %q: %s", constraint, detail),
				}
			}
		}
	}
	return nil
}

func constraintViolation(constraint Constraint, request router.Request, budget BudgetSpec, binding plannedBinding) string {
	switch constraint {
	case ConstraintCatalogBinding:
		if _, found := catalog.ByID(binding.CatalogID); !found {
			return "model is not in the catalog"
		}
		listed := catalog.EnumerateBindingsWithCustom(binding.CatalogID, map[string]struct{}{binding.Provider: {}}, request.CustomBindings)
		if len(listed) == 0 {
			return fmt.Sprintf("provider %q has no catalog or custom binding for the model", binding.Provider)
		}
	case ConstraintContextWindow:
		if required := requiredContextTokens(request); required > catalog.ContextWindowFor(binding.CatalogID) {
			return "request does not fit the model context window"
		}
	case ConstraintModelExclusions:
		if _, excluded := request.ExcludedModels[binding.CatalogID]; excluded {
			return "model is excluded by the request"
		}
		if len(request.AllowedModels) > 0 {
			if _, allowed := request.AllowedModels[binding.CatalogID]; !allowed {
				return "model is outside the request allowlist"
			}
		}
	case ConstraintProviderExclusions:
		if request.EnabledProviders != nil {
			if _, enabled := request.EnabledProviders[binding.Provider]; !enabled {
				return fmt.Sprintf("provider %q is not enabled for the request", binding.Provider)
			}
		}
		if len(request.GatewayProviders) > 0 {
			if _, gateway := request.GatewayProviders[binding.Provider]; !gateway {
				return fmt.Sprintf("provider %q is not one of the request gateways", binding.Provider)
			}
		}
	case ConstraintSpend:
		if exceedsSpendBudget(binding.estimatedCostUSD, budget.MaxSpendUSD) {
			return "estimated cost exceeds the spend cap"
		}
	}
	return ""
}
