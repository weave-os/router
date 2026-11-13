package hmm

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
)

func rosterIDFor(model catalog.Model) string { return armid.ForModel(model) }

// SplitEffort separates a recognized canonical effort suffix; otherwise the arm stays intact.
func SplitEffort(id string) (string, string) { return armid.SplitEffort(id) }

// EffortArm appends a nonempty effort suffix, leaving bare arms unchanged.
func EffortArm(base, effort string) string { return armid.EffortArm(base, effort) }

// CatalogIDForRoster returns the matching catalog ID, or the unchanged arm when unmapped.
func CatalogIDForRoster(id string) string { return armid.CatalogIDForRoster(id) }
