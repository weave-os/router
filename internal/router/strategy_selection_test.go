package router_test

import (
	"testing"

	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
)

func TestStrategyPolicyResolve(t *testing.T) {
	allLive := func(router.Strategy) bool { return true }
	noneLive := func(router.Strategy) bool { return false }
	stable := []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding}
	withBeta := append(append([]router.Strategy{}, stable...), router.StrategyHMMBeta)
	for _, tc := range []struct {
		name            string
		defaultStrategy router.Strategy
		live            func(router.Strategy) bool
		available       []router.Strategy
		in              router.StrategyInputs
		want            router.Strategy
		outcome         router.StrategyOutcome
		headerOutcome   router.StrategyHeaderOutcome
	}{
		{name: "no header uses deployment default", live: allLive, want: router.StrategyHMMEmbedding},
		{name: "empty available falls back to stable lanes", live: allLive, in: router.StrategyInputs{Header: "hmm", HeaderAuthorized: true}, want: router.StrategyHMM, headerOutcome: router.StrategyHeaderApplied},
		{name: "authorized hmm header applies", live: allLive, available: stable, in: router.StrategyInputs{Header: "hmm", HeaderAuthorized: true}, want: router.StrategyHMM, headerOutcome: router.StrategyHeaderApplied},
		{name: "header is lowercased and trimmed", live: allLive, available: stable, in: router.StrategyInputs{Header: "  HMM  ", HeaderAuthorized: true}, want: router.StrategyHMM, headerOutcome: router.StrategyHeaderApplied},
		{name: "retired rl header is unselectable", live: allLive, available: stable, in: router.StrategyInputs{Header: "rl", HeaderAuthorized: true}, want: router.StrategyHMMEmbedding, headerOutcome: router.StrategyHeaderUnselectable},
		{name: "cluster header is unselectable", live: allLive, available: stable, in: router.StrategyInputs{Persisted: router.StrategyHMM, Header: "cluster", HeaderAuthorized: true}, want: router.StrategyHMM, headerOutcome: router.StrategyHeaderUnselectable},
		{name: "classifier header is unselectable", live: allLive, available: []router.Strategy{router.StrategyLLMClassifier, router.StrategyHMMEmbedding}, in: router.StrategyInputs{Header: "llm_classifier", HeaderAuthorized: true}, want: router.StrategyHMMEmbedding, headerOutcome: router.StrategyHeaderUnselectable},
		{name: "persisted classifier fails closed", live: allLive, available: []router.Strategy{router.StrategyLLMClassifier, router.StrategyHMMEmbedding}, in: router.StrategyInputs{Persisted: router.StrategyLLMClassifier}, want: router.StrategyLLMClassifier, outcome: router.StrategyUnresolvable},
		{name: "unauthorized header keeps persisted", live: allLive, available: stable, in: router.StrategyInputs{Persisted: router.StrategyHMM, Header: "hmm_embedding"}, want: router.StrategyHMM, headerOutcome: router.StrategyHeaderUnauthorized},
		{name: "persisted applies without header", live: allLive, available: stable, in: router.StrategyInputs{Persisted: router.StrategyHMM}, want: router.StrategyHMM},
		{name: "persisted is normalized", live: allLive, available: stable, in: router.StrategyInputs{Persisted: " HMM "}, want: router.StrategyHMM},
		{name: "future registered strategy accepted", live: allLive, available: []router.Strategy{"future", router.StrategyHMMEmbedding}, in: router.StrategyInputs{Header: "future", HeaderAuthorized: true}, want: "future", headerOutcome: router.StrategyHeaderApplied},
		{name: "authorized beta header activates live beta", live: allLive, available: withBeta, in: router.StrategyInputs{Header: "hmm_beta", HeaderAuthorized: true}, want: router.StrategyHMMBeta, headerOutcome: router.StrategyHeaderApplied},
		{name: "beta header ignored while beta lane empty", live: noneLive, available: withBeta, in: router.StrategyInputs{Header: "hmm_beta", HeaderAuthorized: true}, want: router.StrategyHMMEmbedding, headerOutcome: router.StrategyHeaderUnselectable},
		{name: "beta unselectable without availability source", available: withBeta, in: router.StrategyInputs{Header: "hmm_beta", HeaderAuthorized: true}, want: router.StrategyHMMEmbedding, headerOutcome: router.StrategyHeaderUnselectable},
		{name: "persisted live beta activates beta", live: allLive, available: withBeta, in: router.StrategyInputs{Persisted: router.StrategyHMMBeta}, want: router.StrategyHMMBeta},
		{name: "persisted beta fails closed when unregistered", live: allLive, available: stable, in: router.StrategyInputs{Persisted: router.StrategyHMMBeta}, want: router.StrategyHMMBeta, outcome: router.StrategyUnresolvable},
		{name: "persisted beta fails closed while lane empty", live: noneLive, available: withBeta, in: router.StrategyInputs{Persisted: router.StrategyHMMBeta}, want: router.StrategyHMMBeta, outcome: router.StrategyUnresolvable},
		{name: "deployment default beta fails closed", defaultStrategy: router.StrategyHMMBeta, live: allLive, available: withBeta, want: "", outcome: router.StrategyUnresolvable},
		{name: "non-managed unavailable persisted fails closed", live: allLive, available: stable, in: router.StrategyInputs{Persisted: "rl"}, want: "rl", outcome: router.StrategyUnresolvable},
		{name: "non-managed unavailable default fails closed", defaultStrategy: router.StrategyHMM, live: allLive, available: []router.Strategy{router.StrategyHMMEmbedding}, want: "", outcome: router.StrategyUnresolvable},
		{name: "managed remaps retired beta to deployment default", live: allLive, available: withBeta, in: router.StrategyInputs{Managed: true, Persisted: router.StrategyHMMBeta}, want: router.StrategyHMMEmbedding},
		{name: "managed remaps empty persisted to raw default", defaultStrategy: router.StrategyHMM, live: allLive, available: stable, in: router.StrategyInputs{Managed: true}, want: router.StrategyHMM},
		{name: "managed remapped default unavailable fails closed", defaultStrategy: router.StrategyHMMBeta, live: noneLive, available: withBeta, in: router.StrategyInputs{Managed: true, Persisted: router.StrategyHMMBeta}, want: router.StrategyHMMBeta, outcome: router.StrategyUnresolvable},
		{name: "managed unavailable persisted fails closed", live: allLive, available: stable, in: router.StrategyInputs{Managed: true, Persisted: "rl"}, want: "rl", outcome: router.StrategyUnresolvable},
		{name: "managed retired beta header ignored", live: allLive, available: withBeta, in: router.StrategyInputs{Managed: true, Header: "hmm_beta", HeaderAuthorized: true}, want: router.StrategyHMMEmbedding, headerOutcome: router.StrategyHeaderRetiredBeta},
		{name: "managed retired beta header wins over authorization", live: allLive, available: withBeta, in: router.StrategyInputs{Managed: true, Header: "hmm_beta"}, want: router.StrategyHMMEmbedding, headerOutcome: router.StrategyHeaderRetiredBeta},
		{name: "plan profile owns strategy and skips header", live: allLive, available: stable, in: router.StrategyInputs{Managed: true, PlanProfile: true, PlanStrategy: router.StrategyHMM, Persisted: router.StrategyHMMEmbedding, Header: "hmm_embedding", HeaderAuthorized: true}, want: router.StrategyHMM},
		{name: "plan profile strategy unavailable", live: allLive, available: stable, in: router.StrategyInputs{Managed: true, PlanProfile: true, PlanStrategy: router.StrategyHMMBeta}, want: "", outcome: router.StrategyPlanUnavailable},
		{name: "plan without profile falls through to persisted", live: allLive, available: stable, in: router.StrategyInputs{Managed: true, PlanStrategy: router.StrategyHMMBeta, Persisted: router.StrategyHMM}, want: router.StrategyHMM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defaultStrategy := tc.defaultStrategy
			if defaultStrategy == "" {
				defaultStrategy = router.StrategyHMMEmbedding
			}
			got := router.NewStrategyPolicy(defaultStrategy, tc.live, tc.available...).Resolve(tc.in)
			assert.Equal(t, tc.want, got.Strategy)
			assert.Equal(t, tc.outcome, got.Outcome)
			assert.Equal(t, tc.headerOutcome, got.HeaderOutcome)
		})
	}
}

func TestSelectRoutingModel(t *testing.T) {
	servable := func(strategies ...router.Strategy) func(router.Strategy) bool {
		return func(s router.Strategy) bool {
			for _, candidate := range strategies {
				if candidate == s {
					return true
				}
			}
			return false
		}
	}
	internal := router.RoutingModelScope{Managed: true, Internal: true}
	hmmPolicy, _ := router.RoutingModelPolicyFor(router.ModelHMM)
	betaPolicy, _ := router.RoutingModelPolicyFor(router.ModelHMMBeta)
	for _, tc := range []struct {
		name     string
		current  router.StrategySelection
		model    router.RoutingModel
		scope    router.RoutingModelScope
		servable func(router.Strategy) bool
		want     router.RoutingModelChoice
	}{
		{name: "automatic keeps strategy and marks model", current: router.StrategySelection{Strategy: router.StrategyLLMClassifier}, model: router.ModelAutomatic, servable: servable(), want: router.RoutingModelChoice{Selection: router.StrategySelection{Strategy: router.StrategyLLMClassifier, RoutingModel: router.ModelAutomatic}}},
		{name: "classifier thread conflicts", current: router.StrategySelection{Strategy: router.StrategyLLMClassifier}, model: router.ModelHMM, scope: internal, servable: servable(hmmPolicy.Strategy), want: router.RoutingModelChoice{Outcome: router.RoutingModelClassifierConflict}},
		{name: "managed non-internal not permitted", model: router.ModelHMM, scope: router.RoutingModelScope{Managed: true}, servable: servable(hmmPolicy.Strategy), want: router.RoutingModelChoice{Outcome: router.RoutingModelNotPermitted}},
		{name: "qwen not permitted outside internal", model: router.ModelQwen, servable: servable(), want: router.RoutingModelChoice{Outcome: router.RoutingModelNotPermitted}},
		{name: "self-hosted named model served", model: router.ModelHMM, servable: servable(hmmPolicy.Strategy), want: router.RoutingModelChoice{Selection: router.StrategySelection{Strategy: hmmPolicy.Strategy, RoutingModel: router.ModelHMM}, PolicyStrategy: hmmPolicy.Strategy}},
		{name: "internal named model served", model: router.ModelHMMBeta, scope: internal, servable: servable(betaPolicy.Strategy), want: router.RoutingModelChoice{Selection: router.StrategySelection{Strategy: betaPolicy.Strategy, RoutingModel: router.ModelHMMBeta}, PolicyStrategy: betaPolicy.Strategy}},
		{name: "auto-efficient keeps strategy on a managed non-internal target", current: router.StrategySelection{Strategy: router.StrategyHMMEmbedding}, model: router.ModelAutoEfficient, scope: router.RoutingModelScope{Managed: true}, servable: servable(), want: router.RoutingModelChoice{Selection: router.StrategySelection{Strategy: router.StrategyHMMEmbedding, RoutingModel: router.ModelAutoEfficient}}},
		{name: "unservable without fallback", model: router.ModelHMM, servable: servable(), want: router.RoutingModelChoice{Outcome: router.RoutingModelUnservable, PolicyStrategy: hmmPolicy.Strategy, UnavailableError: hmmPolicy.UnavailableError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, router.SelectRoutingModel(tc.current, tc.model, tc.scope, tc.servable))
		})
	}
	for model, policy := range map[router.RoutingModel]router.RoutingModelPolicy{router.ModelHMMBeta: betaPolicy, router.ModelQwen: mustPolicy(t, router.ModelQwen), router.ModelDecision: mustPolicy(t, router.ModelDecision)} {
		t.Run("fallback "+string(model), func(t *testing.T) {
			got := router.SelectRoutingModel(router.StrategySelection{}, model, internal, servable(router.PrimaryRoutingStrategy))
			if !policy.FallsBackToPrimary {
				assert.Equal(t, router.RoutingModelChoice{Outcome: router.RoutingModelUnservable, PolicyStrategy: policy.Strategy, UnavailableError: policy.UnavailableError}, got)
				return
			}
			assert.Equal(t, router.RoutingModelChoice{Selection: router.StrategySelection{Strategy: router.PrimaryRoutingStrategy, RoutingModel: model}, Outcome: router.RoutingModelPrimaryFallback, PolicyStrategy: policy.Strategy}, got)
		})
	}
}

func mustPolicy(t *testing.T, model router.RoutingModel) router.RoutingModelPolicy {
	t.Helper()
	policy, ok := router.RoutingModelPolicyFor(model)
	assert.True(t, ok)
	return policy
}
