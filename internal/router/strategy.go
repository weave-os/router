package router

import (
	"context"
	"errors"
)

var ErrStrategyUnavailable = errors.New("router: strategy unavailable")

type Strategy string

const (
	StrategyHMM           Strategy = "hmm"
	StrategyHMMEmbedding  Strategy = "hmm_embedding"
	StrategyHMMBeta       Strategy = "hmm_beta"
	StrategyHMMQwen       Strategy = "hmm_qwen"
	StrategyDecisionModel Strategy = "decision_model"
	StrategyLLMClassifier Strategy = "llm_classifier"
)

func IsHMMStrategy(strategy Strategy) bool {
	return strategy == StrategyHMM || strategy == StrategyHMMEmbedding || IsBetaStrategy(strategy)
}

func IsBetaStrategy(strategy Strategy) bool {
	return strategy == StrategyHMMBeta || strategy == StrategyHMMQwen
}

type StrategySelection struct {
	Strategy     Strategy
	RoutingModel RoutingModel
}

func (s StrategySelection) Effective() Strategy {
	if s.Strategy == "" {
		return StrategyHMMEmbedding
	}
	return s.Strategy
}

func (s StrategySelection) RoutingModelSelected() bool {
	return s.RoutingModel != ""
}

type strategySelectionContextKey struct{}

func WithStrategySelection(ctx context.Context, selection StrategySelection) context.Context {
	return context.WithValue(ctx, strategySelectionContextKey{}, selection)
}

func StrategySelectionFromContext(ctx context.Context) StrategySelection {
	selection, _ := ctx.Value(strategySelectionContextKey{}).(StrategySelection)
	return selection
}

func WithStrategy(ctx context.Context, s Strategy) context.Context {
	if s == "" {
		return ctx
	}
	selection := StrategySelectionFromContext(ctx)
	selection.Strategy = s
	return WithStrategySelection(ctx, selection)
}

func StrategyFromContext(ctx context.Context) Strategy {
	return StrategySelectionFromContext(ctx).Effective()
}

func StrategyUnavailableError(strategy Strategy) error {
	switch strategy {
	case StrategyHMM, StrategyHMMEmbedding, StrategyHMMBeta, StrategyHMMQwen:
		return ErrHMMUnavailable
	default:
		return ErrStrategyUnavailable
	}
}
