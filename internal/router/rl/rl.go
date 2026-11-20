package rl

import (
	"context"
	"fmt"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

var ErrPolicyUnavailable = router.ErrRLPolicyUnavailable

type Candidate = policy.Candidate

type Query struct {
	PromptText string
	TurnIndex  int
	Candidates []Candidate
}

type Result struct {
	Model      string
	Score      float64
	ScoreLabel string
	Reason     string
	StateLabel string
}

type Decider interface {
	Decide(ctx context.Context, q Query) (Result, error)
}

type Router struct {
	decider  Decider
	resolver *policy.Resolver
}

func New(decider Decider, deployed, available map[string]struct{}) *Router {
	return &Router{
		decider:  decider,
		resolver: policy.NewResolver(deployed, available, rosterIDFor, policy.ManagedProviderPolicy()),
	}
}

func (r *Router) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	log := observability.FromContext(ctx)
	resolved := r.resolver.Resolve(req)
	if len(resolved.Candidates) == 0 {
		log.Warn("RL router: no eligible candidate for request; returning ErrPolicyUnavailable",
			"requested_model", req.RequestedModel,
			"has_tools", req.HasTools,
		)
		return router.Decision{}, fmt.Errorf("rl: no eligible candidate: %w", ErrPolicyUnavailable)
	}

	var turnIndex int
	if turn := req.Session.PolicyTurnContext; turn != nil {
		turnIndex = turn.VisibleTurnIndex
	}
	res, err := r.decider.Decide(ctx, Query{
		PromptText: req.PromptText,
		TurnIndex:  turnIndex,
		Candidates: resolved.Candidates,
	})
	if err != nil {
		log.Error("RL router: policy decide failed; returning ErrPolicyUnavailable", "err", err)
		return router.Decision{}, fmt.Errorf("rl: policy decide: %w: %w", err, ErrPolicyUnavailable)
	}

	binding, ok := resolved.ByRosterID[res.Model]
	if !ok {
		log.Error("RL router: policy returned model not in candidate set; returning ErrPolicyUnavailable",
			"returned_model", res.Model,
		)
		return router.Decision{}, fmt.Errorf("rl: policy returned unknown model %q: %w", res.Model, ErrPolicyUnavailable)
	}

	log.Info("RL router decided",
		"model", binding.CatalogID,
		"provider", binding.Provider,
		"roster_model", res.Model,
		"score", res.Score,
		"state", res.StateLabel,
		"candidate_count", len(resolved.Candidates),
	)
	return router.Decision{
		Provider: binding.Provider,
		Model:    binding.CatalogID,
		Reason:   reasonFor(res),
		Metadata: &router.RoutingMetadata{
			CandidateModels:    resolved.CandidateModels(),
			CandidateProviders: resolved.CandidateProviders(),
			ChosenScore:        float32(res.Score),
			Strategy:           string(router.StrategyRL),
			Propensity:         1,
		},
	}, nil
}

func reasonFor(res Result) string {
	label := res.ScoreLabel
	if label == "" {
		label = "score"
	}
	reason := fmt.Sprintf("rl_policy(%s=%.3g", label, res.Score)
	if res.StateLabel != "" {
		reason += "; state=" + res.StateLabel
	}
	return reason + ")"
}

var _ router.Router = (*Router)(nil)
