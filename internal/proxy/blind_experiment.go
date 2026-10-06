package proxy

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/policy"
)

const blindExperimentPublicDecisionReason = "cluster_argmax"

const automaticProbeModel = "auto"

// ErrPassthroughModelUnknown is returned when passthrough must serve the
// requested model verbatim but it names no catalog model (e.g. a harness's
// "auto" placeholder), so it isn't misreported as missing provider keys.
var ErrPassthroughModelUnknown = errors.New("passthrough requested model is not a known model")

// PassthroughModelUnknownError carries the unresolvable value so the
// dispatch classifier can quote it back. RoutingPolicyPassthrough is false
// for blind-experiment passthrough, whose arm must stay undisclosed and which
// no admin routing change would fix.
type PassthroughModelUnknownError struct {
	Model                    string
	RoutingPolicyPassthrough bool
}

// Error implements error.
func (e *PassthroughModelUnknownError) Error() string {
	return fmt.Sprintf("%q is not a known model", e.Model)
}

// Unwrap ties the typed error to ErrPassthroughModelUnknown for errors.Is.
func (e *PassthroughModelUnknownError) Unwrap() error { return ErrPassthroughModelUnknown }

func blindExperimentPassthroughActive(ctx context.Context) bool {
	state, active := auth.BlindExperimentFrom(ctx)
	return active && state.Arm == auth.BlindExperimentArmPassthrough
}

func callerModelPassthroughActive(ctx context.Context) bool {
	return auth.RoutingPassthroughFrom(ctx) || blindExperimentPassthroughActive(ctx)
}

type callerRoutingSource string

const (
	callerRoutingSourceDefault       callerRoutingSource = "default"
	callerRoutingSourceRoutingPolicy callerRoutingSource = "routing_policy"
	callerRoutingSourceExperiment    callerRoutingSource = "experiment"
	callerRoutingSourceCohort        callerRoutingSource = "cohort"
)

// callerRoutingState mirrors withUserSettings precedence: an explicit routing
// policy suppresses the blind experiment, which otherwise decides the arm.
func callerRoutingState(ctx context.Context) (passthrough bool, source callerRoutingSource) {
	mode := auth.RoutingPolicyFrom(ctx).Mode
	if mode == auth.RoutingPolicyPassthrough || mode == auth.RoutingPolicyAssigned {
		return auth.RoutingPassthroughFrom(ctx), callerRoutingSourceRoutingPolicy
	}
	state, active := auth.BlindExperimentFrom(ctx)
	if !active {
		return false, callerRoutingSourceDefault
	}
	source = callerRoutingSourceExperiment
	if state.CohortExperimentID != "" {
		source = callerRoutingSourceCohort
	}
	return state.Arm == auth.BlindExperimentArmPassthrough, source
}

// applyCallerRoutingAttrs records the caller's assigned state, not the turn's
// outcome: force-model and policy pins still rewrite some experiment-passthrough
// turns. Spans are internal, so this does not disclose the blind arm to clients.
func applyCallerRoutingAttrs(ctx context.Context, b *otel.AttrBuilder) *otel.AttrBuilder {
	passthrough, source := callerRoutingState(ctx)
	return b.Bool("routing.caller_passthrough", passthrough).
		String("routing.caller_routing_source", string(source))
}

// policyTrainingAllowedForRequest excludes passthrough outcomes because the
// served model was selected by the caller, not by the routing policy.
func policyTrainingAllowedForRequest(ctx context.Context) bool {
	trainingAllowed, _ := ctx.Value(PolicyTrainingAllowedContextKey{}).(bool)
	return trainingAllowed && !callerModelPassthroughActive(ctx)
}

// blindExperimentPassthroughDecision resolves the requested model without
// exposing the experiment arm in the client-visible decision reason.
func (s *Service) blindExperimentPassthroughDecision(ctx context.Context, req router.Request) (router.Decision, bool, error) {
	if !blindExperimentPassthroughActive(ctx) {
		return router.Decision{}, false, nil
	}
	decision, err := s.callerModelPassthroughDecision(ctx, req)
	return decision, true, err
}

func (s *Service) callerModelPassthroughDecision(ctx context.Context, req router.Request) (router.Decision, error) {
	if !modelPermittedByAllowlist(ctx, req.RequestedModel) || !modelInRequestSubset(ctx, req.RequestedModel) {
		return router.Decision{}, fmt.Errorf("requested model %q is not allowed: %w", req.RequestedModel, cluster.ErrAllowlistEmptiesPool)
	}
	if _, excluded := req.ExcludedModels[req.RequestedModel]; excluded {
		return router.Decision{}, fmt.Errorf("requested model %q cannot serve this request: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
	}
	if _, excluded := req.SafetyExcludedModels[req.RequestedModel]; excluded {
		return router.Decision{}, fmt.Errorf("requested model %q cannot serve this request: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
	}
	if req.HasImages && !catalog.AcceptsImages(req.RequestedModel) {
		return router.Decision{}, fmt.Errorf("requested model %q cannot accept images: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
	}
	if len(req.GatewayProviders) > 0 {
		provider, found := gatewayProviderFor(req.RequestedModel, req.CustomBindings, req.GatewayProviders)
		if !found {
			return router.Decision{}, fmt.Errorf("requested model %q has no available gateway alias: %w", req.RequestedModel, policy.ErrGatewayServesNoDeployedModel)
		}
		return router.Decision{
			Provider: provider,
			Model:    req.RequestedModel,
			Reason:   blindExperimentPublicDecisionReason,
		}, nil
	}

	model, found := catalog.ByID(req.RequestedModel)
	if !found {
		return router.Decision{}, &PassthroughModelUnknownError{
			Model:                    req.RequestedModel,
			RoutingPolicyPassthrough: auth.RoutingPassthroughFrom(ctx),
		}
	}

	availableProviders := req.EnabledProviders
	if availableProviders == nil {
		availableProviders = s.deploymentKeyedProviders
	}
	if availableProviders == nil {
		if model.PrimaryProvider() == "" {
			return router.Decision{}, fmt.Errorf("requested model %q has no available provider: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
		}
		return router.Decision{
			Provider: model.PrimaryProvider(),
			Model:    req.RequestedModel,
			Reason:   blindExperimentPublicDecisionReason,
		}, nil
	}

	binding, found := catalog.ResolveBindingWithCustom(req.RequestedModel, availableProviders, req.CustomBindings)
	if !found {
		return router.Decision{}, fmt.Errorf("requested model %q has no available provider: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
	}
	return router.Decision{
		Provider: binding.Provider,
		Model:    req.RequestedModel,
		Reason:   blindExperimentPublicDecisionReason,
	}, nil
}
