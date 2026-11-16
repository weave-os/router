package randomrouter

import (
	"context"
	"errors"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

var ErrUnavailable = errors.New("strict random benchmark policy unavailable")

type Draw func(int) (int, error)

type Policy struct {
	draw     Draw
	resolver *policy.Resolver
}

func New(draw Draw) *Policy {
	models := make(map[string]struct{}, 3)
	for _, model := range Models() {
		models[model] = struct{}{}
	}
	return &Policy{draw: draw, resolver: policy.NewResolver(models, map[string]struct{}{providers.ProviderOpenAI: {}}, func(model catalog.Model) string { return model.ID }, policy.ManagedProviderPolicy())}
}

func (p *Policy) Route(ctx context.Context, request router.Request) (router.Decision, error) {
	resolved := p.resolver.Resolve(request)
	if len(resolved.Candidates) != 3 {
		return router.Decision{}, ErrUnavailable
	}
	models := Models()
	for _, model := range models {
		candidate, found := resolved.ByRosterID[model]
		if !found || candidate.Provider != providers.ProviderOpenAI {
			return router.Decision{}, ErrUnavailable
		}
	}
	if p.draw == nil {
		return router.Decision{}, ErrUnavailable
	}
	index, err := p.draw(3)
	if err != nil || index < 0 || index >= 3 {
		return router.Decision{}, errors.Join(ErrUnavailable, err)
	}
	return router.Decision{Model: models[index], Provider: providers.ProviderOpenAI, Reason: "random_strict_uniform",
		Metadata: &router.RoutingMetadata{CandidateModels: models, CandidateProviders: resolved.CandidateProviders(), Strategy: string(router.StrategyRandomBenchmark), Propensity: float32(1.0 / 3.0), PolicyArtifactID: PolicyID, PolicyArtifactSHA256: PolicySHA256}}, nil
}

var _ router.Router = (*Policy)(nil)
