package router

import "strings"

type RoutingModel string

const (
	ModelAutomatic     RoutingModel = "weave-auto"
	ModelAutoEfficient RoutingModel = "weave-auto-efficient"
	ModelAutoBeta      RoutingModel = "weave-auto-beta"
	ModelHMM           RoutingModel = "weave-hmm-20261019-0001v1"
	ModelHMMBeta       RoutingModel = "weave-hmm-beta"
	ModelQwen          RoutingModel = "weave-qwen-20261008-0001v2"
	ModelDecision      RoutingModel = "weave-decision-model-20261019-0001v1"
)

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

func CanonicalRoutingModel(model string) RoutingModel {
	if model == ModelAutomaticDisplayName || IsAutoBetaRoutingModel(model) {
		return ModelAutomatic
	}
	return RoutingModel(model)
}

func IsAutoBetaRoutingModel(model string) bool {
	return strings.EqualFold(model, string(ModelAutoBeta))
}

func UsesAssignedProfile(model RoutingModel) bool {
	return model == ModelAutoEfficient
}

func IsRoutingModel(model string) bool {
	canonical := CanonicalRoutingModel(model)
	if canonical == ModelAutomatic || UsesAssignedProfile(canonical) {
		return true
	}
	_, found := routingModelPolicies[canonical]
	return found
}
