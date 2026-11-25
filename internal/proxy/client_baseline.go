package proxy

import (
	"context"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func clientRequestedModel(ctx context.Context, requested string) string {
	if !router.IsRoutingModel(requested) {
		return requested
	}
	if router.CanonicalRoutingModel(requested) == router.ModelAutoEfficient {
		return router.AutoEfficientComparisonModel
	}
	if baseline := clientBaselineModel(ctx); baseline != "" {
		return baseline
	}
	return requested
}

func clientBaselineModel(ctx context.Context) string {
	switch requestcontext.ClientIdentityFrom(ctx).ClientApp {
	case requestcontext.ClientAppClaudeCode:
		return "claude-opus-5-5"
	case requestcontext.ClientAppCodex:
		return "gpt-6.1-sol"
	}
	return ""
}

func (s *Service) baselineFor(ctx context.Context, requested string) string {
	requested = clientRequestedModel(ctx, requested)
	if requested != "" {
		if _, ok := catalog.PrimaryPriceFor(requested); ok {
			return requested
		}
	}
	return s.routing.DefaultBaselineModel()
}
