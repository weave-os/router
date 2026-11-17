package proxy

import (
	"context"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	randomrouter "weave-os/router/internal/router/random_router"
)

func dispatchRandomRequest(s *Service, ctx context.Context, in failoverInputs) (int, error) {
	state := randomBenchmarkFrom(ctx)
	if state == nil || !state.Prepared || !state.Config.ExpiresAt.After(s.clockNow()) || state.SelectedModel == "" || in.initialDecision.Model != state.SelectedModel || !randomrouter.IncludesTarget(in.initialDecision) || in.purpose != inference.PurposeOpenAIResponses {
		return -1, randomrouter.ErrUnavailable
	}
	in.alternatives = nil
	in.subscriptionStateRequest = nil
	in.buildAlternative = nil
	in.bindings = []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}
	return dispatchOrdinary(s, ctx, in)
}
