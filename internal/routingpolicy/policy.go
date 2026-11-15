// Package routingpolicy is the Routing Policy bounded context: it decides
// which models a request may route to and authorizes the routed decision as
// an immutable execution plan. It knows nothing of HTTP, billing or provider
// clients; credential reach arrives as a credentials.Eligibility.
package routingpolicy

import (
	"errors"

	"weave-os/router/internal/router/policy"
)

// ErrNoPlanResolver reports a Policy built without a plan resolver.
var ErrNoPlanResolver = errors.New("routing policy has no plan resolver")

// Planner authorizes a decision the turn loop made as an execution plan.
type Planner interface {
	Plan(request policy.RoutedResolutionRequest) (policy.ResolvedPlan, error)
}

// Policy owns request-time model exclusions and plan authorization.
type Policy struct {
	plans                  *policy.PlanResolver
	excludedModelsOverride map[string]struct{}
}

var _ Planner = (*Policy)(nil)

// New builds a Policy; a nil plans makes Plan fail closed with
// ErrNoPlanResolver.
func New(plans *policy.PlanResolver) *Policy {
	return &Policy{plans: plans}
}

// Plan fails closed with ErrNoPlanResolver when no plan resolver is wired.
func (p *Policy) Plan(request policy.RoutedResolutionRequest) (policy.ResolvedPlan, error) {
	if p == nil || p.plans == nil {
		return policy.ResolvedPlan{}, ErrNoPlanResolver
	}
	return p.plans.ResolveRouted(request)
}

// WithExcludedModelsOverride returns a copy of p whose per-request model
// exclusions are replaced by models; an empty list clears the override.
func (p *Policy) WithExcludedModelsOverride(models []string) *Policy {
	out := &Policy{}
	if p != nil {
		*out = *p
	}
	out.excludedModelsOverride = ModelSet(models)
	return out
}

// ExcludedModelsOverride returns the deployment-wide exclusion set, or nil
// when the per-installation list applies.
func (p *Policy) ExcludedModelsOverride() map[string]struct{} {
	if p == nil {
		return nil
	}
	return p.excludedModelsOverride
}
