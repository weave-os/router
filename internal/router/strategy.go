package router

import (
	"context"
	"errors"
)

var ErrStrategyUnavailable = errors.New("router: strategy unavailable")

type Strategy string

const (
	StrategyRL            Strategy = "rl"
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

const retiredClusterStrategy Strategy = "cluster"

func IsRetiredStrategy(strategy Strategy) bool {
	return strategy == retiredClusterStrategy
}

func IsBetaStrategy(strategy Strategy) bool {
	return strategy == StrategyHMMBeta || strategy == StrategyHMMQwen
}

type strategyContextKey struct{}

func WithStrategy(ctx context.Context, s Strategy) context.Context {
	if s == "" {
		return ctx
	}
	return context.WithValue(ctx, strategyContextKey{}, s)
}

func StrategyFromContext(ctx context.Context) Strategy {
	s, ok := ctx.Value(strategyContextKey{}).(Strategy)
	if !ok || s == "" {
		return StrategyHMMEmbedding
	}
	return s
}
