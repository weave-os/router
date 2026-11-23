package proxy

import (
	"context"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/router/candidates"
)

func (s *Service) globalAutomaticExcludedModels(ctx context.Context) map[string]struct{} {
	controlPlane := s.controlPlaneExcludedModels(ctx)
	overloaded := s.throttling.Overload.OpenModels(ctx, overloadOrg(ctx))
	if len(overloaded) == 0 {
		return controlPlane
	}
	return candidates.MergeExcludedModels(overloaded, controlPlane)
}

func (s *Service) controlPlaneExcludedModels(ctx context.Context) map[string]struct{} {
	exclusions := s.routing.GlobalAutomaticExclusions()
	if exclusions == nil {
		return nil
	}
	byModel := exclusions.Snapshot(ctx, s.clock.Now)
	if len(byModel) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(byModel))
	for model := range byModel {
		out[model] = struct{}{}
	}
	return out
}

func (s *Service) globalAutomaticExclusionReason(ctx context.Context, model string) (string, bool) {
	if exclusions := s.routing.GlobalAutomaticExclusions(); exclusions != nil {
		if reason, disabled := exclusions.Snapshot(ctx, s.clock.Now)[model]; disabled {
			return reason, true
		}
	}
	if _, open := s.throttling.Overload.OpenModels(ctx, overloadOrg(ctx))[model]; open {
		return dispatch.OverloadBreakerReason, true
	}
	return "", false
}

func modelSet(ids []string) map[string]struct{} { return candidates.ModelSet(ids) }
