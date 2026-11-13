package proxy

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/telemetry"
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

func blindExperimentModelSource(ctx context.Context, routed *turnLoopResult) auth.BlindExperimentModelSource {
	switch {
	case routed == nil || routed.Decision.Model == "":
		return auth.BlindExperimentModelSourceNotDispatched
	case routed.UsageBypass:
		return auth.BlindExperimentModelSourceUsageBypass
	case isUserForcedReason(routed.Decision.Reason):
		return auth.BlindExperimentModelSourceForceModel
	case routed.HardPinned:
		return auth.BlindExperimentModelSourceHardPin
	case !routed.CallerModelPassthrough:
		return auth.BlindExperimentModelSourceRouter
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

func blindExperimentOutcomeFor(ctx context.Context, state auth.BlindExperimentState, routed *turnLoopResult, requestedModel, servedModel string) blindExperimentOutcome {
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

// blindExperimentBaselineModel is the model the client would run without
// routing: its requested model, or its default for an automatic-routing alias
// or an id the catalog no longer serves. Empty for an automatic-routing alias from a client with no default.
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

// applyBlindExperimentOutcomeAttrs records the assigned arm and what actually
// chose the model. Spans are internal, so this does not disclose the arm to clients.
func applyBlindExperimentOutcomeAttrs(ctx context.Context, b *telemetry.AttrBuilder, routed *turnLoopResult, requestedModel, servedModel string) {
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
func applyCallerRoutingAttrs(ctx context.Context, b *telemetry.AttrBuilder) *telemetry.AttrBuilder {
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

// passthroughDecision serves the requested model as-is when the installation's
// routing policy requires it.
func (s *Service) passthroughDecision(ctx context.Context, req router.Request) (router.Decision, bool, error) {
	if !auth.RoutingPassthroughFrom(ctx) {
		return router.Decision{}, false, nil
	}
	decision, err := s.callerModelPassthroughDecision(ctx, req)
	return decision, true, err
}

func (s *Service) callerModelPassthroughDecision(ctx context.Context, req router.Request) (router.Decision, error) {
	// A passthrough arm or router-off policy keeps a Weave Auto user on the
	// model their client would run without Weave.
	req.RequestedModel = clientRequestedModel(ctx, req.RequestedModel)
	experimentArm := blindExperimentPassthroughActive(ctx) && !auth.RoutingPassthroughFrom(ctx)
	if experimentArm && len(req.GatewayProviders) == 0 {
		if baseline := blindExperimentBaselineModel(ctx, req.RequestedModel); baseline != "" {
			req.RequestedModel = baseline
		}
	}
	if !modelPermittedByAllowlist(ctx, req.RequestedModel) || !modelInRequestSubset(ctx, req.RequestedModel) {
		return router.Decision{}, fmt.Errorf("requested model %q is not allowed: %w", req.RequestedModel, router.ErrAllowlistEmptiesPool)
	}
	if _, excluded := req.ExcludedModels[req.RequestedModel]; excluded {
		return router.Decision{}, fmt.Errorf("requested model %q cannot serve this request: %w", req.RequestedModel, router.ErrNoEligibleProvider)
	}
	if _, excluded := req.SafetyExcludedModels[req.RequestedModel]; excluded {
		return router.Decision{}, fmt.Errorf("requested model %q cannot serve this request: %w", req.RequestedModel, router.ErrNoEligibleProvider)
	}
	if req.HasImages && (!catalog.AcceptsImages(req.RequestedModel) ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(req.RequestedModel, req.ImageCount, req.EnabledProviders)) {
		return router.Decision{}, fmt.Errorf("requested model %q cannot accept images: %w", req.RequestedModel, router.ErrNoEligibleProvider)
	}
	if len(req.GatewayProviders) > 0 {
		provider, found := gatewayProviderFor(req.RequestedModel, req.CustomBindings, req.GatewayProviders)
		if !found {
			// Passthrough serves the client's model as-is, so aliases retired from routing still count.
			provider, found = gatewayProviderFor(req.RequestedModel, aliasedBindingsFromKeys(externalKeysFromContext(ctx), true), req.GatewayProviders)
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
			RoutingPolicyPassthrough: auth.RoutingPassthroughFrom(ctx),
		}
	}

	availableProviders := req.EnabledProviders
	if availableProviders == nil {
		availableProviders = s.deploymentKeyedProviders
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

// blindExperimentLoopStopMessage stops a looping turn without naming a model or the router.
const blindExperimentLoopStopMessage = "Stopped this turn: the last several responses made no progress. Send a follow-up message describing what's still needed.\n\n"
