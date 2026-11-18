package hmm

import (
	"errors"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/policy"
)

var ErrHMMUnavailable = errors.New("hmm: policy router unavailable")

type Candidate = policy.Candidate

type Query = policy.Query
type Result = policy.Result
type Decider = policy.Decider
type OutcomeReporter = policy.OutcomeReporter
type FeedbackReporter = policy.FeedbackReporter

type Router struct {
	*policy.SidecarRouter
	resolver *policy.Resolver
}

func New(decider Decider, availableProviders map[string]struct{}) *Router {
	return NewForStrategy(router.StrategyHMM, decider, availableProviders)
}

func NewForStrategy(strategy router.Strategy, decider Decider, availableProviders map[string]struct{}) *Router {
	return newWithRoutingTargets(
		strategy,
		decider,
		catalog.HMMRoutingTargetSet(availableProviders),
		availableProviders,
	)
}

func newWithRoutingTargets(strategy router.Strategy, decider Decider, routingTargets, availableProviders map[string]struct{}) *Router {
	resolver := policy.NewResolver(routingTargets, availableProviders, armid.ForModel, policy.ManagedProviderPolicy())
	return &Router{
		SidecarRouter: policy.NewSidecarRouter(policy.SidecarRouterConfig{
			Strategy:    strategy,
			Unavailable: ErrHMMUnavailable,
			Reason:      reasonFor,
		}, decider, resolver),
		resolver: resolver,
	}
}

func reasonFor(res Result) string {
	prefix := "hmm_policy"
	if isToolExecutionResult(res) {
		prefix = "hmm_policy:tool_execution"
	}
	if res.Reason != "" {
		return prefix + "(" + res.Reason + ")"
	}
	if res.PolicyLabel != "" {
		return prefix + "(label=" + res.PolicyLabel + ")"
	}
	return prefix
}

func isToolExecutionResult(res Result) bool {
	group := strings.TrimSpace(strings.ToLower(res.PolicyGroup))
	if group == "explore" || group == "low" {
		return true
	}
	label := strings.TrimSpace(strings.ToLower(res.PolicyLabel))
	return label == "spawn_explore" || strings.Contains(label, "tool_call")
}

var _ router.Router = (*Router)(nil)
var _ OutcomeReporter = (*Router)(nil)
