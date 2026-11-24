package proxy

import (
	"context"

	"weave-os/router/internal/router"
)

func routingModelBaseline(ctx context.Context, requested string) string {
	if comparison, found := router.DefaultRoutingModelComparisonModels[router.CanonicalRoutingModel(requested)]; found {
		return comparison
	}
	return clientBaselineModel(ctx)
}
