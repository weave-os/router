package proxy

import (
	"context"
	"errors"
	"fmt"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/telemetry"
)

const blindExperimentPublicDecisionReason = "cluster_argmax"

const automaticProbeModel = "auto"

var ErrPassthroughModelUnknown = errors.New("passthrough requested model is not a known model")

type PassthroughModelUnknownError struct {
	Model                    string
	RoutingPolicyPassthrough bool
}

func (e *PassthroughModelUnknownError) Error() string {
	return fmt.Sprintf("%q is not a known model", e.Model)
}

func (e *PassthroughModelUnknownError) Unwrap() error { return ErrPassthroughModelUnknown }

func blindExperimentPassthroughActive(ctx context.Context) bool {
	state, active := auth.BlindExperimentFrom(ctx)
	return active && state.Arm == auth.BlindExperimentArmPassthrough
}

func blindExperimentModelSource(ctx context.Context, routed *routingpolicy.TurnRoute) auth.BlindExperimentModelSource {
	switch {
	case routed == nil || routed.Decision.Model == "":
		return auth.BlindExperimentModelSourceNotDispatched
	case routed.UsageBypass:
		return auth.BlindExperimentModelSourceUsageBypass
	case routingpolicy.IsUserForcedReason(routed.Decision.Reason):
		return auth.BlindExperimentModelSourceForceModel
	case routed.HardPinned:
		return auth.BlindExperimentModelSourceHardPin
	case !routed.CallerModelPassthrough:
		return auth.BlindExperimentModelSourceRouter
	case auth.ClientModelPassthroughFrom(ctx):
		return auth.BlindExperimentModelSourceClientModelPassthrough
	case auth.RoutingPassthroughFrom(ctx):
		return auth.BlindExperimentModelSourceRoutingPolicyPassthrough
	default:
		return auth.BlindExperimentModelSourceExperimentPassthrough
	}
}

type blindExperimentOutcome struct {
	source           auth.BlindExperimentModelSource
	treatmentApplied bool
	baselineModel    string
	modelChanged     *bool
}

func blindExperimentOutcomeFor(ctx context.Context, state auth.BlindExperimentState, routed *routingpolicy.TurnRoute, requestedModel, servedModel string) blindExperimentOutcome {
	source := blindExperimentModelSource(ctx, routed)
	outcome := blindExperimentOutcome{
		source:           source,
		treatmentApplied: blindExperimentTreatmentApplied(state.Arm, source),
		baselineModel:    blindExperimentBaselineModel(ctx, requestedModel),
	}
	if source != auth.BlindExperimentModelSourceNotDispatched && outcome.baselineModel != "" {
		if servedModel == "" {
			servedModel = routed.Decision.Model
		}
		modelChanged := servedModel != outcome.baselineModel
		outcome.modelChanged = &modelChanged
	}
	return outcome
}

func blindExperimentBaselineModel(ctx context.Context, requestedModel string) string {
	baseline := clientRequestedModel(ctx, requestedModel)
	if router.IsRoutingModel(baseline) {
		return ""
	}
	if _, known := catalog.ByID(baseline); !known {
		if clientDefault := clientBaselineModel(ctx); clientDefault != "" {
			return clientDefault
		}
	}
	return baseline
}

func applyBlindExperimentOutcomeAttrs(ctx context.Context, b *telemetry.AttrBuilder, routed *routingpolicy.TurnRoute, requestedModel, servedModel string) {
	state, active := auth.BlindExperimentFrom(ctx)
	if !active {
		return
	}
	outcome := blindExperimentOutcomeFor(ctx, state, routed, requestedModel, servedModel)
	b.String("routing.blind_experiment_arm", string(state.Arm)).
		String("routing.blind_experiment_assignment_source", string(state.AssignmentSource)).
		String("routing.blind_experiment_model_source", string(outcome.source)).
		Bool("routing.blind_experiment_treatment_applied", outcome.treatmentApplied)
	if outcome.baselineModel != "" {
		b.String("routing.blind_experiment_baseline_model", outcome.baselineModel)
	}
	if outcome.modelChanged != nil {
		b.Bool("routing.blind_experiment_model_changed", *outcome.modelChanged)
	}
}

func blindExperimentTreatmentApplied(arm auth.BlindExperimentArm, source auth.BlindExperimentModelSource) bool {
	if arm == auth.BlindExperimentArmPassthrough {
		return source == auth.BlindExperimentModelSourceExperimentPassthrough
	}
	return source == auth.BlindExperimentModelSourceRouter
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
	callerRoutingSourceClientModel   callerRoutingSource = "client_model"
)

func callerRoutingState(ctx context.Context) (passthrough bool, source callerRoutingSource) {
	mode := auth.RoutingPolicyFrom(ctx).Mode
	if mode == auth.RoutingPolicyPassthrough || mode == auth.RoutingPolicyAssigned {
		if auth.ClientModelPassthroughFrom(ctx) {
			return true, callerRoutingSourceClientModel
		}
		return auth.RoutingPassthroughFrom(ctx), callerRoutingSourceRoutingPolicy
	}
	state, active := auth.BlindExperimentFrom(ctx)
	if active {
		source = callerRoutingSourceExperiment
		if state.CohortExperimentID != "" {
			source = callerRoutingSourceCohort
		}
		return state.Arm == auth.BlindExperimentArmPassthrough, source
	}
	if auth.ClientModelPassthroughFrom(ctx) {
		return true, callerRoutingSourceClientModel
	}
	return false, callerRoutingSourceDefault
}

func applyCallerRoutingAttrs(ctx context.Context, b *telemetry.AttrBuilder) {
	passthrough, source := callerRoutingState(ctx)
	b.Bool("routing.caller_passthrough", passthrough).
		String("routing.caller_routing_source", string(source))
}

func policyTrainingAllowedForRequest(ctx context.Context) bool {
	trainingAllowed, _ := ctx.Value(requestcontext.PolicyTrainingAllowedContextKey{}).(bool)
	return trainingAllowed && !callerModelPassthroughActive(ctx)
}

func (s *Service) blindExperimentPassthroughDecision(ctx context.Context, req router.Request) (router.Decision, bool, error) {
	if !blindExperimentPassthroughActive(ctx) {
		return router.Decision{}, false, nil
	}
	decision, err := s.callerModelPassthroughDecision(ctx, req)
	return decision, true, err
}

func (s *Service) passthroughDecision(ctx context.Context, req router.Request) (context.Context, router.Decision, bool, error) {
	if !auth.RoutingPassthroughFrom(ctx) {
		return ctx, router.Decision{}, false, nil
	}
	decision, err := s.callerModelPassthroughDecision(ctx, req)
	if err == nil || !auth.ClientModelPassthroughFrom(ctx) {
		return ctx, decision, true, err
	}
	observability.FromContext(ctx).Info("Client-selected model cannot be served as-is; routing instead", "model", req.RequestedModel, "err", err)
	ctx = auth.WithoutClientModelPassthrough(ctx)
	if !auth.RoutingPassthroughFrom(ctx) {
		return ctx, router.Decision{}, false, nil
	}
	decision, err = s.callerModelPassthroughDecision(ctx, req)
	return ctx, decision, true, err
}

func (s *Service) callerModelPassthroughDecision(ctx context.Context, req router.Request) (router.Decision, error) {
	req.RequestedModel = clientRequestedModel(ctx, req.RequestedModel)
	experimentArm := blindExperimentPassthroughActive(ctx) && !auth.RoutingPassthroughFrom(ctx) && !auth.ClientModelPassthroughFrom(ctx)
	if experimentArm && len(req.GatewayProviders) == 0 {
		if baseline := blindExperimentBaselineModel(ctx, req.RequestedModel); baseline != "" {
			req.RequestedModel = baseline
		}
	}
	if !modelPermittedByAllowlist(ctx, req.RequestedModel) || !modelInRequestSubset(ctx, req.RequestedModel) {
		return router.Decision{}, fmt.Errorf("requested model %q is not allowed: %w", req.RequestedModel, router.ErrAllowlistEmptiesPool)
	}
	if _, excluded := req.Candidates.ExcludedModels[req.RequestedModel]; excluded {
		return router.Decision{}, fmt.Errorf("requested model %q cannot serve this request: %w", req.RequestedModel, router.ErrNoEligibleProvider)
	}
	if _, excluded := req.Candidates.SafetyExcludedModels[req.RequestedModel]; excluded {
		return router.Decision{}, fmt.Errorf("requested model %q cannot serve this request: %w", req.RequestedModel, router.ErrNoEligibleProvider)
	}
	if req.HasImages && (!catalog.AcceptsImages(req.RequestedModel) ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(req.RequestedModel, req.ImageCount, req.EnabledProviders)) {
		return router.Decision{}, fmt.Errorf("requested model %q cannot accept images: %w", req.RequestedModel, router.ErrNoEligibleProvider)
	}
	if len(req.GatewayProviders) > 0 {
		provider, found := gatewayProviderFor(req.RequestedModel, req.CustomBindings, req.GatewayProviders)
		if !found {
			provider, found = gatewayProviderFor(req.RequestedModel, credentials.AliasedBindings(credentials.ExternalKeysFromContext(ctx), true), req.GatewayProviders)
		}
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
			RoutingPolicyPassthrough: auth.RoutingPassthroughFrom(ctx) && !auth.ClientModelPassthroughFrom(ctx),
		}
	}

	availableProviders := req.EnabledProviders
	if availableProviders == nil {
		availableProviders = s.credentials.KeyedProviders
	}
	if availableProviders == nil {
		if model.PrimaryProvider() == "" {
			return router.Decision{}, fmt.Errorf("requested model %q has no available provider: %w", req.RequestedModel, router.ErrNoEligibleProvider)
		}
		return router.Decision{
			Provider: model.PrimaryProvider(),
			Model:    req.RequestedModel,
			Reason:   blindExperimentPublicDecisionReason,
		}, nil
	}

	binding, found := catalog.ResolveBindingWithCustom(req.RequestedModel, availableProviders, req.CustomBindings)
	if !found {
		return router.Decision{}, fmt.Errorf("requested model %q has no available provider: %w", req.RequestedModel, router.ErrNoEligibleProvider)
	}
	return router.Decision{
		Provider: binding.Provider,
		Model:    req.RequestedModel,
		Reason:   blindExperimentPublicDecisionReason,
	}, nil
}
