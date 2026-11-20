package selection

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
)

type DistributionRosters interface {
	DistributionRoster(context.Context, router.Strategy) (*rosterdata.Roster, error)
}

type DistributionSource struct {
	Rosters DistributionRosters
}

func (s DistributionSource) DistributionRoster(ctx context.Context, strategy router.Strategy) (router.DistributionRoster, error) {
	roster, err := s.Rosters.DistributionRoster(ctx, strategy)
	if err != nil {
		return nil, err
	}
	return distributionRoster{roster: roster}, nil
}

type distributionRoster struct {
	roster *rosterdata.Roster
}

func (d distributionRoster) RoutingDistribution(gridN int, excludedModels, excludedProviders map[string]struct{}) ([]router.DistributionPoint, error) {
	return RoutingDistribution(d.roster, gridN, excludedModels, excludedProviders)
}
