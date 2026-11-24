package strategymembership

import (
	"context"

	"weave-os/router/internal/router"
)

type Service struct{ strategy router.Router }

// Reported: routeWithStrategy no longer calls eligibleStrategyDecision.
func (s *Service) routeWithStrategy(ctx context.Context, req router.Request) (router.Decision, error) {
	return s.routeWithStrategyUnchecked(ctx, req)
}

func (s *Service) routeWithStrategyUnchecked(ctx context.Context, req router.Request) (router.Decision, error) {
	return s.strategy.Route(ctx, req)
}

// Reported: a second caller of the unchecked path.
func (s *Service) shortcut(ctx context.Context, req router.Request) (router.Decision, error) {
	unchecked := s.routeWithStrategyUnchecked
	return unchecked(ctx, req)
}

// Reported: a package-level function value of the unchecked path.
var uncheckedRoute = (*Service).routeWithStrategyUnchecked

// Reported: a strategy Route call outside the unchecked path.
func bypass(ctx context.Context, strategy router.Router, req router.Request) (router.Decision, error) {
	return strategy.Route(ctx, req)
}

// Reported: eligibleStrategyDecision no longer enforces candidate membership.
func eligibleStrategyDecision(decision router.Decision) router.Decision {
	return decision
}
