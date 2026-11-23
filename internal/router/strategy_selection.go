package router

import "strings"

const DefaultStrategy = StrategyHMMEmbedding

type StrategyPolicy struct {
	defaultStrategy        Strategy
	managedDefaultStrategy Strategy
	allowed                map[Strategy]struct{}
	live                   func(Strategy) bool
}

type StrategyInputs struct {
	Persisted        Strategy
	Managed          bool
	PlanProfile      bool
	PlanStrategy     Strategy
	Header           string
	HeaderAuthorized bool
}

type StrategyOutcome uint8

const (
	StrategyResolved StrategyOutcome = iota
	StrategyPlanUnavailable
	StrategyUnresolvable
)

type StrategyHeaderOutcome uint8

const (
	StrategyHeaderAbsent StrategyHeaderOutcome = iota
	StrategyHeaderRetiredBeta
	StrategyHeaderUnauthorized
	StrategyHeaderUnselectable
	StrategyHeaderApplied
)

type StrategyDecision struct {
	Strategy        Strategy
	Outcome         StrategyOutcome
	HeaderOutcome   StrategyHeaderOutcome
	RequestedHeader string
}

func NewStrategyPolicy(defaultStrategy Strategy, live func(Strategy) bool, available ...Strategy) StrategyPolicy {
	if len(available) == 0 {
		available = []Strategy{StrategyHMM, StrategyHMMEmbedding}
	}
	allowed := make(map[Strategy]struct{}, len(available))
	for _, strategy := range available {
		allowed[strategy] = struct{}{}
	}
	return StrategyPolicy{
		defaultStrategy:        NormalizeDefaultStrategy(defaultStrategy, available...),
		managedDefaultStrategy: defaultStrategy,
		allowed:                allowed,
		live:                   live,
	}
}

func (p StrategyPolicy) Selectable(strategy Strategy) bool {
	if ClassifiesThreads(strategy) || !strategyAllowed(strategy, p.allowed) {
		return false
	}
	return !IsBetaStrategy(strategy) || (p.live != nil && p.live(strategy))
}

func (p StrategyPolicy) Resolve(in StrategyInputs) StrategyDecision {
	if in.PlanProfile {
		if !p.Selectable(in.PlanStrategy) {
			return StrategyDecision{Outcome: StrategyPlanUnavailable}
		}
		return StrategyDecision{Strategy: in.PlanStrategy}
	}
	strategy := NormalizeStrategy(string(in.Persisted))
	switch {
	case in.Managed && (IsBetaStrategy(strategy) || strategy == ""):
		strategy = p.managedDefaultStrategy
	case strategy == "":
		strategy = p.defaultStrategy
	}
	if !p.Selectable(strategy) {
		return StrategyDecision{Strategy: strategy, Outcome: StrategyUnresolvable}
	}
	return p.applyHeader(StrategyDecision{Strategy: strategy, RequestedHeader: string(NormalizeStrategy(in.Header))}, in)
}

func (p StrategyPolicy) applyHeader(decision StrategyDecision, in StrategyInputs) StrategyDecision {
	if decision.RequestedHeader == "" {
		return decision
	}
	requested := Strategy(decision.RequestedHeader)
	switch {
	case in.Managed && IsBetaStrategy(requested):
		decision.HeaderOutcome = StrategyHeaderRetiredBeta
	case !in.HeaderAuthorized:
		decision.HeaderOutcome = StrategyHeaderUnauthorized
	case !p.Selectable(requested):
		decision.HeaderOutcome = StrategyHeaderUnselectable
	default:
		decision.Strategy, decision.HeaderOutcome = requested, StrategyHeaderApplied
	}
	return decision
}

func NormalizeStrategy(raw string) Strategy {
	return Strategy(strings.ToLower(strings.TrimSpace(raw)))
}

func SelectableAsDefault(strategy Strategy) bool {
	return !IsBetaStrategy(strategy) && !ClassifiesThreads(strategy)
}

func NormalizeDefaultStrategy(defaultStrategy Strategy, available ...Strategy) Strategy {
	allowed := make(map[Strategy]struct{}, len(available))
	for _, strategy := range available {
		if !SelectableAsDefault(strategy) {
			continue
		}
		allowed[strategy] = struct{}{}
	}
	if !strategyAllowed(defaultStrategy, allowed) {
		return ""
	}
	return defaultStrategy
}

func strategyAllowed(strategy Strategy, allowed map[Strategy]struct{}) bool {
	if strategy == "" {
		return false
	}
	_, ok := allowed[strategy]
	return ok
}
