package armid

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

type Diagnostic = policy.Diagnostic

func ValidateRosterIDs(rosterIDs []string) []Diagnostic {
	return ValidateWithCatalog(rosterIDs, catalog.Models, policy.ManagedProviderPolicy())
}

func ValidateWithCatalog(rosterIDs []string, models []catalog.Model, providerPolicy policy.ProviderPolicy) []Diagnostic {
	inverse := make(map[string][]catalog.Model, len(models))
	for _, m := range models {
		rosterID := ForModel(m)
		if rosterID == "" {
			continue
		}
		inverse[rosterID] = append(inverse[rosterID], m)
	}

	var diagnostics []Diagnostic
	for _, rosterID := range rosterIDs {
		baseID, _ := SplitEffort(rosterID)
		matches := inverse[baseID]
		switch {
		case len(matches) == 0:
			diagnostics = append(diagnostics, Diagnostic{RosterID: rosterID, Reason: policy.ExclusionUnknownCatalogModel})
		case len(matches) > 1:
			diagnostics = append(diagnostics, Diagnostic{RosterID: rosterID, CatalogID: matches[0].ID, Reason: policy.ExclusionAmbiguousRoster})
		case !hasAllowedProvider(matches[0], providerPolicy):
			diagnostics = append(diagnostics, Diagnostic{RosterID: rosterID, CatalogID: matches[0].ID, Reason: policy.ExclusionProviderPolicy})
		}
	}
	return diagnostics
}

func hasAllowedProvider(m catalog.Model, providerPolicy policy.ProviderPolicy) bool {
	for _, binding := range catalog.RoutableBindings(m) {
		if providerPolicy.Allows(binding.Provider) {
			return true
		}
	}
	return false
}
