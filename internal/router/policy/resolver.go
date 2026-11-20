package policy

import (
	"sort"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

type RosterMapper func(catalog.Model) string

type ProviderPolicy struct {
	Denied map[string]struct{}
}

func ManagedProviderPolicy() ProviderPolicy {
	return ProviderPolicy{Denied: map[string]struct{}{providers.ProviderOpenRouter: {}}}
}

func (p ProviderPolicy) Allows(provider string) bool {
	_, denied := p.Denied[provider]
	return !denied
}

type ExclusionReason string

const (
	ExclusionRequested           ExclusionReason = "requested_exclusion"
	ExclusionNotAllowlisted      ExclusionReason = "not_allowlisted"
	ExclusionUnknownCatalogModel ExclusionReason = "unknown_catalog_model"
	ExclusionProductIneligible   ExclusionReason = "product_ineligible"
	ExclusionUnmappedRoster      ExclusionReason = "unmapped_roster"
	ExclusionNoProvider          ExclusionReason = "no_enabled_provider"
	ExclusionProviderPolicy      ExclusionReason = "provider_policy"
	ExclusionImageCapability     ExclusionReason = "image_capability"
	ExclusionToolCapability      ExclusionReason = "tool_capability"
	ExclusionAmbiguousRoster     ExclusionReason = "ambiguous_roster_id"
	ExclusionContextWindow       ExclusionReason = "context_window"
	ExclusionImageLimit          ExclusionReason = "image_limit"
	ExclusionUnsignedHistory     ExclusionReason = "unsigned_history"
	ExclusionGatewayNotServed    ExclusionReason = "gateway_not_served"
	ExclusionAutomaticDisabled   ExclusionReason = "automatic_disabled"
)

type Diagnostic struct {
	CatalogID string          `json:"catalog_id"`
	RosterID  string          `json:"roster_id,omitempty"`
	Reason    ExclusionReason `json:"reason"`
}

type Candidate struct {
	ArmID                        string                `json:"arm_id"`
	RosterID                     string                `json:"roster_id"`
	CatalogID                    string                `json:"catalog_id"`
	Provider                     string                `json:"provider"`
	UpstreamID                   string                `json:"upstream_id"`
	BindingIndex                 int                   `json:"binding_index"`
	Endpoint                     string                `json:"endpoint"`
	ModelRevision                string                `json:"model_revision"`
	ReasoningConfigurationSHA256 string                `json:"reasoning_configuration_sha256"`
	ToolConfigurationSHA256      string                `json:"tool_configuration_sha256"`
	PreferenceRank               *int                  `json:"preference_rank,omitempty"`
	InputUSDPer1M                float64               `json:"input_usd_per_1m"`
	OutputUSDPer1M               float64               `json:"output_usd_per_1m"`
	EstimatedCostUSD             float64               `json:"estimated_cost_usd"`
	CacheReadMultiplier          float64               `json:"cache_read_multiplier"`
	MarginalCostFactor           float64               `json:"marginal_cost_factor"`
	EffectiveInputUSDPer1M       float64               `json:"effective_input_usd_per_1m"`
	EffectiveOutputUSDPer1M      float64               `json:"effective_output_usd_per_1m"`
	EffectiveEstimatedCostUSD    float64               `json:"effective_estimated_cost_usd"`
	Capabilities                 CandidateCapabilities `json:"capabilities"`
}

type CandidateCapabilities struct {
	ContextWindow  int    `json:"context_window"`
	Tier           string `json:"tier"`
	SupportsTools  bool   `json:"supports_tools"`
	SupportsImages bool   `json:"supports_images"`
}

type Binding struct {
	ArmID                        string
	CatalogID                    string
	Provider                     string
	UpstreamID                   string
	BindingIndex                 int
	Endpoint                     string
	ModelRevision                string
	ReasoningConfigurationSHA256 string
	ToolConfigurationSHA256      string
	Effort                       string
}

type ResolvedCandidates struct {
	Candidates          []Candidate
	ByArmID             map[string]Binding
	ByRosterID          map[string]Binding
	Diagnostics         []Diagnostic
	bindingsByCatalogID map[string][]resolvedBinding
}

func (r ResolvedCandidates) BindingsForCatalog(catalogID string) []Binding {
	resolvedBindings := r.bindingsByCatalogID[catalogID]
	bindings := make([]Binding, len(resolvedBindings))
	for index, resolvedBinding := range resolvedBindings {
		bindings[index] = resolvedBinding.Binding
	}
	return bindings
}

type resolvedBinding struct {
	Binding
	estimatedCostUSD float64
}

func (r ResolvedCandidates) CandidateModels() []string {
	models := make([]string, 0, len(r.Candidates))
	seen := make(map[string]struct{}, len(r.Candidates))
	for _, candidate := range r.Candidates {
		if _, exists := seen[candidate.CatalogID]; exists {
			continue
		}
		seen[candidate.CatalogID] = struct{}{}
		models = append(models, candidate.CatalogID)
	}
	return models
}

func (r ResolvedCandidates) CandidateArmIDs() []string {
	armIDs := make([]string, 0, len(r.Candidates))
	for _, candidate := range r.Candidates {
		armIDs = append(armIDs, candidate.ArmID)
	}
	return armIDs
}

func (r ResolvedCandidates) CandidateProviders() map[string]string {
	result := make(map[string]string, len(r.Candidates))
	for _, candidate := range r.Candidates {
		if _, exists := result[candidate.CatalogID]; exists {
			continue
		}
		result[candidate.CatalogID] = candidate.Provider
	}
	return result
}

func (r ResolvedCandidates) CandidateArmProviders() map[string]string {
	result := make(map[string]string, len(r.Candidates))
	for _, candidate := range r.Candidates {
		result[candidate.ArmID] = candidate.Provider
	}
	return result
}

func (r ResolvedCandidates) CatalogCandidateScores(scores map[string]float32) map[string]float32 {
	result := make(map[string]float32, len(scores))
	for _, candidate := range r.Candidates {
		if _, exists := result[candidate.CatalogID]; exists {
			continue
		}
		score, ok := scores[candidate.ArmID]
		if !ok {
			score, ok = scores[candidate.RosterID]
		}
		if ok {
			result[candidate.CatalogID] = score
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (r ResolvedCandidates) ArmCandidateScores(scores map[string]float32) map[string]float32 {
	result := make(map[string]float32, len(scores))
	for armID, score := range scores {
		if _, ok := r.ByArmID[armID]; ok {
			result[armID] = score
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

type Resolver struct {
	deployed          map[string]struct{}
	available         map[string]struct{}
	mapper            RosterMapper
	providerPolicy    ProviderPolicy
	enumerateBindings bool
	routerSelectsArm  bool
	toolLow           map[string]struct{}
	imageLow          map[string]struct{}
}

func NewResolver(deployed, available map[string]struct{}, mapper RosterMapper, providerPolicy ProviderPolicy) *Resolver {
	return &Resolver{
		deployed:       deployed,
		available:      available,
		mapper:         mapper,
		providerPolicy: providerPolicy,
		toolLow:        catalog.ToolUseLowSet(),
		imageLow:       catalog.ImageUnsupportedSet(),
	}
}

func NewArmResolver(deployed, available map[string]struct{}, mapper RosterMapper, providerPolicy ProviderPolicy) *Resolver {
	resolver := NewResolver(deployed, available, mapper, providerPolicy)
	resolver.enumerateBindings = true
	return resolver
}

func (r *Resolver) RouterSelectsArm() {
	r.routerSelectsArm = true
}

func (r *Resolver) SchemaVersion() string {
	if r.routerSelectsArm {
		return SchemaVersionV4
	}
	if r.enumerateBindings {
		return SchemaVersionV2
	}
	return SchemaVersionV1
}

type eligibleCandidate struct {
	Candidate
	bindings []resolvedBinding
}

func (r *Resolver) Resolve(req router.Request) ResolvedCandidates {
	diagnostics := make([]Diagnostic, 0)
	base := make([]eligibleCandidate, 0, len(r.deployed))
	preferenceRanks := preferenceRanks(req.PreferredModels)
	armContext := DeriveArmContext(req)

	providerSet := req.EnabledProviders
	if providerSet == nil {
		providerSet = r.available
	}
	gateways := req.GatewayProviders

	deployedIDs := make([]string, 0, len(r.deployed))
	for id := range r.deployed {
		deployedIDs = append(deployedIDs, id)
	}
	sort.Strings(deployedIDs)

	for _, id := range deployedIDs {
		if len(req.Candidates.AllowedModels) > 0 {
			if _, allowed := req.Candidates.AllowedModels[id]; !allowed {
				diagnostics = append(diagnostics, Diagnostic{CatalogID: id, Reason: ExclusionNotAllowlisted})
				continue
			}
		}
		model, ok := catalog.ByID(id)
		if !ok {
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, Reason: ExclusionUnknownCatalogModel})
			continue
		}
		if !req.ProductEligibility.PermitsSource(model.Source) {
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, Reason: ExclusionProductIneligible})
			continue
		}
		_, unsignedHistoryExcluded := req.Candidates.UnsignedHistoryExcludedModels[id]
		_, contextWindowExcluded := req.Candidates.ContextWindowExcludedModels[id]
		_, imageLimitExcluded := req.Candidates.ImageLimitExcludedModels[id]
		if _, excluded := req.Candidates.ExcludedModels[id]; excluded && !unsignedHistoryExcluded && !contextWindowExcluded && !imageLimitExcluded {
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, Reason: ExclusionRequested})
			continue
		}
		rosterID := r.mapper(model)
		if rosterID == "" {
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, Reason: ExclusionUnmappedRoster})
			continue
		}
		if unsignedHistoryExcluded {
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, RosterID: rosterID, Reason: ExclusionUnsignedHistory})
			continue
		}
		if contextWindowExcluded {
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, RosterID: rosterID, Reason: ExclusionContextWindow})
			continue
		}
		if imageLimitExcluded {
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, RosterID: rosterID, Reason: ExclusionImageLimit})
			continue
		}
		contextWindow := catalog.ContextWindowFor(id)
		if exceedsContextWindow(req, id, contextWindow) {
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, RosterID: rosterID, Reason: ExclusionContextWindow})
			continue
		}

		if len(gateways) > 0 {
			allowedBindings := gatewayBindings(id, gateways, req.CustomBindings)
			if len(allowedBindings) == 0 {
				diagnostics = append(diagnostics, Diagnostic{CatalogID: id, RosterID: rosterID, Reason: ExclusionGatewayNotServed})
				continue
			}
			allowedBindings = imageEligibleBindings(allowedBindings, req.ImageCount)
			if len(allowedBindings) == 0 {
				diagnostics = append(diagnostics, Diagnostic{CatalogID: id, RosterID: rosterID, Reason: ExclusionImageLimit})
				continue
			}
			base = r.appendCandidates(base, candidateContext{
				req:             req,
				catalogID:       id,
				rosterID:        rosterID,
				model:           model,
				contextWindow:   contextWindow,
				armContext:      armContext,
				preferenceRanks: preferenceRanks,
			}, allowedBindings)
			continue
		}

		allowedBindings := catalog.EnumerateBindingsWithCustom(
			id,
			r.allowedProviders(providerSet),
			req.CustomBindings,
		)
		if len(allowedBindings) == 0 {
			reason := ExclusionNoProvider
			if unrestrictedBindings := catalog.EnumerateBindingsWithCustom(id, providerSet, req.CustomBindings); len(unrestrictedBindings) > 0 {
				reason = ExclusionProviderPolicy
			}
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, RosterID: rosterID, Reason: reason})
			continue
		}
		allowedBindings = imageEligibleBindings(allowedBindings, req.ImageCount)
		if len(allowedBindings) == 0 {
			diagnostics = append(diagnostics, Diagnostic{CatalogID: id, RosterID: rosterID, Reason: ExclusionImageLimit})
			continue
		}

		base = r.appendCandidates(base, candidateContext{
			req:             req,
			catalogID:       id,
			rosterID:        rosterID,
			model:           model,
			contextWindow:   contextWindow,
			armContext:      armContext,
			preferenceRanks: preferenceRanks,
		}, allowedBindings)
	}

	base, diagnostics = softFilter(base, req.HasImages, r.imageLow, ExclusionImageCapability, diagnostics)
	base, diagnostics = softFilter(base, req.HasTools, r.toolLow, ExclusionToolCapability, diagnostics)
	base, diagnostics = softFilter(base, true, req.Candidates.AutomaticExcludedModels, ExclusionAutomaticDisabled, diagnostics)

	selectionCounts := make(map[string]int, len(base))
	for _, candidate := range base {
		selectionCounts[candidate.ArmID]++
	}

	resolved := ResolvedCandidates{
		Candidates:          make([]Candidate, 0, len(base)),
		ByArmID:             make(map[string]Binding, len(base)),
		ByRosterID:          make(map[string]Binding, len(base)),
		Diagnostics:         diagnostics,
		bindingsByCatalogID: make(map[string][]resolvedBinding, len(base)),
	}
	ambiguousRosterIDs := make(map[string]struct{})
	for _, candidate := range base {
		if selectionCounts[candidate.ArmID] > 1 {
			resolved.Diagnostics = append(resolved.Diagnostics, Diagnostic{
				CatalogID: candidate.CatalogID,
				RosterID:  candidate.RosterID,
				Reason:    ExclusionAmbiguousRoster,
			})
			continue
		}
		resolved.Candidates = append(resolved.Candidates, candidate.Candidate)
		binding := bindingFromCandidate(candidate.Candidate)
		resolved.ByArmID[candidate.ArmID] = binding
		if _, recorded := resolved.bindingsByCatalogID[candidate.CatalogID]; !recorded {
			resolved.bindingsByCatalogID[candidate.CatalogID] = append([]resolvedBinding(nil), candidate.bindings...)
		}
		if _, ambiguous := ambiguousRosterIDs[candidate.RosterID]; ambiguous {
			continue
		}
		if _, exists := resolved.ByRosterID[candidate.RosterID]; !exists {
			resolved.ByRosterID[candidate.RosterID] = binding
		} else {
			delete(resolved.ByRosterID, candidate.RosterID)
			ambiguousRosterIDs[candidate.RosterID] = struct{}{}
		}
	}
	return resolved
}

func imageEligibleBindings(bindings []catalog.IndexedBinding, imageCount int) []catalog.IndexedBinding {
	if imageCount <= 0 {
		return bindings
	}
	filtered := make([]catalog.IndexedBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.AcceptsImageCount(imageCount) {
			filtered = append(filtered, binding)
		}
	}
	return filtered
}

func (r ResolvedCandidates) BindingForSelection(armID, rosterID string) (Binding, bool) {
	if armID != "" {
		lookup, effort := splitEffort(armID)
		if binding, ok := r.ByArmID[lookup]; ok {
			binding.Effort = effort
			return binding, ok
		}
	}
	lookup, effort := splitEffort(rosterID)
	binding, ok := r.ByRosterID[lookup]
	if ok {
		binding.Effort = effort
	}
	return binding, ok
}

func splitEffort(armID string) (string, string) {
	for i := len(armID) - 1; i > 0; i-- {
		if armID[i] == ':' {
			suffix := armID[i+1:]
			if router.CanonicalizeEffort(suffix) == suffix && router.IsValidEffort(suffix) {
				return armID[:i], suffix
			}
			return armID, ""
		}
	}
	return armID, ""
}

func estimatedCostUSD(req router.Request, pricing catalog.Pricing) float64 {
	pricing = pricing.ForInputTokens(req.EstimatedInputTokens)
	outputTokens := expectedOutputTokens(req)
	return (float64(req.EstimatedInputTokens)*pricing.InputUSDPer1M +
		float64(outputTokens)*pricing.OutputUSDPer1M) / 1_000_000
}

func requiredContextTokens(req router.Request) int {
	return max(req.EstimatedInputTokens, 0) + expectedOutputTokens(req)
}

func exceedsContextWindow(req router.Request, catalogID string, contextWindow int) bool {
	if _, admitted := req.Candidates.OverflowAdmittedModels[catalogID]; admitted {
		return false
	}
	return requiredContextTokens(req) > contextWindow
}

func expectedOutputTokens(req router.Request) int {
	if req.RoutingKnobs == nil || req.RoutingKnobs.ExpectedOutputTokens == nil {
		return 0
	}
	return max(*req.RoutingKnobs.ExpectedOutputTokens, 0)
}

type candidateContext struct {
	req             router.Request
	catalogID       string
	rosterID        string
	model           catalog.Model
	contextWindow   int
	armContext      ArmContext
	preferenceRanks map[string]*int
}

func (r *Resolver) appendCandidates(base []eligibleCandidate, ctx candidateContext, bindings []catalog.IndexedBinding) []eligibleCandidate {
	expanded := make([]Candidate, 0, len(bindings))
	for _, binding := range bindings {
		upstreamID := catalog.UpstreamIDFor(ctx.catalogID, binding.UpstreamID)
		modelRevision := upstreamID
		armID := ctx.rosterID
		if r.enumerateBindings {
			armID = MakeArmID(ArmIdentity{
				CanonicalModel:               ctx.catalogID,
				Provider:                     binding.Provider,
				UpstreamID:                   upstreamID,
				Endpoint:                     ctx.armContext.Endpoint,
				ModelRevision:                modelRevision,
				ReasoningConfigurationSHA256: ctx.armContext.ReasoningConfigurationSHA256,
				ToolConfigurationSHA256:      ctx.armContext.ToolConfigurationSHA256,
			})
		}
		marginalCostFactor := 1.0
		pricing := binding.Price.ForInputTokens(ctx.req.EstimatedInputTokens)
		expanded = append(expanded, Candidate{
			ArmID:                        armID,
			RosterID:                     ctx.rosterID,
			CatalogID:                    ctx.catalogID,
			Provider:                     binding.Provider,
			UpstreamID:                   upstreamID,
			BindingIndex:                 binding.Index,
			Endpoint:                     ctx.armContext.Endpoint,
			ModelRevision:                modelRevision,
			ReasoningConfigurationSHA256: ctx.armContext.ReasoningConfigurationSHA256,
			ToolConfigurationSHA256:      ctx.armContext.ToolConfigurationSHA256,
			PreferenceRank:               ctx.preferenceRanks[ctx.catalogID],
			InputUSDPer1M:                pricing.InputUSDPer1M,
			OutputUSDPer1M:               pricing.OutputUSDPer1M,
			EstimatedCostUSD:             estimatedCostUSD(ctx.req, pricing),
			CacheReadMultiplier:          pricing.EffectiveCacheReadMultiplier(),
			MarginalCostFactor:           marginalCostFactor,
			EffectiveInputUSDPer1M:       pricing.InputUSDPer1M * marginalCostFactor,
			EffectiveOutputUSDPer1M:      pricing.OutputUSDPer1M * marginalCostFactor,
			EffectiveEstimatedCostUSD:    estimatedCostUSD(ctx.req, pricing) * marginalCostFactor,
			Capabilities: CandidateCapabilities{
				ContextWindow:  ctx.contextWindow,
				Tier:           ctx.model.Tier.String(),
				SupportsTools:  ctx.model.ToolUseQuality != catalog.ToolUseLow && ctx.model.AgenticUse != catalog.AgenticLow,
				SupportsImages: ctx.model.ImageInput != catalog.ImageInputUnsupported,
			},
		})
	}
	resolvedBindings := make([]resolvedBinding, len(expanded))
	for index, candidate := range expanded {
		resolvedBindings[index] = resolvedBinding{
			Binding:          bindingFromCandidate(candidate),
			estimatedCostUSD: candidate.EstimatedCostUSD,
		}
	}
	if !r.enumerateBindings {
		return append(base, eligibleCandidate{Candidate: expanded[0], bindings: resolvedBindings})
	}
	for _, candidate := range expanded {
		base = append(base, eligibleCandidate{Candidate: candidate, bindings: resolvedBindings})
	}
	return base
}

func bindingFromCandidate(candidate Candidate) Binding {
	return Binding{
		ArmID:                        candidate.ArmID,
		CatalogID:                    candidate.CatalogID,
		Provider:                     candidate.Provider,
		UpstreamID:                   candidate.UpstreamID,
		BindingIndex:                 candidate.BindingIndex,
		Endpoint:                     candidate.Endpoint,
		ModelRevision:                candidate.ModelRevision,
		ReasoningConfigurationSHA256: candidate.ReasoningConfigurationSHA256,
		ToolConfigurationSHA256:      candidate.ToolConfigurationSHA256,
	}
}

func gatewayBindings(id string, gateways map[string]struct{}, custom map[string][]string) []catalog.IndexedBinding {
	aliased := make(map[string]struct{}, len(custom[id]))
	for _, provider := range custom[id] {
		if _, ok := gateways[provider]; ok {
			aliased[provider] = struct{}{}
		}
	}
	if len(aliased) == 0 {
		return nil
	}
	return catalog.EnumerateBindingsWithCustom(id, aliased, custom)
}

func (r *Resolver) allowedProviders(in map[string]struct{}) map[string]struct{} {
	allowed := make(map[string]struct{}, len(in))
	for provider := range in {
		if r.providerPolicy.Allows(provider) {
			allowed[provider] = struct{}{}
		}
	}
	return allowed
}

func preferenceRanks(models []string) map[string]*int {
	result := make(map[string]*int, len(models))
	for rank, model := range models {
		if _, exists := result[model]; exists {
			continue
		}
		rankCopy := rank
		result[model] = &rankCopy
	}
	return result
}

func softFilter(in []eligibleCandidate, active bool, drop map[string]struct{}, reason ExclusionReason, diagnostics []Diagnostic) ([]eligibleCandidate, []Diagnostic) {
	if !active || len(drop) == 0 {
		return in, diagnostics
	}
	kept := make([]eligibleCandidate, 0, len(in))
	dropped := make([]eligibleCandidate, 0)
	for _, candidate := range in {
		if _, shouldDrop := drop[candidate.CatalogID]; shouldDrop {
			dropped = append(dropped, candidate)
			continue
		}
		kept = append(kept, candidate)
	}
	if len(kept) == 0 {
		return in, diagnostics
	}
	for _, candidate := range dropped {
		diagnostics = append(diagnostics, Diagnostic{CatalogID: candidate.CatalogID, RosterID: candidate.RosterID, Reason: reason})
	}
	return kept, diagnostics
}
