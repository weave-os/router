// Package routingpolicy is the Routing Policy bounded context: it decides
// which models a request may route to and authorizes the routed decision as
// an immutable execution plan. It knows nothing of HTTP, billing or provider
// clients; credential reach arrives through credentials.Broker.
package routingpolicy

import (
	"context"
	"errors"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/router/policy"
)

// ErrNoPlanResolver reports a Policy built without a plan resolver.
var ErrNoPlanResolver = errors.New("routing policy has no plan resolver")

// Planner authorizes a decision the turn loop made as an execution plan.
type Planner interface {
	Plan(request policy.RoutedResolutionRequest) (policy.ResolvedPlan, error)
}

// Policy is the Routing Policy collaborator.
type Policy struct {
	broker                 credentials.Broker
	plans                  *policy.PlanResolver
	excludedModelsOverride map[string]struct{}
}

var _ Planner = (*Policy)(nil)

// New builds a Policy that reads credential reach from broker and resolves
// plans through plans.
func New(broker credentials.Broker, plans *policy.PlanResolver) *Policy {
	return &Policy{broker: broker, plans: plans}
}

// Plan resolves the immutable plan for a routed decision.
func (p *Policy) Plan(request policy.RoutedResolutionRequest) (policy.ResolvedPlan, error) {
	if p == nil || p.plans == nil {
		return policy.ResolvedPlan{}, ErrNoPlanResolver
	}
	return p.plans.ResolveRouted(request)
}

// Eligibility returns the provider reach of the request's credentials.
func (p *Policy) Eligibility(ctx context.Context, req credentials.EligibilityRequest) (credentials.Eligibility, error) {
	return p.broker.Eligibility(ctx, req)
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
