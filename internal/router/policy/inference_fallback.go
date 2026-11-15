package policy

func fallbackBindings(spec PolicySpec, selected plannedBinding, resolved ResolvedCandidates, maxSpendUSD float64) []plannedBinding {
	var candidates []plannedBinding
	switch spec.Fallback.Kind {
	case FallbackKindBinding:
		for _, binding := range resolved.bindingsByCatalogID[selected.CatalogID] {
			candidates = append(candidates, plannedBinding(binding))
		}
	case FallbackKindPlanAlternatives:
		for _, catalogID := range spec.Fallback.Alternatives {
			for _, binding := range resolved.bindingsByCatalogID[catalogID] {
				candidates = append(candidates, plannedBinding(binding))
			}
		}
	case FallbackKindLocalRecovery, FallbackKindFullHistory, FallbackKindRoutedDispatch, FallbackKindNone:
	}

	alternatives := make([]plannedBinding, 0, len(candidates))
	for _, candidate := range candidates {
		if sameBinding(selected.Binding, candidate.Binding) || exceedsSpendBudget(candidate.estimatedCostUSD, maxSpendUSD) {
			continue
		}
		candidate.Effort = selected.Effort
		alternatives = append(alternatives, candidate)
	}
	return alternatives
}
