package hmm

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

// Diagnostic re-exports policy.Diagnostic for roster-validation callers.
type Diagnostic = policy.Diagnostic

// ValidateRosterIDs reports roster arms that cannot be dispatched — unknown,
// ambiguous, or provider-policy-blocked. Observation only: DeployedModelsForRosterIDs's
// silent drop is unchanged.
func ValidateRosterIDs(rosterIDs []string) []Diagnostic {
	return validateRosterIDs(rosterIDs, catalog.Models, policy.ManagedProviderPolicy())
}

func validateRosterIDs(rosterIDs []string, models []catalog.Model, providerPolicy policy.ProviderPolicy) []Diagnostic {
	inverse := make(map[string][]catalog.Model, len(models))
	for _, m := range models {
		rosterID := rosterIDFor(m)
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
	for _, binding := range m.Providers {
		if providerPolicy.Allows(binding.Provider) {
			return true
		}
	}
	return false
}
