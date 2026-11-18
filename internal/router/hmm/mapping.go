package hmm

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
)

func rosterIDFor(model catalog.Model) string { return armid.ForModel(model) }

func SplitEffort(id string) (string, string) { return armid.SplitEffort(id) }

func EffortArm(base, effort string) string { return armid.EffortArm(base, effort) }

func CatalogIDForRoster(id string) string { return armid.CatalogIDForRoster(id) }
