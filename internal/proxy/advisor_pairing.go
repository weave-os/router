package proxy

import (
	"maps"
	"sort"
	"strings"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

// excludeAdvisorOutrankingModels augments excluded with every Claude model
// the request's advisor_* tool cannot advise: Anthropic 400s an advisor ranked
// below the request model, and the client validated the pairing only against
// the model it asked for. Returns every such model in available, including
// ones already excluded, so callers can keep them out of later re-admission.
func excludeAdvisorOutrankingModels(env *translate.RequestEnvelope, excluded, available map[string]struct{}) (map[string]struct{}, []string) {
	advisorRank, ok := catalog.AdvisorRankFor(env.AdvisorToolModel())
	if !ok {
		return excluded, nil
	}
	var modelsAdvisorCannotAdvise []string
	for model := range available {
		if advisorCannotAdvise(advisorRank, model) {
			modelsAdvisorCannotAdvise = append(modelsAdvisorCannotAdvise, model)
		}
	}
	if len(modelsAdvisorCannotAdvise) == 0 {
		return excluded, nil
	}
	updatedExcluded := make(map[string]struct{}, len(excluded)+len(modelsAdvisorCannotAdvise))
	maps.Copy(updatedExcluded, excluded)
	for _, model := range modelsAdvisorCannotAdvise {
		updatedExcluded[model] = struct{}{}
	}
	sort.Strings(modelsAdvisorCannotAdvise)
	return updatedExcluded, modelsAdvisorCannotAdvise
}

// advisorRejectsModel reports whether env's advisor_* tool cannot advise
// model, so a pin or force re-admitting model would dispatch into a 400.
func advisorRejectsModel(env *translate.RequestEnvelope, model string) bool {
	advisorRank, ok := catalog.AdvisorRankFor(env.AdvisorToolModel())
	return ok && advisorCannotAdvise(advisorRank, model)
}

func advisorCannotAdvise(advisorRank int, model string) bool {
	if !strings.HasPrefix(model, "claude-") {
		return false
	}
	rank, ranked := catalog.AdvisorRankFor(model)
	return !ranked || rank > advisorRank
}
