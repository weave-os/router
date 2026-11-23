package routingpolicy

import (
	"context"
	"errors"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/routingpolicy/overrides"
)

var ErrNoPlanResolver = errors.New("routing policy has no plan resolver")

type Planner interface {
	Plan(request policy.RoutedResolutionRequest) (policy.ResolvedPlan, error)
}

type Policy struct {
	plans                  *policy.PlanResolver
	excludedModelsOverride map[string]struct{}
	deadline               PolicyDeadline
	availableModels        map[string]struct{}
	defaultBaselineModel   string
	hardPins               overrides.HardPins
	automaticExclusions    *GlobalAutomaticExclusionCache
	prefixTrimFreeSwitch   bool
	effortEscalation       bool
}

type PolicyDeadline struct {
	Fallback     bool
	DefaultModel string
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
	out.excludedModelsOverride = candidates.ModelSet(models)
	return out
}

func (p *Policy) ExcludedModelsOverride() map[string]struct{} {
	if p == nil {
		return nil
	}
	return p.excludedModelsOverride
}

func (p *Policy) Deadline() PolicyDeadline {
	if p == nil {
		return PolicyDeadline{}
	}
	return p.deadline
}

func (p *Policy) WithDeadline(deadline PolicyDeadline) *Policy {
	out := &Policy{}
	if p != nil {
		*out = *p
	}
	out.deadline = deadline
	return out
}

func (p *Policy) WithPlansFrom(src *Policy) *Policy {
	out := p.clone()
	out.plans = nil
	if src != nil {
		out.plans = src.plans
	}
	return out
}

func (p *Policy) AvailableModels() map[string]struct{} {
	if p == nil {
		return nil
	}
	return p.availableModels
}

func (p *Policy) WithAvailableModels(models map[string]struct{}) *Policy {
	out := p.clone()
	out.availableModels = nil
	if models != nil {
		out.availableModels = make(map[string]struct{}, len(models))
		for m := range models {
			out.availableModels[m] = struct{}{}
		}
	}
	return out
}

func (p *Policy) DefaultBaselineModel() string {
	if p == nil {
		return ""
	}
	return p.defaultBaselineModel
}

func (p *Policy) WithDefaultBaselineModel(model string) *Policy {
	out := p.clone()
	out.defaultBaselineModel = model
	return out
}

func (p *Policy) HardPins() overrides.HardPins {
	if p == nil {
		return overrides.HardPins{}
	}
	return p.hardPins
}

func (p *Policy) WithHardPins(hardPins overrides.HardPins) *Policy {
	out := p.clone()
	out.hardPins = hardPins
	return out
}

func (p *Policy) GlobalAutomaticExclusions() *GlobalAutomaticExclusionCache {
	if p == nil {
		return nil
	}
	return p.automaticExclusions
}

func (p *Policy) WithGlobalAutomaticExclusions(store candidates.GlobalAutomaticExclusionStore) *Policy {
	out := p.clone()
	out.automaticExclusions = nil
	if store != nil {
		out.automaticExclusions = NewGlobalAutomaticExclusionCache(store)
	}
	return out
}

func (p *Policy) WithPrefixTrimFreeSwitch(enabled bool) *Policy {
	out := p.clone()
	out.prefixTrimFreeSwitch = enabled
	return out
}

func (p *Policy) ResolvePrefixTrimFreeSwitch(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPrefixTrimFreeSwitch, p != nil && p.prefixTrimFreeSwitch)
}

func (p *Policy) WithEffortEscalation(enabled bool) *Policy {
	out := p.clone()
	out.effortEscalation = enabled
	return out
}

func (p *Policy) ResolveEffortEscalation(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyEffortEscalation, p != nil && p.effortEscalation)
}

func (p *Policy) clone() *Policy {
	out := &Policy{}
	if p != nil {
		*out = *p
	}
	return out
}
