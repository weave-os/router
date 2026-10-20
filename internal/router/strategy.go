package router

import (
	"context"
	"errors"
)

// ErrStrategyUnavailable signals that a selected strategy has no configured
// implementation. Strategy-specific sentinels may wrap this error.
var ErrStrategyUnavailable = errors.New("router: strategy unavailable")

// Strategy identifies the routing policy selected for a request. The zero
// value ("") means the deployment default (cluster).
type Strategy string

const (
	// StrategyCluster is the default cluster-scorer strategy (AvengersPro).
	StrategyCluster Strategy = "cluster"
	// StrategyRL routes via the trained RL/DPO policy served by the
	// out-of-process policy sidecar. Opt-in only; never the silent default.
	StrategyRL Strategy = "rl"
	// StrategyHMM routes via an out-of-process policy sidecar. Opt-in only;
	// never the silent default.
	StrategyHMM Strategy = "hmm"
	// StrategyHMMEmbedding uses the HMM sidecar with the embedding-quality-seeded bandit prior.
	StrategyHMMEmbedding Strategy = "hmm_embedding"
	// StrategyHMMBeta selects the beta HMM classifier.
	// It is selected only by the session-scoped /beta toggle.
	StrategyHMMBeta Strategy = "hmm_beta"
	// StrategyHMMQwen selects Qwen through the same classifier sidecar.
	StrategyHMMQwen Strategy = "hmm_qwen"
	// StrategyBandit routes via Thompson sampling over a frozen
	// ts_posterior.json (cluster×model reward posterior). Opt-in only; wired
	// when ROUTER_BANDIT_POSTERIOR_FILE is set at boot.
	StrategyBandit Strategy = "bandit"
	// StrategyLLMClassifier requires an authenticated, release-bound thread handshake.
	StrategyLLMClassifier Strategy = "llm_classifier"
)

// IsHMMStrategy reports whether strategy uses the HMM policy contract and
// lifecycle semantics.
func IsHMMStrategy(strategy Strategy) bool {
	return strategy == StrategyHMM || strategy == StrategyHMMEmbedding || IsBetaStrategy(strategy)
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
// StrategyCluster when none was set.
func StrategyFromContext(ctx context.Context) Strategy {
	s, ok := ctx.Value(strategyContextKey{}).(Strategy)
	if !ok || s == "" {
		return StrategyCluster
	}
	return s
}
