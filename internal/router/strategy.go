package router

import (
	"context"
	"errors"
)

// ErrStrategyUnavailable signals that a selected strategy has no configured
// implementation. Strategy-specific sentinels may wrap this error.
var ErrStrategyUnavailable = errors.New("router: strategy unavailable")

// Strategy identifies the routing policy selected for a request. The zero
// value ("") means the deployment default.
type Strategy string

const (
	// StrategyRL routes via the trained RL/DPO policy served by the
	// out-of-process policy sidecar. Opt-in only; never the silent default.
	StrategyRL Strategy = "rl"
	// StrategyHMM routes via an out-of-process policy sidecar. Opt-in only;
	// never the silent default.
	StrategyHMM Strategy = "hmm"
	// StrategyHMMEmbedding uses the HMM sidecar with the embedding-quality-seeded bandit prior.
	StrategyHMMEmbedding Strategy = "hmm_embedding"
	// StrategyHMMBeta selects the beta HMM classifier.
	// It is selected explicitly by a routing model ID or a legacy session toggle.
	StrategyHMMBeta Strategy = "hmm_beta"
	// StrategyHMMQwen selects Qwen through the same classifier sidecar.
	StrategyHMMQwen Strategy = "hmm_qwen"
	// StrategyDecisionModel asks the decision model judge for task and call complexity and picks models in Go.
	StrategyDecisionModel Strategy = "decision_model"
	// StrategyLLMClassifier requires an authenticated, release-bound thread handshake.
	StrategyLLMClassifier Strategy = "llm_classifier"
)

// IsHMMStrategy reports whether strategy uses the HMM policy contract and
// lifecycle semantics.
func IsHMMStrategy(strategy Strategy) bool {
	return strategy == StrategyHMM || strategy == StrategyHMMEmbedding || IsBetaStrategy(strategy)
}

// retiredClusterStrategy is the removed cluster-scorer strategy. Older
// installations and deployments may still carry it.
const retiredClusterStrategy Strategy = "cluster"

// IsRetiredStrategy reports whether strategy names a removed routing policy.
// It is never routable: persisted values follow the deployment default and a
// configured default refuses boot.
func IsRetiredStrategy(strategy Strategy) bool {
	return strategy == retiredClusterStrategy
}

// IsBetaStrategy identifies explicit session classifier overrides.
func IsBetaStrategy(strategy Strategy) bool {
	return strategy == StrategyHMMBeta || strategy == StrategyHMMQwen
}

type strategyContextKey struct{}

// WithStrategy stashes a routing-strategy override on ctx. An empty strategy
// leaves ctx unchanged so StrategyFromContext falls back to the default.
func WithStrategy(ctx context.Context, s Strategy) context.Context {
	if s == "" {
		return ctx
	}
	return context.WithValue(ctx, strategyContextKey{}, s)
}

// StrategyFromContext returns the per-request strategy override, or
// StrategyHMMEmbedding (the default policy strategy) when none was set.
func StrategyFromContext(ctx context.Context) Strategy {
	s, ok := ctx.Value(strategyContextKey{}).(Strategy)
	if !ok || s == "" {
		return StrategyHMMEmbedding
	}
	return s
}
