package proxy

import (
	"context"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
)

const blindExperimentPublicDecisionReason = "cluster_argmax"

// policyTrainingAllowedForRequest excludes passthrough outcomes because the
// served model was selected by the caller, not by the routing policy.
func policyTrainingAllowedForRequest(ctx context.Context) bool {
	trainingAllowed, _ := ctx.Value(PolicyTrainingAllowedContextKey{}).(bool)
	state, active := auth.BlindExperimentFrom(ctx)
	return trainingAllowed && (!active || state.Arm != auth.BlindExperimentArmPassthrough)
}

// blindExperimentPassthroughDecision resolves the requested model without
// exposing the experiment arm in the client-visible decision reason.
func (s *Service) blindExperimentPassthroughDecision(ctx context.Context, req router.Request) (router.Decision, bool, error) {
	state, active := auth.BlindExperimentFrom(ctx)
	if !active || state.Arm != auth.BlindExperimentArmPassthrough {
		return router.Decision{}, false, nil
	}
	if !modelPermittedByAllowlist(ctx, req.RequestedModel) || !modelInRequestSubset(ctx, req.RequestedModel) {
		return router.Decision{}, true, fmt.Errorf("requested model %q is not allowed: %w", req.RequestedModel, cluster.ErrAllowlistEmptiesPool)
	}
	if _, excluded := req.SafetyExcludedModels[req.RequestedModel]; excluded {
		return router.Decision{}, true, fmt.Errorf("requested model %q cannot serve this request: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
	}
	if req.HasImages && !catalog.AcceptsImages(req.RequestedModel) {
		return router.Decision{}, true, fmt.Errorf("requested model %q cannot accept images: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
	}

	availableProviders := req.EnabledProviders
	if availableProviders == nil {
		availableProviders = s.deploymentKeyedProviders
	}
	if availableProviders == nil {
		model, found := catalog.ByID(req.RequestedModel)
		if !found || model.PrimaryProvider() == "" {
			return router.Decision{}, true, fmt.Errorf("requested model %q has no available provider: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
		}
		return router.Decision{
			Provider: model.PrimaryProvider(),
			Model:    req.RequestedModel,
			Reason:   blindExperimentPublicDecisionReason,
		}, true, nil
	}

	binding, found := catalog.ResolveBindingWithCustom(req.RequestedModel, availableProviders, req.CustomBindings)
	if !found {
		return router.Decision{}, true, fmt.Errorf("requested model %q has no available provider: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
	}
	return router.Decision{
		Provider: binding.Provider,
		Model:    req.RequestedModel,
		Reason:   blindExperimentPublicDecisionReason,
	}, true, nil
}
