package servingstrategy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/decisionmodel"
)

var _ = decisionmodel.ErrUnavailable

var _ = router.StrategyHMM == router.Strategy("x")
