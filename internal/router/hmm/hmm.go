// Package hmm delegates model selection to a policy sidecar.
//
// The router builds the eligible candidate set from its catalog, delegates the
// choice, and dispatches through its normal provider machinery.
package hmm

import (
	"errors"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
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

// NewForStrategy constructs an HMM adapter with a separately selectable
// strategy ID while preserving the HMM roster mapping and lifecycle behavior.
// Its candidate universe comes from the catalog and registered providers; the
// HMM sidecar owns roster membership and intersects these candidates with the
// selected cluster's arms.
func NewForStrategy(strategy router.Strategy, decider Decider, availableProviders map[string]struct{}) *Router {
	return newWithRoutingTargets(
		strategy,
		decider,
		catalog.HMMRoutingTargetSet(availableProviders),
		availableProviders,
	)
}

// newWithRoutingTargets allows focused tests to inject a fixed target set.
func newWithRoutingTargets(strategy router.Strategy, decider Decider, routingTargets, availableProviders map[string]struct{}) *Router {
	resolver := policy.NewResolver(routingTargets, availableProviders, rosterIDFor, policy.ManagedProviderPolicy())
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
	// "explore" is the retired five-class label (roster_v2, still the pinned
	// prod package); "low" is its four-class successor (roster_v4) that the
	// retired explore cluster was merged into. Both carry the same
	// tool-execution semantics, so both must be recognized during the
	// migration window where either package can be deployed.
	if group == "explore" || group == "low" {
		return true
	}
	label := strings.TrimSpace(strings.ToLower(res.PolicyLabel))
	return label == "spawn_explore" || strings.Contains(label, "tool_call")
}

var _ router.Router = (*Router)(nil)
var _ OutcomeReporter = (*Router)(nil)
