package router

import "context"

// RoutingModel names an automatic router instead of an upstream completion model.
type RoutingModel string

const (
	ModelAutomatic RoutingModel = "weave-auto"
	ModelHMM       RoutingModel = "weave-hmm-20261019-0001v1"
	ModelHMMBeta   RoutingModel = "weave-hmm-beta"
	ModelQwen      RoutingModel = "weave-qwen-20261008-0001v2"
	ModelDecision  RoutingModel = "weave-decision-model-20261019-0001v1"
)

// PrimaryRoutingStrategy is the main HMM classifier every managed release serves.
const PrimaryRoutingStrategy = StrategyHMM

// RoutingModelPolicy binds an explicit routing model to the strategy serving it.
type RoutingModelPolicy struct {
	Strategy Strategy
	// FallsBackToPrimary serves PrimaryRoutingStrategy when the variant is absent,
	// so releases can add and drop experiments without breaking selections.
	FallsBackToPrimary bool
	UnavailableError   string
}

var routingModelPolicies = map[RoutingModel]RoutingModelPolicy{
	ModelHMM:      {Strategy: PrimaryRoutingStrategy, UnavailableError: "hmm_model_unavailable"},
	ModelHMMBeta:  {Strategy: StrategyHMMBeta, FallsBackToPrimary: true, UnavailableError: "hmm_beta_model_unavailable"},
	ModelQwen:     {Strategy: StrategyHMMQwen, FallsBackToPrimary: true, UnavailableError: "qwen_model_unavailable"},
	ModelDecision: {Strategy: StrategyDecisionModel, FallsBackToPrimary: true, UnavailableError: "decision_model_unavailable"},
}

// RoutingModelPolicyFor returns the policy for an explicit non-automatic routing model.
func RoutingModelPolicyFor(model RoutingModel) (RoutingModelPolicy, bool) {
	selection, found := routingModelPolicies[model]
	return selection, found
}

// IsRoutingModel reports whether model selects an automatic routing policy.
func IsRoutingModel(model string) bool {
	if RoutingModel(model) == ModelAutomatic {
		return true
	}
	_, found := routingModelPolicies[RoutingModel(model)]
	return found
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
