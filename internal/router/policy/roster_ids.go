package policy

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
)

func ValidateRosterIDs(rosterIDs []string) []Diagnostic {
	return ValidateRosterIDsWithCatalog(rosterIDs, catalog.Models, ManagedProviderPolicy())
}

func ValidateRosterIDsWithCatalog(rosterIDs []string, models []catalog.Model, providerPolicy ProviderPolicy) []Diagnostic {
	inverse := make(map[string][]catalog.Model, len(models))
	for _, m := range models {
		rosterID := armid.ForModel(m)
		if rosterID == "" {
			continue
		}
		inverse[rosterID] = append(inverse[rosterID], m)
	}

	var diagnostics []Diagnostic
	for _, rosterID := range rosterIDs {
		matches := inverse[armid.Parse(rosterID).Base]
		switch {
		case len(matches) == 0:
			diagnostics = append(diagnostics, Diagnostic{RosterID: rosterID, Reason: ExclusionUnknownCatalogModel})
		case len(matches) > 1:
			diagnostics = append(diagnostics, Diagnostic{RosterID: rosterID, CatalogID: matches[0].ID, Reason: ExclusionAmbiguousRoster})
		case !hasRoutableBindingAllowedBy(matches[0], providerPolicy):
			diagnostics = append(diagnostics, Diagnostic{RosterID: rosterID, CatalogID: matches[0].ID, Reason: ExclusionProviderPolicy})
		}
	}
	return diagnostics
}

func hasRoutableBindingAllowedBy(m catalog.Model, providerPolicy ProviderPolicy) bool {
	for _, binding := range catalog.RoutableBindings(m) {
		if providerPolicy.Allows(binding.Provider) {
			return true
		}
	}
	return false
}
