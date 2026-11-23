package router

type RoutingModelScope struct {
	Managed  bool
	Internal bool
}

type RoutingModelOutcome uint8

const (
	RoutingModelServed RoutingModelOutcome = iota
	RoutingModelPrimaryFallback
	RoutingModelClassifierConflict
	RoutingModelNotPermitted
	RoutingModelUnservable
)

type RoutingModelChoice struct {
	Selection        StrategySelection
	Outcome          RoutingModelOutcome
	PolicyStrategy   Strategy
	UnavailableError string
}

func SelectRoutingModel(current StrategySelection, model RoutingModel, scope RoutingModelScope, servable func(Strategy) bool) RoutingModelChoice {
	if model == ModelAutomatic {
		current.RoutingModel = model
		return RoutingModelChoice{Selection: current}
	}
	if ClassifiesThreads(current.Effective()) {
		return RoutingModelChoice{Outcome: RoutingModelClassifierConflict}
	}
	policy, _ := RoutingModelPolicyFor(model)
	if (scope.Managed || model == ModelQwen) && !scope.Internal {
		return RoutingModelChoice{Outcome: RoutingModelNotPermitted}
	}
	choice := RoutingModelChoice{Selection: StrategySelection{Strategy: policy.Strategy, RoutingModel: model}, PolicyStrategy: policy.Strategy}
	if servable(policy.Strategy) {
		return choice
	}
	if !policy.FallsBackToPrimary || !servable(PrimaryRoutingStrategy) {
		return RoutingModelChoice{Outcome: RoutingModelUnservable, PolicyStrategy: policy.Strategy, UnavailableError: policy.UnavailableError}
	}
	choice.Selection.Strategy, choice.Outcome = PrimaryRoutingStrategy, RoutingModelPrimaryFallback
	return choice
}

func RequiresPolicySnapshot(strategy Strategy) bool {
	return strategy != StrategyDecisionModel
}
