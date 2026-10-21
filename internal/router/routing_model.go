package router

import "context"

// RoutingModel names an automatic router instead of an upstream completion model.
type RoutingModel string

const (
	ModelAutomatic RoutingModel = "weave-auto"
	ModelHMM       RoutingModel = "weave-hmm-20261019-0001v1"
	ModelQwen      RoutingModel = "weave-qwen-20261008-0001v2"
	ModelDecision  RoutingModel = "weave-decision-model-20261019-0001v1"
)

// IsRoutingModel reports whether model selects an automatic routing policy.
func IsRoutingModel(model string) bool {
	switch RoutingModel(model) {
	case ModelAutomatic, ModelHMM, ModelQwen, ModelDecision:
		return true
	default:
		return false
	}
}

type routingModelContextKey struct{}

// WithRoutingModel records explicit selection so legacy preferences cannot override it.
func WithRoutingModel(ctx context.Context, model RoutingModel) context.Context {
	return context.WithValue(ctx, routingModelContextKey{}, model)
}

// RoutingModelFromContext returns the routing model explicitly selected by the client.
func RoutingModelFromContext(ctx context.Context) (RoutingModel, bool) {
	model, ok := ctx.Value(routingModelContextKey{}).(RoutingModel)
	return model, ok
}
