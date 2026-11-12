package proxy

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
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
	baseline         blindExperimentBaseline
	modelChanged     *bool
}

type blindExperimentBaseline struct {
	model  string
	source auth.BlindExperimentBaselineSource
}

func blindExperimentOutcomeFor(ctx context.Context, state auth.BlindExperimentState, routed *turnLoopResult, requestedModel, servedModel string) blindExperimentOutcome {
	source := blindExperimentModelSource(ctx, routed)
	outcome := blindExperimentOutcome{
		source:           source,
		treatmentApplied: blindExperimentTreatmentApplied(state.Arm, source),
		baseline:         blindExperimentBaselineFor(ctx, requestedModel),
	}
	if source == auth.BlindExperimentModelSourceExperimentPassthrough &&
		outcome.baseline.source == auth.BlindExperimentBaselineSourcePriorInstall &&
		routed.Decision.Model != outcome.baseline.model {
		// The recorded model could not serve this request, so passthrough fell back to the client default.
		outcome.baseline = blindExperimentGuessedBaseline(ctx, requestedModel)
	}
	if source != auth.BlindExperimentModelSourceNotDispatched && outcome.baseline.model != "" {
		if servedModel == "" {
			servedModel = routed.Decision.Model
		}
		modelChanged := servedModel != outcome.baseline.model
		outcome.modelChanged = &modelChanged
	}
	return outcome
}

// blindExperimentBaselineFor is the model the client would run without
// routing: its requested model; for an automatic-routing alias the model the
// installer recorded before switching it to Weave Auto, when the installation
// can serve it; otherwise the client default.
func blindExperimentBaselineFor(ctx context.Context, requestedModel string) blindExperimentBaseline {
	if router.IsRoutingModel(requestedModel) {
		if prior := blindExperimentPriorModel(ctx); prior != "" {
			return blindExperimentBaseline{model: prior, source: auth.BlindExperimentBaselineSourcePriorInstall}
		}
	}
	return blindExperimentGuessedBaseline(ctx, requestedModel)
}

// blindExperimentGuessedBaseline ignores any recorded prior model: the
// requested model, or the client default for an automatic-routing alias or an
// id the catalog no longer serves. Empty for an automatic-routing alias from a
// client with no default.
func blindExperimentGuessedBaseline(ctx context.Context, requestedModel string) blindExperimentBaseline {
	baseline := clientRequestedModel(ctx, requestedModel)
	if router.IsRoutingModel(baseline) {
		return blindExperimentBaseline{}
	}
	if baseline != requestedModel {
		return blindExperimentBaseline{model: baseline, source: auth.BlindExperimentBaselineSourceClientDefault}
	}
	if _, known := catalog.ByID(baseline); !known {
		if clientDefault := clientBaselineModel(ctx); clientDefault != "" {
			return blindExperimentBaseline{model: clientDefault, source: auth.BlindExperimentBaselineSourceClientDefault}
		}
	}
	return blindExperimentBaseline{model: baseline, source: auth.BlindExperimentBaselineSourceRequestedModel}
}

// blindExperimentPriorModel is the installer-recorded prior model when it is a
// catalog model the caller's allowlists admit. Per-request eligibility is
// checked when passthrough tries to serve it.
func blindExperimentPriorModel(ctx context.Context) string {
	prior := ClientIdentityFrom(ctx).PriorModel
	if prior == "" {
		return ""
	}
	if _, known := catalog.ByID(prior); !known {
		return ""
	}
	if !modelPermittedByAllowlist(ctx, prior) || !modelInRequestSubset(ctx, prior) {
		return ""
	}
	return prior
}

// applyBlindExperimentOutcomeAttrs records the assigned arm and what actually
// chose the model. Spans are internal, so this does not disclose the arm to clients.
func applyBlindExperimentOutcomeAttrs(ctx context.Context, b *otel.AttrBuilder, routed *turnLoopResult, requestedModel, servedModel string) {
	state, active := auth.BlindExperimentFrom(ctx)
	if !active {
		return
	}
	outcome := blindExperimentOutcomeFor(ctx, state, routed, requestedModel, servedModel)
	b.String("routing.blind_experiment_arm", string(state.Arm)).
		String("routing.blind_experiment_assignment_source", string(state.AssignmentSource)).
		String("routing.blind_experiment_model_source", string(outcome.source)).
		Bool("routing.blind_experiment_treatment_applied", outcome.treatmentApplied)
	if outcome.baseline.model != "" {
		b.String("routing.blind_experiment_baseline_model", outcome.baseline.model).
			String("routing.blind_experiment_baseline_source", string(outcome.baseline.source))
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

// callerRoutingState mirrors withUserSettings precedence: an explicit routing
// policy suppresses the blind experiment, which otherwise decides the arm.
func callerRoutingState(ctx context.Context) (passthrough bool, source callerRoutingSource) {
	if auth.ClientModelPassthroughFrom(ctx) {
		return true, callerRoutingSourceClientModel
	}
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
	decision, err := s.passthroughModelDecision(ctx, req)
	return decision, true, err
}

// passthroughDecision serves the requested model as-is when the request must
// keep it. A client-picked model the installation cannot serve as-is routes
// instead, unless a routing policy still requires passthrough.
func (s *Service) passthroughDecision(ctx context.Context, req router.Request) (context.Context, router.Decision, bool, error) {
	if !auth.RoutingPassthroughFrom(ctx) {
		return ctx, router.Decision{}, false, nil
	}
	decision, err := s.passthroughModelDecision(ctx, req)
	if err == nil || !auth.ClientModelPassthroughFrom(ctx) {
		return ctx, decision, true, err
	}
	observability.FromContext(ctx).Info("Client-selected model cannot be served as-is; routing instead", "model", req.RequestedModel, "err", err)
	ctx = auth.WithoutClientModelPassthrough(ctx)
	if !auth.RoutingPassthroughFrom(ctx) {
		return ctx, router.Decision{}, false, nil
	}
	decision, err = s.passthroughModelDecision(ctx, req)
	return ctx, decision, true, err
}

// passthroughModelDecision serves the model the client would run without
// routing; in the passthrough arm that is the installer-recorded prior model
// when this request can use it.
func (s *Service) passthroughModelDecision(ctx context.Context, req router.Request) (router.Decision, error) {
	experimentArm := blindExperimentPassthroughActive(ctx) && !auth.RoutingPassthroughFrom(ctx) && !auth.ClientModelPassthroughFrom(ctx)
	if experimentArm && router.IsRoutingModel(req.RequestedModel) {
		if prior := blindExperimentPriorModel(ctx); prior != "" {
			priorReq := req
			priorReq.RequestedModel = prior
			decision, err := s.callerModelPassthroughDecision(ctx, priorReq)
			if err == nil {
				return decision, nil
			}
			observability.FromContext(ctx).Debug("Recorded prior model cannot serve this request; using client default", "model", prior, "err", err)
		}
	}
	// A passthrough arm or router-off policy keeps a Weave Auto user on the
	// model their client would run without Weave.
	req.RequestedModel = clientRequestedModel(ctx, req.RequestedModel)
	if experimentArm && len(req.GatewayProviders) == 0 {
		if baseline := blindExperimentGuessedBaseline(ctx, req.RequestedModel); baseline.model != "" {
			req.RequestedModel = baseline.model
		}
	}
	return s.callerModelPassthroughDecision(ctx, req)
}

// callerModelPassthroughDecision serves req.RequestedModel verbatim when the
// installation's allowlists, the request's exclusions and images, and its
// providers or gateway admit it.
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
	if req.HasImages && (!catalog.AcceptsImages(req.RequestedModel) ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(req.RequestedModel, req.ImageCount, req.EnabledProviders)) {
		return router.Decision{}, fmt.Errorf("requested model %q cannot accept images: %w", req.RequestedModel, cluster.ErrNoEligibleProvider)
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
			RoutingPolicyPassthrough: auth.RoutingPassthroughFrom(ctx) && !auth.ClientModelPassthroughFrom(ctx),
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
