package router

import "context"

type RoutingModel string

const (
	ModelAutomatic RoutingModel = "weave-auto"
	ModelHMM       RoutingModel = "weave-hmm-20261019-0001v1"
	ModelHMMBeta   RoutingModel = "weave-hmm-beta"
	ModelQwen      RoutingModel = "weave-qwen-20261008-0001v2"
	ModelDecision  RoutingModel = "weave-decision-model-20261019-0001v1"
)

// ModelAutomaticDisplayName is the human-readable spelling of ModelAutomatic.
// Clients such as Cursor send the custom model name exactly as the user typed
// it, so the router accepts it as an alias.
const ModelAutomaticDisplayName = "Weave Auto"

const PrimaryRoutingStrategy = StrategyHMM

type RoutingModelPolicy struct {
	Strategy           Strategy
	FallsBackToPrimary bool
	UnavailableError   string
}

var routingModelPolicies = map[RoutingModel]RoutingModelPolicy{
	ModelHMM:      {Strategy: PrimaryRoutingStrategy, UnavailableError: "hmm_model_unavailable"},
	ModelHMMBeta:  {Strategy: StrategyHMMBeta, FallsBackToPrimary: true, UnavailableError: "hmm_beta_model_unavailable"},
	ModelQwen:     {Strategy: StrategyHMMQwen, FallsBackToPrimary: true, UnavailableError: "qwen_model_unavailable"},
	ModelDecision: {Strategy: StrategyDecisionModel, FallsBackToPrimary: true, UnavailableError: "decision_model_unavailable"},
}

func RoutingModelPolicyFor(model RoutingModel) (RoutingModelPolicy, bool) {
	selection, found := routingModelPolicies[model]
	return selection, found
}

// CanonicalRoutingModel maps ModelAutomaticDisplayName to ModelAutomatic and
// returns every other value unchanged.
func CanonicalRoutingModel(model string) RoutingModel {
	if model == ModelAutomaticDisplayName {
		return ModelAutomatic
	}
	return RoutingModel(model)
}

func IsRoutingModel(model string) bool {
	canonical := CanonicalRoutingModel(model)
	if canonical == ModelAutomatic {
		return true
	}
	_, found := routingModelPolicies[canonical]
	return found
}

type routingModelContextKey struct{}

func WithRoutingModel(ctx context.Context, model RoutingModel) context.Context {
	return context.WithValue(ctx, routingModelContextKey{}, model)
}

func RoutingModelFromContext(ctx context.Context) (RoutingModel, bool) {
	model, ok := ctx.Value(routingModelContextKey{}).(RoutingModel)
	return model, ok
}
