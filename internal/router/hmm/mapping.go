package hmm

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
)

func rosterIDFor(model catalog.Model) string { return armid.ForModel(model) }

// SplitEffort preserves the existing roster arm syntax.
func SplitEffort(id string) (string, string) { return armid.SplitEffort(id) }

// EffortArm composes a roster arm ID from its base and effort.
func EffortArm(base, effort string) string { return armid.EffortArm(base, effort) }

// CatalogIDForRoster uses the shared catalog mapping used by release validation.
func CatalogIDForRoster(id string) string { return armid.CatalogIDForRoster(id) }
