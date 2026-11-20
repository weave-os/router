package hmm

import (
	"weave-os/router/internal/router/armid"
	"weave-os/router/internal/router/catalog"
)

func rosterIDFor(model catalog.Model) string { return armid.ForModel(model) }

func SplitEffort(id string) (string, string) { return armid.SplitEffort(id) }

func EffortArm(base, effort string) string { return armid.EffortArm(base, effort) }

func CatalogIDForRoster(id string) string { return armid.CatalogIDForRoster(id) }
