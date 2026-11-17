package routingpolicy

import (
	"errors"

	"weave-os/router/internal/router/policy"
)

var ErrNoPlanResolver = errors.New("routing policy has no plan resolver")

type Planner interface {
	Plan(request policy.RoutedResolutionRequest) (policy.ResolvedPlan, error)
}

type Policy struct {
	plans                  *policy.PlanResolver
	excludedModelsOverride map[string]struct{}
}

var _ Planner = (*Policy)(nil)

func New(plans *policy.PlanResolver) *Policy {
	return &Policy{plans: plans}
}

func (p *Policy) Plan(request policy.RoutedResolutionRequest) (policy.ResolvedPlan, error) {
	if p == nil || p.plans == nil {
		return policy.ResolvedPlan{}, ErrNoPlanResolver
	}
	return p.plans.ResolveRouted(request)
}

func (p *Policy) WithExcludedModelsOverride(models []string) *Policy {
	out := &Policy{}
	if p != nil {
		*out = *p
	}
	out.excludedModelsOverride = ModelSet(models)
	return out
}

func (p *Policy) ExcludedModelsOverride() map[string]struct{} {
	if p == nil {
		return nil
	}
	return p.excludedModelsOverride
}
