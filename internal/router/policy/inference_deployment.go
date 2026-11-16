package policy

import (
	"fmt"
	"slices"

	"weave-os/router/internal/router/catalog"
)

type PurposeTargetOverride struct {
	Purpose Purpose
	Target  TargetOverride
}

type DeploymentPolicyConfig struct {
	AvailableProviders      map[string]struct{}
	EnabledOptionalPurposes map[Purpose]bool
	TargetOverrides         []PurposeTargetOverride
	RoutableModels          map[string]struct{}
}

func (c DeploymentPolicyConfig) routableModels() map[string]struct{} {
	if c.RoutableModels != nil {
		return c.RoutableModels
	}
	return catalog.RoutingTargetSet(c.AvailableProviders)
}

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
		if spec.Optional && !config.EnabledOptionalPurposes[spec.Purpose] {
			continue
		}
		override, hasOverride := overrides[spec.Purpose]
		if hasOverride {
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
			requiredProvider := override.Provider
			if spec.FixedProvider != "" {
				requiredProvider = spec.FixedProvider
			}
			if !deploymentCanResolve(models, requiredProvider, config.AvailableProviders) {
				return fmt.Errorf("deployment inference policy %q has no available fixed catalog binding", spec.PolicyID)
			}
		case SelectionStrategyDeploymentHardPin:
			if !hasOverride {
				return fmt.Errorf("deployment inference policy %q requires a typed deployment target", spec.PolicyID)
			}
		case SelectionStrategyClientAuthoritative, SelectionStrategyPassthrough, SelectionStrategyNone:
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
