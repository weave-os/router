package candidates

import (
	"slices"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/armid"
	"weave-os/router/internal/router/catalog"
)

func SubscriptionAlternatives(req router.Request, selected router.Decision, trace *router.SelectionTrace, servable func(provider, model string) bool) []router.Decision {
	selectedEntry, selectedKnown := catalog.ByID(selected.Model)
	if !selectedKnown {
		return nil
	}
	var alternatives []router.Decision
	seen := map[string]bool{selected.Model: true}
	for _, arm := range alternativeOrder(selected.Metadata, trace) {
		model, effort := armid.SplitEffort(arm)
		model = armid.CatalogIDForRoster(model)
		if !alternativeCandidate(model, selected.Metadata, trace) || seen[model] {
			continue
		}
		seen[model] = true
		if requestExcludesModel(req.Candidates, model) {
			continue
		}
		entry, known := catalog.ByID(model)
		if !known || !meetsSelectedBar(req, model, entry, selectedEntry) {
			continue
		}
		if provider, ok := subscriptionProvider(req, model, entry, servable); ok {
			alternatives = append(alternatives, router.Decision{Model: model, Provider: provider, Effort: effort, Metadata: selected.Metadata, Reason: selected.Reason})
		}
	}
	return alternatives
}

func alternativeOrder(metadata *router.RoutingMetadata, trace *router.SelectionTrace) []string {
	var ordered []string
	if trace != nil && trace.SelectedGroup != "" {
		ordered = trace.EffectiveOrders[trace.SelectedGroup]
	}
	if len(ordered) == 0 {
		ordered = metadata.RescueModels
	}
	if len(ordered) == 0 {
		ordered = metadata.CandidateModels
	}
	return ordered
}

func alternativeCandidate(model string, metadata *router.RoutingMetadata, trace *router.SelectionTrace) bool {
	eligible := slices.Contains(metadata.CandidateModels, model)
	if trace == nil {
		return eligible
	}
	for _, candidate := range trace.CandidateRosterIDs {
		base, _ := armid.SplitEffort(candidate)
		if armid.CatalogIDForRoster(base) == model {
			eligible = true
		}
	}
	for _, rejection := range trace.ResolverExclusions {
		if rejection.CatalogID == model {
			return false
		}
	}
	return eligible
}

func requestExcludesModel(candidates router.Candidates, model string) bool {
	for _, excluded := range []map[string]struct{}{candidates.ExcludedModels, candidates.AutomaticExcludedModels, candidates.SafetyExcludedModels, candidates.UnsignedHistoryExcludedModels} {
		if _, blocked := excluded[model]; blocked {
			return true
		}
	}
	return false
}

func meetsSelectedBar(req router.Request, model string, entry, selectedEntry catalog.Model) bool {
	if entry.Tier < selectedEntry.Tier || req.HasTools && (entry.ToolUseQuality == catalog.ToolUseLow || entry.AgenticUse == catalog.AgenticLow) ||
		req.HasImages && entry.ImageInput == catalog.ImageInputUnsupported ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(model, req.ImageCount, req.EnabledProviders) {
		return false
	}
	if entry.ContextWindow > 0 && req.EstimatedInputTokens > entry.ContextWindow {
		_, admitted := req.Candidates.OverflowAdmittedModels[model]
		return admitted
	}
	return true
}

func subscriptionProvider(req router.Request, model string, entry catalog.Model, servable func(provider, model string) bool) (string, bool) {
	for _, binding := range entry.Providers {
		if req.EnabledProviders != nil {
			if _, enabled := req.EnabledProviders[binding.Provider]; !enabled {
				continue
			}
		}
		if servable(binding.Provider, model) {
			return binding.Provider, true
		}
	}
	return "", false
}
