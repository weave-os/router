package servingstrategynames

import (
	"log/slog"

	"weave-os/router/internal/router"
)

const localName = "hmm-local"

const typedName string = "hmm-typed"

func routeWithStrategyNames(strategy router.Strategy, raw string) router.Strategy {
	// Counted: a conversion of a string constant.
	converted := router.Strategy("hmm-converted")
	// Counted: a conversion of a typed string constant.
	if strategy == router.Strategy(typedName) {
		return converted
	}
	// Counted: an untyped literal compared with a router.Strategy.
	if strategy == "hmm-compared" {
		return converted
	}
	// Counted: an untyped literal assigned to a router.Strategy.
	var assigned router.Strategy = "hmm-assigned"
	// Counted: an untyped constant assigned to a router.Strategy.
	assigned = localName
	switch strategy {
	// Counted: two literal switch cases on a router.Strategy.
	case "hmm-case-a", "hmm-case-b":
		return assigned
	// Counted: a router.Strategy* constant case.
	case router.StrategyHMM:
		return converted
	}
	// Not counted: the zero-value check, a conversion of a runtime value, and a
	// log field key that only names a strategy-shaped string.
	if strategy == "" {
		return router.Strategy(raw)
	}
	slog.Info("routed", "decision_model", string(strategy))
	return strategy
}
