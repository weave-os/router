package hmm

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/policy"
)

// Diagnostic describes an undispatchable roster arm for conformance and debug inspection.
type Diagnostic = policy.Diagnostic

// ValidateRosterIDs reports undispatchable roster arms without initializing a router.
func ValidateRosterIDs(ids []string) []Diagnostic { return armid.ValidateRosterIDs(ids) }

func validateRosterIDs(ids []string, models []catalog.Model, providerPolicy policy.ProviderPolicy) []Diagnostic {
	return armid.ValidateWithCatalog(ids, models, providerPolicy)
}
