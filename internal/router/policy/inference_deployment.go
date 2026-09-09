package policy

import (
	"fmt"
	"slices"

	"weave-os/router/internal/router/catalog"
)

// PurposeTargetOverride associates a typed target override with one registered
// purpose for boot-time validation.
type PurposeTargetOverride struct {
	Purpose Purpose
	Target  TargetOverride
}

// DeploymentPolicyConfig contains the non-secret deployment facts required to
// prove that boot-resolved policies can produce a dispatchable catalog binding.
type DeploymentPolicyConfig struct {
	AvailableProviders map[string]struct{}
	TargetOverrides    []PurposeTargetOverride
	// RoutableModels is the universe router-selected purposes actually serve
	// (generic routing targets plus any strategy-only rows such as HMM
	// targets). Nil means the generic catalog routing set.
	RoutableModels map[string]struct{}
}

func (c DeploymentPolicyConfig) routableModels() map[string]struct{} {
	if c.RoutableModels != nil {
		return c.RoutableModels
	}
	return catalog.RoutingTargetSet(c.AvailableProviders)
}

// ValidateDeployment rejects policy/configuration combinations that cannot
// produce a target in this deployment. Request-scoped credentials, tenant
// restrictions, and request requirements remain request-time validation.
func (r Registry) ValidateDeployment(config DeploymentPolicyConfig) error {
	overrides := make(map[Purpose]TargetOverride, len(config.TargetOverrides))
	for _, purposeOverride := range config.TargetOverrides {
		spec, found := r.Spec(purposeOverride.Purpose)
		if !found {
			return fmt.Errorf("deployment inference policy override names unknown purpose %q", purposeOverride.Purpose)
		}
		if purposeOverride.Target.Source != OverrideSourceDeployment {
			return fmt.Errorf("deployment inference policy override for %q must use source %q", purposeOverride.Purpose, OverrideSourceDeployment)
		}
		if !slices.Contains(spec.OverridePrecedence, OverrideSourceDeployment) {
			return fmt.Errorf("deployment inference policy override is not allowed for policy %q", spec.PolicyID)
		}
		if err := validateTargetOverride(spec, purposeOverride.Target); err != nil {
			return err
		}
		if _, duplicate := overrides[purposeOverride.Purpose]; duplicate {
			return fmt.Errorf("deployment inference policy has more than one target override for purpose %q", purposeOverride.Purpose)
		}
		overrides[purposeOverride.Purpose] = purposeOverride.Target
	}

	routingTargets := config.routableModels()
	for _, spec := range r.Specs() {
		override, hasOverride := overrides[spec.Purpose]
		if hasOverride {
			// Fixed-catalog policies own their membership (checked below), so a
			// reviewed untiered row such as a large-window summarizer is valid there.
			if _, routable := routingTargets[override.CatalogID]; !routable && spec.SelectionStrategy != SelectionStrategyFixedCatalog {
				return fmt.Errorf("deployment inference policy %q target %q is not a routable catalog model in this deployment", spec.PolicyID, override.CatalogID)
			}
			if !deploymentCanResolve([]string{override.CatalogID}, override.Provider, config.AvailableProviders) {
				return fmt.Errorf("deployment inference policy %q target %q has no available catalog binding", spec.PolicyID, override.CatalogID)
			}
		}
		switch spec.SelectionStrategy {
		case SelectionStrategyRouter:
			if len(routingTargets) == 0 {
				return fmt.Errorf("deployment inference policy %q has no routable catalog binding", spec.PolicyID)
			}
		case SelectionStrategyFixedCatalog:
			models := spec.FixedCatalogModels
			if hasOverride {
				if !slices.Contains(models, override.CatalogID) {
					return fmt.Errorf("deployment inference policy override %q is outside fixed policy %q", override.CatalogID, spec.PolicyID)
				}
				models = []string{override.CatalogID}
			}
			if !deploymentCanResolve(models, override.Provider, config.AvailableProviders) {
				return fmt.Errorf("deployment inference policy %q has no available fixed catalog binding", spec.PolicyID)
			}
		case SelectionStrategyDeploymentHardPin:
			if !hasOverride {
				return fmt.Errorf("deployment inference policy %q requires a typed deployment target", spec.PolicyID)
			}
		}
	}
	return nil
}

func deploymentCanResolve(models []string, requiredProvider string, availableProviders map[string]struct{}) bool {
	for _, catalogID := range models {
		for _, binding := range catalog.EnumerateBindings(catalogID, availableProviders) {
			if requiredProvider == "" || binding.Provider == requiredProvider {
				return true
			}
		}
	}
	return false
}
