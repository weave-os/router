package strategyfromcontextvalue

import (
	"context"

	"weave-os/router/internal/router"
)

// Counted: a package-level function value.
var readStrategy = router.StrategyFromContext

// Counted: a local function value, called indirectly.
func selectedVia(ctx context.Context) router.Strategy {
	f := router.StrategyFromContext
	return f(ctx)
}

func selected(ctx context.Context) router.Strategy {
	return readStrategy(ctx)
}
