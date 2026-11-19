package router

import (
	"cmp"
	"slices"
)

const CandidateFallbackReason = "candidate_fallback"

func EligibleRankedFallback(req Request, decision Decision, permitted func(model string) bool) (Decision, bool) {
	md := decision.Metadata
	if md == nil {
		return Decision{}, false
	}
	for _, model := range rankedFallbackModels(md) {
		_, hasHardViolation := req.Candidates.HardFilterViolation(model)
		if model == decision.Model || hasHardViolation || req.Candidates.AutomaticallyExcludes(model) || !permitted(model) {
			continue
		}
		provider := md.CandidateProviders[model]
		if provider == "" {
			continue
		}
		if _, enabled := req.EnabledProviders[provider]; req.EnabledProviders != nil && !enabled {
			continue
		}
		fallbackMetadata := *md
		fallbackMetadata.ChosenScore = md.CandidateScores[model]
		fallbackMetadata.PairedModel, fallbackMetadata.PairedProvider = "", ""
		fallbackMetadata.PolicyPinHonoured = false
		fallbackMetadata.PolicyGroup = ""
		fallbackMetadata.SelectedArmID, fallbackMetadata.SelectedRosterArmID = "", ""
		fallbackMetadata.SelectedUpstreamID, fallbackMetadata.BindingIndex = "", 0
		fallbackMetadata.SelectionTrace = nil
		return Decision{Provider: provider, Model: model, Reason: CandidateFallbackReason, Metadata: &fallbackMetadata}, true
	}
	return Decision{}, false
}

func rankedFallbackModels(md *RoutingMetadata) []string {
	if md.RosterFailover || len(md.RescueModels) > 0 {
		return md.RescueModels
	}
	scored := slices.Clone(md.CandidateModels)
	slices.SortStableFunc(scored, func(a, b string) int { return cmp.Compare(md.CandidateScores[b], md.CandidateScores[a]) })
	return scored
}
