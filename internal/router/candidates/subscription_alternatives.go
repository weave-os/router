package candidates

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/armid"
	"weave-os/router/internal/router/catalog"
)

func SubscriptionAlternatives(req router.Request, selected router.Decision, trace *router.SelectionTrace, servable func(provider, model string) bool) []router.Decision {
	var ordered []string
	if trace != nil && trace.SelectedGroup != "" {
		ordered = trace.EffectiveOrders[trace.SelectedGroup]
	}
	if len(ordered) == 0 {
		ordered = selected.Metadata.RescueModels
	}
	if len(ordered) == 0 {
		ordered = selected.Metadata.CandidateModels
	}
	selectedEntry, selectedKnown := catalog.ByID(selected.Model)
	if !selectedKnown {
		return nil
	}
	var alternatives []router.Decision
	seen := map[string]bool{selected.Model: true}
	for _, arm := range ordered {
		model, effort := armid.SplitEffort(arm)
		model = armid.CatalogIDForRoster(model)
		eligible := false
		for _, candidate := range selected.Metadata.CandidateModels {
			if candidate == model {
				eligible = true
			}
		}
		if trace != nil {
			for _, candidate := range trace.CandidateRosterIDs {
				base, _ := armid.SplitEffort(candidate)
				if armid.CatalogIDForRoster(base) == model {
					eligible = true
				}
			}
			for _, rejection := range trace.ResolverExclusions {
				if rejection.CatalogID == model {
					eligible = false
					break
				}
			}
		}
		if !eligible {
			continue
		}
		if seen[model] {
			continue
		}
		seen[model] = true
		if _, blocked := req.Candidates.ExcludedModels[model]; blocked {
			continue
		}
		if _, blocked := req.Candidates.AutomaticExcludedModels[model]; blocked {
			continue
		}
		if _, blocked := req.Candidates.SafetyExcludedModels[model]; blocked {
			continue
		}
		if _, blocked := req.Candidates.UnsignedHistoryExcludedModels[model]; blocked {
			continue
		}
		entry, known := catalog.ByID(model)
		if !known || entry.Tier < selectedEntry.Tier || req.HasTools && (entry.ToolUseQuality == catalog.ToolUseLow || entry.AgenticUse == catalog.AgenticLow) ||
			req.HasImages && entry.ImageInput == catalog.ImageInputUnsupported ||
			req.ImageCount > 0 && !catalog.AcceptsImageCount(model, req.ImageCount, req.EnabledProviders) {
			continue
		}
		if entry.ContextWindow > 0 && req.EstimatedInputTokens > entry.ContextWindow {
			if _, admitted := req.Candidates.OverflowAdmittedModels[model]; !admitted {
				continue
			}
		}
		for _, binding := range entry.Providers {
			if req.EnabledProviders != nil {
				if _, enabled := req.EnabledProviders[binding.Provider]; !enabled {
					continue
				}
			}
			if !servable(binding.Provider, model) {
				continue
			}
			alternatives = append(alternatives, router.Decision{Model: model, Provider: binding.Provider, Effort: effort, Metadata: selected.Metadata, Reason: selected.Reason})
			break
		}
	}
	return alternatives
}
