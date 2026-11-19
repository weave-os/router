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
		if model == decision.Model || !candidateEligible(req, model, permitted) {
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
		return Decision{Provider: provider, Model: model, Reason: CandidateFallbackReason, Metadata: &fallbackMetadata}, true
	}
	return Decision{}, false
}

func candidateEligible(req Request, model string, permitted func(model string) bool) bool {
	_, hard := req.Candidates.HardFilterViolation(model)
	return !hard && !req.Candidates.AutomaticallyExcludes(model) && permitted(model)
}

func rankedFallbackModels(md *RoutingMetadata) []string {
	scored := slices.Clone(md.CandidateModels)
	slices.SortStableFunc(scored, func(a, b string) int { return cmp.Compare(md.CandidateScores[b], md.CandidateScores[a]) })
	return append(slices.Clone(md.RescueModels), scored...)
}
