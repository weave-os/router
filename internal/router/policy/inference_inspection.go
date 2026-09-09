package policy

import (
	"errors"
	"slices"
	"sort"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// maxProjectedAlternatives bounds the fallback list an inspection response
// carries; the total is reported separately so nothing is silently dropped.
const maxProjectedAlternatives = 8

// TargetProjection is the content-free view of one authorized binding.
type TargetProjection struct {
	CatalogID    string `json:"catalog_id"`
	Provider     string `json:"provider"`
	UpstreamID   string `json:"upstream_id,omitempty"`
	BindingIndex int    `json:"binding_index"`
	Effort       string `json:"effort,omitempty"`
}

// DeploymentTargetProjection reports the boot-validated override for one purpose.
type DeploymentTargetProjection struct {
	CatalogID string `json:"catalog_id"`
	Provider  string `json:"provider,omitempty"`
	Effort    string `json:"effort,omitempty"`
}

// DeploymentPolicyProjection is one purpose as this deployment can serve it.
type DeploymentPolicyProjection struct {
	Purpose           Purpose                     `json:"purpose"`
	PolicyID          PolicyID                    `json:"policy_id"`
	PolicyRevision    PolicyRevision              `json:"policy_revision"`
	SelectionStrategy SelectionStrategy           `json:"selection_strategy"`
	MigrationStatus   MigrationStatus             `json:"migration_status"`
	DeploymentTarget  *DeploymentTargetProjection `json:"deployment_target,omitempty"`
	// CandidateBindings lists the deployment-available bindings for fixed and
	// hard-pinned policies. Router-selected policies report only a count.
	CandidateBindings []TargetProjection `json:"candidate_bindings,omitempty"`
	RoutableModels    int                `json:"routable_models,omitempty"`
}

// DeploymentProjection is the deployment-scoped inspection view: which
// providers are configured and how each purpose resolves against them.
type DeploymentProjection struct {
	SchemaVersion      string                       `json:"schema_version"`
	RegistryRevision   string                       `json:"registry_revision"`
	AvailableProviders []string                     `json:"available_providers"`
	RoutableModels     int                          `json:"routable_models"`
	Policies           []DeploymentPolicyProjection `json:"policies"`
}

// DeploymentProjection renders the registry against validated deployment facts.
// It never reads credentials; AvailableProviders carries names only.
func (r Registry) DeploymentProjection(config DeploymentPolicyConfig) DeploymentProjection {
	providers := make([]string, 0, len(config.AvailableProviders))
	for provider := range config.AvailableProviders {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	routable := config.routableModels()

	overrides := make(map[Purpose]TargetOverride, len(config.TargetOverrides))
	for _, override := range config.TargetOverrides {
		overrides[override.Purpose] = override.Target
	}

	policies := make([]DeploymentPolicyProjection, 0, len(r.specs))
	for _, spec := range r.specs {
		projection := DeploymentPolicyProjection{
			Purpose:           spec.Purpose,
			PolicyID:          spec.PolicyID,
			PolicyRevision:    spec.PolicyRevision,
			SelectionStrategy: spec.SelectionStrategy,
			MigrationStatus:   spec.MigrationStatus,
		}
		models := spec.FixedCatalogModels
		requiredProvider := ""
		if override, found := overrides[spec.Purpose]; found {
			projection.DeploymentTarget = &DeploymentTargetProjection{CatalogID: override.CatalogID, Provider: override.Provider, Effort: override.Effort}
			models = []string{override.CatalogID}
			requiredProvider = override.Provider
		}
		switch spec.SelectionStrategy {
		case SelectionStrategyRouter:
			projection.RoutableModels = len(routable)
		case SelectionStrategyFixedCatalog, SelectionStrategyDeploymentHardPin:
			for _, catalogID := range models {
				for _, binding := range catalog.EnumerateBindings(catalogID, config.AvailableProviders) {
					if requiredProvider != "" && binding.Provider != requiredProvider {
						continue
					}
					projection.CandidateBindings = append(projection.CandidateBindings, TargetProjection{
						CatalogID:    catalogID,
						Provider:     binding.Provider,
						UpstreamID:   catalog.UpstreamIDFor(catalogID, binding.UpstreamID),
						BindingIndex: binding.Index,
					})
				}
			}
		}
		policies = append(policies, projection)
	}
	return DeploymentProjection{
		SchemaVersion:      InferenceRegistrySchemaVersion,
		RegistryRevision:   r.Revision(),
		AvailableProviders: providers,
		RoutableModels:     len(routable),
		Policies:           policies,
	}
}

// PlanProvenanceProjection is the safe subset of plan provenance.
type PlanProvenanceProjection struct {
	SelectionStrategy SelectionStrategy `json:"selection_strategy"`
	OverrideSource    OverrideSource    `json:"override_source,omitempty"`
	ArmID             string            `json:"arm_id,omitempty"`
	RosterID          string            `json:"roster_id,omitempty"`
}

// PlanProjection is a resolved plan rendered for inspection: identity,
// authorization, and a bounded fallback list.
type PlanProjection struct {
	Purpose           Purpose                  `json:"purpose"`
	DispatchClass     DispatchClass            `json:"dispatch_class"`
	PolicyID          PolicyID                 `json:"policy_id"`
	RegistryRevision  string                   `json:"registry_revision"`
	PolicyRevision    PolicyRevision           `json:"policy_revision"`
	SelectionStrategy SelectionStrategy        `json:"selection_strategy"`
	SelectedTarget    TargetProjection         `json:"selected_target"`
	Alternatives      []TargetProjection       `json:"alternatives"`
	AlternativeCount  int                      `json:"alternative_count"`
	HardConstraints   []Constraint             `json:"hard_constraints"`
	SoftPreferences   []SoftPreference         `json:"soft_preferences"`
	Budget            BudgetSpec               `json:"budget"`
	Provenance        PlanProvenanceProjection `json:"provenance"`
}

// Projection renders the plan without candidate scores or request content.
func (p ResolvedPlan) Projection() PlanProjection {
	alternatives := p.AlternativeTargets()
	projected := make([]TargetProjection, 0, min(len(alternatives), maxProjectedAlternatives))
	for _, target := range alternatives[:min(len(alternatives), maxProjectedAlternatives)] {
		projected = append(projected, projectTarget(target))
	}
	provenance := p.Provenance()
	return PlanProjection{
		Purpose:           p.Purpose(),
		DispatchClass:     p.DispatchClass(),
		PolicyID:          p.PolicyID(),
		RegistryRevision:  p.RegistryRevision(),
		PolicyRevision:    p.PolicyRevision(),
		SelectionStrategy: p.SelectionStrategy(),
		SelectedTarget:    projectTarget(p.SelectedTarget()),
		Alternatives:      projected,
		AlternativeCount:  len(alternatives),
		HardConstraints:   p.HardConstraints(),
		SoftPreferences:   p.SoftPreferences(),
		Budget:            p.Budget(),
		Provenance: PlanProvenanceProjection{
			SelectionStrategy: provenance.SelectionStrategy,
			OverrideSource:    provenance.OverrideSource,
			ArmID:             provenance.ArmID,
			RosterID:          provenance.RosterID,
		},
	}
}

func projectTarget(target inference.Target) TargetProjection {
	return TargetProjection{
		CatalogID:    target.CatalogID,
		Provider:     target.Provider,
		UpstreamID:   target.UpstreamID,
		BindingIndex: target.BindingIndex,
		Effort:       target.Effort,
	}
}

// ResolutionErrorProjection is the bounded, content-free failure shape.
type ResolutionErrorProjection struct {
	Code      ResolutionErrorCode `json:"code"`
	Purpose   Purpose             `json:"purpose,omitempty"`
	PolicyID  PolicyID            `json:"policy_id,omitempty"`
	CatalogID string              `json:"catalog_id,omitempty"`
	Source    OverrideSource      `json:"source,omitempty"`
	Message   string              `json:"message"`
}

// ProjectResolutionError renders a resolver failure for inspection callers.
// Errors that are not typed resolution failures are reported without detail so
// an internal message never leaks through the API.
func ProjectResolutionError(err error) (ResolutionErrorProjection, bool) {
	var resolution *ResolutionError
	if !errors.As(err, &resolution) {
		return ResolutionErrorProjection{}, false
	}
	return ResolutionErrorProjection{
		Code:      resolution.Code,
		Purpose:   resolution.Purpose,
		PolicyID:  resolution.PolicyID,
		CatalogID: resolution.CatalogID,
		Source:    resolution.Source,
		Message:   resolution.Error(),
	}, true
}

// InspectionRequest is the content-free input to a resolution preview. Model
// is the catalog ID a caller proposes; router-selected purposes require it
// because no live routing decision exists for an inspection call.
type InspectionRequest struct {
	Purpose  Purpose         `json:"purpose"`
	Model    string          `json:"model,omitempty"`
	Provider string          `json:"provider,omitempty"`
	Effort   string          `json:"effort,omitempty"`
	Budget   *BudgetOverride `json:"budget,omitempty"`
}

// Inspect previews how a purpose would resolve in this deployment. Fixed and
// hard-pinned purposes run the real resolver with an optional request override;
// router-selected purposes adopt the proposed model over its deployment
// bindings exactly as a served request would.
func (r *PlanResolver) Inspect(request InspectionRequest, config DeploymentPolicyConfig) (ResolvedPlan, error) {
	spec, found := r.registry.Spec(request.Purpose)
	if !found {
		return ResolvedPlan{}, resolutionError(ResolutionErrorUnknownPurpose, request.Purpose, PolicyID(""), "purpose is not registered")
	}
	var overrides []TargetOverride
	for _, purposeOverride := range config.TargetOverrides {
		if purposeOverride.Purpose == request.Purpose {
			overrides = append(overrides, purposeOverride.Target)
		}
	}
	if spec.SelectionStrategy == SelectionStrategyRouter {
		if request.Model == "" {
			return ResolvedPlan{}, resolutionError(ResolutionErrorMissingSelection, request.Purpose, spec.PolicyID, "router-selected purpose inspection requires a catalog model")
		}
		if _, routable := config.routableModels()[request.Model]; !routable {
			return ResolvedPlan{}, &ResolutionError{Code: ResolutionErrorNoEligibleBinding, Purpose: request.Purpose, PolicyID: spec.PolicyID, CatalogID: request.Model, detail: "model is not a routable catalog target in this deployment"}
		}
		bindings := catalog.AvailableBindings(request.Model, config.AvailableProviders)
		if request.Provider != "" {
			index := slices.IndexFunc(bindings, func(binding catalog.ProviderBinding) bool { return binding.Provider == request.Provider })
			if index < 0 {
				return ResolvedPlan{}, &ResolutionError{Code: ResolutionErrorNoEligibleBinding, Purpose: request.Purpose, PolicyID: spec.PolicyID, CatalogID: request.Model, detail: "requested provider has no available binding for the model"}
			}
			bindings = append([]catalog.ProviderBinding{bindings[index]}, slices.Delete(slices.Clone(bindings), index, index+1)...)
		}
		return r.ResolveRouted(RoutedResolutionRequest{
			Purpose:  request.Purpose,
			Decision: router.Decision{Model: request.Model, Provider: bindings[0].Provider, Effort: request.Effort},
			Bindings: bindings,
			Budget:   request.Budget,
		})
	}
	if request.Model != "" {
		overrides = append(overrides, TargetOverride{Source: OverrideSourceRequest, CatalogID: request.Model, Provider: request.Provider, Effort: request.Effort})
	}
	return r.Resolve(ResolutionRequest{
		Purpose:       request.Purpose,
		RouterRequest: router.Request{EnabledProviders: config.AvailableProviders},
		Overrides:     overrides,
		Budget:        request.Budget,
	})
}
