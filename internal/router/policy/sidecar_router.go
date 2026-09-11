package policy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
)

// ReasonRenderer converts policy metadata into the compact internal reason
// consumed by the existing pin/planner layer.
type ReasonRenderer func(Result) string

// SidecarRouterConfig is the small strategy-specific registration required to
// plug a versioned policy sidecar into the shared routing harness.
type SidecarRouterConfig struct {
	Strategy                 router.Strategy
	Unavailable              error
	Reason                   ReasonRenderer
	ClassifierArtifactID     string
	ClassifierArtifactSHA256 string
	SelectionPolicyReleaseID string
	SelectionPolicySHA256    string
	SelectionHeadGeneration  int64
}

// SidecarRouter is a shared adapter for out-of-process policy routers.
type SidecarRouter struct {
	config           SidecarRouterConfig
	decider          Decider
	reporter         OutcomeReporter
	feedbackReporter FeedbackReporter
	resolver         *Resolver
	armSelector      ArmSelector
	capabilitiesMu   sync.RWMutex
	capabilities     Capabilities
	capabilitiesSet  bool
}

// NewSidecarRouter constructs a reusable policy adapter. Strategy packages
// provide only a roster mapper/resolver and optional reason renderer.
func NewSidecarRouter(config SidecarRouterConfig, decider Decider, resolver *Resolver) *SidecarRouter {
	reporter, _ := decider.(OutcomeReporter)
	feedbackReporter, _ := decider.(FeedbackReporter)
	if config.Unavailable == nil {
		config.Unavailable = router.ErrStrategyUnavailable
	}
	return &SidecarRouter{
		config:           config,
		decider:          decider,
		reporter:         reporter,
		feedbackReporter: feedbackReporter,
		resolver:         resolver,
	}
}

// WithCapabilities gates optional outcome/feedback callbacks based on the sidecar's negotiated support.
func (r *SidecarRouter) WithCapabilities(capabilities Capabilities) *SidecarRouter {
	r.capabilitiesMu.Lock()
	defer r.capabilitiesMu.Unlock()

	r.capabilities = capabilities
	r.capabilitiesSet = true
	return r
}

// WithArmSelector installs a boot-time arm selector and negotiates the
// classifier-only sidecar contract; cluster overrides still take precedence.
func (r *SidecarRouter) WithArmSelector(selector ArmSelector) *SidecarRouter {
	r.armSelector = selector
	r.resolver.RouterSelectsArm()
	return r
}

// WithClassifierIdentity binds every classification response to one immutable release.
func (r *SidecarRouter) WithClassifierIdentity(artifactID, artifactSHA256 string) *SidecarRouter {
	r.config.ClassifierArtifactID = artifactID
	r.config.ClassifierArtifactSHA256 = artifactSHA256
	return r
}

// WithSelectionPolicyIdentity binds telemetry to the same immutable release
// snapshot that supplied this router's Go arm selector.
func (r *SidecarRouter) WithSelectionPolicyIdentity(releaseID, policySHA256 string, headGeneration int64) *SidecarRouter {
	r.config.SelectionPolicyReleaseID = releaseID
	r.config.SelectionPolicySHA256 = policySHA256
	r.config.SelectionHeadGeneration = headGeneration
	return r
}

// CurrentCapabilities returns the currently applied capability set.
func (r *SidecarRouter) CurrentCapabilities() Capabilities {
	r.capabilitiesMu.RLock()
	defer r.capabilitiesMu.RUnlock()
	return r.capabilities
}

func (r *SidecarRouter) ReportOutcome(ctx context.Context, payload map[string]interface{}) error {
	r.capabilitiesMu.RLock()
	enabled := !r.capabilitiesSet || r.capabilities.ReportsOutcomes
	r.capabilitiesMu.RUnlock()
	if !enabled || r.reporter == nil {
		return nil
	}
	return r.reporter.ReportOutcome(ctx, payload)
}

func (r *SidecarRouter) ReportFeedback(ctx context.Context, payload map[string]interface{}) error {
	r.capabilitiesMu.RLock()
	enabled := !r.capabilitiesSet || r.capabilities.ReportsFeedback
	r.capabilitiesMu.RUnlock()
	if !enabled || r.feedbackReporter == nil {
		return nil
	}
	return r.feedbackReporter.ReportFeedback(ctx, payload)
}

// ObserveEscalation forwards classifier-state observations to the bound revision.
func (r *SidecarRouter) ObserveEscalation(ctx context.Context, request escalation.ObserveRequest) (escalation.ObserveResponse, error) {
	observer, ok := r.decider.(escalation.Observer)
	if !ok {
		return escalation.ObserveResponse{}, errors.New("policy sidecar does not support escalation observations")
	}
	return observer.ObserveEscalation(ctx, request)
}

// PreviewRoute resolves candidates and returns all arms chosen by the sidecar's
// first nonempty ranked group without dispatching or lifecycle callbacks.
func (r *SidecarRouter) PreviewRoute(ctx context.Context, req router.Request) (PreviewResult, error) {
	strategy := r.config.Strategy
	r.capabilitiesMu.RLock()
	previewUnsupported := r.capabilitiesSet && !r.capabilities.SupportsPreview
	r.capabilitiesMu.RUnlock()
	if previewUnsupported {
		return PreviewResult{}, fmt.Errorf("%s: sidecar does not support preview: %w", strategy, r.config.Unavailable)
	}
	previewer, ok := r.decider.(PreviewDecider)
	if !ok {
		return PreviewResult{}, fmt.Errorf("%s: sidecar client has no preview contract: %w", strategy, r.config.Unavailable)
	}

	resolved := r.resolver.Resolve(req)
	requestRouteID := uuid.NewString()
	pin, pinned := router.HonouredPolicyPin(ctx)
	result, err := previewer.Preview(ctx, Query{
		ArtifactSHA256:       pin.ArtifactSHA256,
		SchemaVersion:        r.resolver.SchemaVersion(),
		Strategy:             strategy,
		ExecutionMode:        ExecutionModePreview,
		RouteID:              requestRouteID,
		OrganizationID:       req.OrganizationID,
		InstallationID:       req.InstallationID,
		ClientApp:            req.ClientApp,
		RolloutID:            req.RolloutID,
		RequestedModel:       req.RequestedModel,
		PromptText:           req.PromptText,
		ConversationMessages: req.ConversationMessages,
		AvailableTools:       req.AvailableTools,
		Tools:                req.Tools,
		FeedbackKey:          req.FeedbackKey,
		FeedbackRole:         req.FeedbackRole,
		ClientSessionID:      req.ClientSessionID,
		TurnContext:          req.PolicyTurnContext,
		EstimatedInputTokens: req.EstimatedInputTokens,
		HasTools:             req.HasTools,
		HasImages:            req.HasImages,
		RoutingIntent:        req.RoutingIntent,
		PreferredModels:      req.PreferredModels,
		RoutingKnobs:         req.RoutingKnobs,
		TrainingAllowed:      false,
		CaptureMode:          req.CaptureMode,
		DebugEnabled:         true,
		Candidates:           resolved.Candidates,
	})
	if err != nil {
		if errors.Is(err, router.ErrPolicyPinUnavailable) {
			return PreviewResult{}, fmt.Errorf("%s: sidecar preview: %w", strategy, err)
		}
		return PreviewResult{}, fmt.Errorf("%s: sidecar preview: %w: %w", strategy, err, r.config.Unavailable)
	}
	if pinned && result.PolicyArtifactSHA256 != pin.ArtifactSHA256 {
		return PreviewResult{}, fmt.Errorf("%s: sidecar served artifact %q, pin requires %q: %w", strategy, result.PolicyArtifactSHA256, pin.ArtifactSHA256, router.ErrPolicyPinUnavailable)
	}
	if pinned && result.SchemaVersion != SchemaVersionV4 && result.RosterSHA256 != pin.RosterSHA256 {
		return PreviewResult{}, fmt.Errorf("%s: sidecar served roster %q, pin requires %q: %w", strategy, result.RosterSHA256, pin.RosterSHA256, router.ErrPolicyPinUnavailable)
	}
	if result.RouteID != "" && result.RouteID != requestRouteID {
		return PreviewResult{}, fmt.Errorf("%s: preview route id mismatch: %w", strategy, r.config.Unavailable)
	}
	if err := validatePreviewResult(result, r.resolver.SchemaVersion()); err != nil {
		return PreviewResult{}, fmt.Errorf("%s: invalid preview result: %v: %w", strategy, err, r.config.Unavailable)
	}
	if err := r.validateClassifierIdentity(result.PolicyArtifactID, result.PolicyArtifactSHA256); err != nil {
		return PreviewResult{}, fmt.Errorf("%s: invalid preview classifier identity: %v: %w", strategy, err, r.config.Unavailable)
	}
	if result.SchemaVersion == SchemaVersionV4 {
		if r.armSelector == nil {
			return PreviewResult{}, fmt.Errorf("%s: Go arm selector is unavailable: %w", strategy, r.config.Unavailable)
		}
		classification := Result{
			SchemaVersion: result.SchemaVersion, RouteID: result.RouteID, PredictedLabel: result.PredictedLabel,
			ClassOrder: result.ClassOrder, ClassProbabilities: result.ClassProbabilities,
		}
		selectionInput := selectionInputFor(strategy, ExecutionModePreview, req, classification, resolved)
		selectionInput.RosterSHA256 = pin.RosterSHA256
		pick, selectErr := r.armSelector(ctx, selectionInput)
		if selectErr != nil {
			return PreviewResult{}, fmt.Errorf("%s: preview arm selection: %w: %w", strategy, selectErr, r.config.Unavailable)
		}
		if pinned && pick.RosterSHA256 != pin.RosterSHA256 {
			return PreviewResult{}, fmt.Errorf("%s: selection used roster %q, pin requires %q: %w", strategy, pick.RosterSHA256, pin.RosterSHA256, router.ErrPolicyPinUnavailable)
		}
		result.RankedFallback = pick.RankedFallback
		result.SelectedGroup = pick.Group
		for _, group := range pick.RankedFallback {
			if group.Group == pick.Group {
				result.EligibleRosterIDs = append([]string(nil), group.EligibleArms...)
				break
			}
		}
	}

	eligibleCandidates := resolved.ByRosterID
	if r.resolver.SchemaVersion() == SchemaVersionV2 {
		eligibleCandidates = resolved.ByArmID
	}
	seen := make(map[string]struct{}, len(result.EligibleRosterIDs))
	for _, rosterID := range result.EligibleRosterIDs {
		if _, duplicate := seen[rosterID]; duplicate {
			return PreviewResult{}, fmt.Errorf("%s: preview returned duplicate roster id %q: %w", strategy, rosterID, r.config.Unavailable)
		}
		seen[rosterID] = struct{}{}
		if _, offered := eligibleCandidates[rosterID]; !offered {
			return PreviewResult{}, fmt.Errorf("%s: preview returned unknown roster id %q: %w", strategy, rosterID, r.config.Unavailable)
		}
	}
	if (len(result.EligibleRosterIDs) > 0) != (result.SelectedGroup != "") {
		return PreviewResult{}, fmt.Errorf("%s: preview selected group/arms mismatch: %w", strategy, r.config.Unavailable)
	}

	result.RouteID = requestRouteID
	result.Strategy = strategy
	result.ResolverCandidates = resolved.Candidates
	result.ResolverExclusions = resolved.Diagnostics
	return result, nil
}

func validatePreviewResult(result PreviewResult, expectedSchemaVersion string) error {
	if result.SchemaVersion != expectedSchemaVersion {
		return fmt.Errorf("unsupported schema %q", result.SchemaVersion)
	}
	if result.PolicyArtifactID == "" || result.PolicyArtifactSHA256 == "" {
		return fmt.Errorf("missing frozen artifact identity")
	}
	if result.SchemaVersion != SchemaVersionV4 && result.RosterSHA256 == "" {
		return fmt.Errorf("missing frozen roster identity")
	}
	if len(result.HMMStatePath) == 0 || len(result.HMMStateProbabilities) == 0 || len(result.ClassOrder) == 0 {
		return fmt.Errorf("missing state or class order")
	}
	if result.HMMStateID < 0 || result.HMMStateID >= len(result.HMMStateProbabilities) {
		return fmt.Errorf("HMM state id is outside the posterior vector")
	}
	hmmTotal := 0.0
	for index, probability := range result.HMMStateProbabilities {
		if math.IsNaN(probability) || probability < 0 || probability > 1 {
			return fmt.Errorf("invalid HMM state probability at index %d", index)
		}
		hmmTotal += probability
	}
	if math.Abs(hmmTotal-1) > 1e-6 {
		return fmt.Errorf("HMM state probabilities sum to %.9f", hmmTotal)
	}
	for index, stateID := range result.HMMStatePath {
		if stateID < 0 || stateID >= len(result.HMMStateProbabilities) {
			return fmt.Errorf("HMM state path value at index %d is outside the posterior vector", index)
		}
	}
	if len(result.ClassProbabilities) != len(result.ClassOrder) {
		return fmt.Errorf("class probability/fallback cardinality mismatch")
	}
	seenClasses := make(map[string]struct{}, len(result.ClassOrder))
	total := 0.0
	for index, className := range result.ClassOrder {
		if className == "" {
			return fmt.Errorf("empty class at index %d", index)
		}
		if _, duplicate := seenClasses[className]; duplicate {
			return fmt.Errorf("duplicate class %q", className)
		}
		seenClasses[className] = struct{}{}
		probability, ok := result.ClassProbabilities[className]
		if !ok || math.IsNaN(probability) || probability < 0 || probability > 1 {
			return fmt.Errorf("invalid probability for class %q", className)
		}
		total += probability
	}
	if math.Abs(total-1) > 1e-6 {
		return fmt.Errorf("class probabilities sum to %.9f", total)
	}
	if result.SchemaVersion == SchemaVersionV4 {
		return nil
	}
	classRank := make(map[string]int, len(result.ClassOrder))
	for index, className := range result.ClassOrder {
		classRank[className] = index
	}
	seenFallback := make(map[string]struct{}, len(result.RankedFallback))
	for index, fallback := range result.RankedFallback {
		probability, ok := result.ClassProbabilities[fallback.Group]
		if !ok || math.Abs(fallback.Probability-probability) > 1e-9 {
			return fmt.Errorf("fallback probability mismatch for class %q", fallback.Group)
		}
		if _, duplicate := seenFallback[fallback.Group]; duplicate {
			return fmt.Errorf("duplicate fallback class %q", fallback.Group)
		}
		seenFallback[fallback.Group] = struct{}{}
		if index > 0 {
			previous := result.RankedFallback[index-1]
			if previous.Probability < fallback.Probability ||
				(previous.Probability == fallback.Probability && classRank[previous.Group] > classRank[fallback.Group]) {
				return fmt.Errorf("fallback groups are not deterministically ranked")
			}
		}
	}

	var selectedArms []string
	for _, fallback := range result.RankedFallback {
		if fallback.Group == result.SelectedGroup {
			selectedArms = fallback.EligibleArms
			break
		}
	}
	if !slices.Equal(selectedArms, result.EligibleRosterIDs) {
		return fmt.Errorf("selected fallback arms do not match eligible roster ids")
	}
	return nil
}

func (r *SidecarRouter) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	strategy := r.config.Strategy
	r.capabilitiesMu.RLock()
	capabilities := r.capabilities
	shadowUnsupported := r.capabilitiesSet && !capabilities.SupportsShadow
	r.capabilitiesMu.RUnlock()
	if req.ShadowMode && shadowUnsupported {
		return router.Decision{}, fmt.Errorf("%s: sidecar does not support shadow routing: %w", strategy, r.config.Unavailable)
	}
	executionMode := ExecutionModeServing
	if req.ShadowMode {
		executionMode = ExecutionModeShadow
		// Shadow decisions are operational comparisons, never learning events.
		req.TrainingAllowed = false
		req.DebugEnabled = false
	}
	resolved := r.resolver.Resolve(req)
	if len(resolved.Candidates) == 0 {
		observability.FromContext(ctx).Error("Policy router resolved no eligible candidate",
			append([]any{"strategy", strategy}, candidateLogFields(resolved)...)...)
		return router.Decision{}, fmt.Errorf("%s: no eligible candidate: %w: %w",
			strategy, emptyCandidateError(resolved.Diagnostics), r.config.Unavailable)
	}
	requestRouteID := uuid.NewString()
	pin, pinned := router.HonouredPolicyPin(ctx)
	if pinned && r.armSelector == nil {
		return router.Decision{}, fmt.Errorf("%s: roster pin requires router-owned arm selection: %w", strategy, router.ErrPolicyPinUnavailable)
	}
	res, err := r.decider.Decide(ctx, Query{
		ArtifactSHA256:       pin.ArtifactSHA256,
		SchemaVersion:        r.resolver.SchemaVersion(),
		Strategy:             strategy,
		ExecutionMode:        executionMode,
		RouteID:              requestRouteID,
		OrganizationID:       req.OrganizationID,
		InstallationID:       req.InstallationID,
		ClientApp:            req.ClientApp,
		RolloutID:            req.RolloutID,
		RequestedModel:       req.RequestedModel,
		PromptText:           req.PromptText,
		ConversationMessages: req.ConversationMessages,
		AvailableTools:       req.AvailableTools,
		Tools:                req.Tools,
		FeedbackKey:          req.FeedbackKey,
		FeedbackRole:         req.FeedbackRole,
		ClientSessionID:      req.ClientSessionID,
		TurnContext:          req.PolicyTurnContext,
		EstimatedInputTokens: req.EstimatedInputTokens,
		HasTools:             req.HasTools,
		HasImages:            req.HasImages,
		RoutingIntent:        req.RoutingIntent,
		PreferredModels:      req.PreferredModels,
		RoutingKnobs:         req.RoutingKnobs,
		TrainingAllowed:      req.TrainingAllowed,
		CaptureMode:          req.CaptureMode,
		DebugEnabled:         req.DebugEnabled,
		Candidates:           resolved.Candidates,
	})
	if err != nil {
		observability.FromContext(ctx).Error("Policy router sidecar decision failed",
			append([]any{"strategy", strategy, "err", err}, candidateLogFields(resolved)...)...)
		if errors.Is(err, router.ErrPolicyPinUnavailable) {
			return router.Decision{}, fmt.Errorf("%s: sidecar decide: %w", strategy, err)
		}
		return router.Decision{}, fmt.Errorf("%s: sidecar decide: %w: %w", strategy, err, r.config.Unavailable)
	}
	if pinned && res.PolicyArtifactSHA256 != pin.ArtifactSHA256 {
		return router.Decision{}, fmt.Errorf("%s: sidecar served artifact %q, pin requires %q: %w", strategy, res.PolicyArtifactSHA256, pin.ArtifactSHA256, router.ErrPolicyPinUnavailable)
	}
	servedRosterSHA256 := res.RosterVersion
	if err := r.validateClassifierIdentity(res.PolicyArtifactID, res.PolicyArtifactSHA256); err != nil {
		return router.Decision{}, fmt.Errorf("%s: invalid classifier identity: %v: %w", strategy, err, r.config.Unavailable)
	}

	overrideArmID := res.ArmID
	overrideRosterID := res.Model
	overrideReasonSuffix := ""
	// reselected records that the served arm is the router's pick rather than the
	// sidecar's, which is the only case where res.Provider legitimately names a
	// different provider than the resolved binding.
	reselected := false
	var routerArmScoresByGroup map[string]map[string]float32
	var selectionTrace SelectionTrace
	var escalationDecision *escalation.Decision
	if r.armSelector != nil {
		if res.SchemaVersion != SchemaVersionV4 {
			return router.Decision{}, fmt.Errorf("%s: sidecar reported schema %q, expected %s: %w", strategy, res.SchemaVersion, SchemaVersionV4, r.config.Unavailable)
		}
		originalSelectionInput := selectionInputFor(strategy, executionMode, req, res, resolved)
		originalSelectionInput.RosterSHA256 = pin.RosterSHA256
		selectionInput, decision, constrained := constrainEscalation(req, originalSelectionInput, resolved)
		escalationDecision = decision
		pick, selectErr := r.selectEscalationArm(ctx, req, selectionInput, resolved, constrained)
		for selectErr != nil && constrained && errors.Is(selectErr, ErrNoEligibleArm) {
			selectionInput, escalationDecision, constrained = fallbackEscalation(req, originalSelectionInput, resolved, escalationDecision)
			pick, selectErr = r.selectEscalationArm(ctx, req, selectionInput, resolved, constrained)
		}
		if selectErr != nil {
			if errors.Is(selectErr, router.ErrPolicyPinUnavailable) {
				return router.Decision{}, fmt.Errorf("%s: arm selection: %w", strategy, selectErr)
			}
			if errors.Is(selectErr, ErrNoEligibleArm) && req.ForceCluster != "" && selectionInput.ForcedGroup == req.ForceCluster {
				return router.Decision{}, &ForcedClusterUnservableError{
					Cluster: req.ForceCluster,
					Reason:  fmt.Sprintf("no model in cluster %q can serve this request", req.ForceCluster),
				}
			}
			return router.Decision{}, fmt.Errorf("%s: arm selection: %w: %w", strategy, selectErr, r.config.Unavailable)
		}
		if escalationDecision != nil {
			escalationDecision.Constrained = constrained
		}
		overrideArmID = indexCandidates(resolved).rosterToArm[pick.Arm]
		overrideRosterID = pick.Arm
		reselected = true
		res.PolicyGroup = pick.Group
		res.RankedFallback = pick.RankedFallback
		routerArmScoresByGroup = pick.ArmScoresByGroup
		selectionTrace = pick.Trace
		if pinned {
			if pick.RosterSHA256 != pin.RosterSHA256 {
				return router.Decision{}, fmt.Errorf("%s: selection used roster %q, pin requires %q: %w", strategy, pick.RosterSHA256, pin.RosterSHA256, router.ErrPolicyPinUnavailable)
			}
			servedRosterSHA256 = pick.RosterSHA256
		}
	}

	// Per-key cluster allowlist enforcement. ranked_fallback presence in the /route
	// response is proof the sidecar supports it — boot-time capabilities can be
	// stale after an upgrade. Missing ranked_fallback → fail open.
	switch {
	case req.ForceCluster != "":
		// Returned unwrapped: the caller's dispatch classifier matches the typed
		// error to a 400, and burying it under the strategy's unavailable sentinel
		// would report a bad header as a sidecar outage.
		outcome, err := ApplyClusterArmOverridesRequireMatch(req.ClusterArmOverrides, res.RankedFallback, resolved, overrideRosterID, req.ForceCluster)
		if err != nil {
			return router.Decision{}, err
		}
		previousRosterID := overrideRosterID
		_, hasExplicitOverride := req.ClusterArmOverrides[req.ForceCluster]
		selectedForcedGroupWithPreference := r.armSelector != nil && !hasExplicitOverride && res.PolicyGroup == outcome.Group
		if !selectedForcedGroupWithPreference {
			overrideArmID = outcome.ArmID
			overrideRosterID = outcome.RosterID
		}
		// Annotated even when the forced cluster is the one already selected,
		// so telemetry can tell a constrained turn from a free one.
		overrideReasonSuffix = ":force_cluster"
		reselected = reselected || outcome.Changed || selectedForcedGroupWithPreference
		observability.FromContext(ctx).Info("Forced cluster applied",
			"strategy", strategy,
			"group", outcome.Group,
			"previous_arm", previousRosterID,
			"forced_arm", overrideRosterID,
		)
		res.PolicyGroup = outcome.Group
	case len(req.ClusterArmOverrides) > 0 && len(res.RankedFallback) > 0 && !(escalationDecision != nil && escalationDecision.Constrained):
		outcome := ApplyClusterArmOverrides(req.ClusterArmOverrides, res.RankedFallback, resolved, overrideRosterID)
		// Only a configured allowlist supersedes the router's own selection; an
		// unconstrained group walk would just re-derive the same ranked order.
		if outcome.Applied && outcome.RosterID != "" && (outcome.Constrained || r.armSelector == nil) {
			previousRosterID := overrideRosterID
			// Use the resolved arm ID: on arm-enumerating resolvers a roster ID can
			// be ambiguous (shared across providers) and absent from ByRosterID.
			overrideArmID = outcome.ArmID
			overrideRosterID = outcome.RosterID
			res.PolicyGroup = outcome.Group
			if outcome.Changed {
				overrideReasonSuffix = ":cluster_override"
				reselected = true
				observability.FromContext(ctx).Info("Cluster allowlist override applied",
					"strategy", strategy,
					"group", outcome.Group,
					"previous_arm", previousRosterID,
					"override_arm", outcome.RosterID,
				)
			}
		}
	}

	if escalationDecision != nil {
		escalationDecision.Effective = escalation.Group(res.PolicyGroup)
	}
	selectedRosterArmID := overrideArmID
	if selectedRosterArmID == "" {
		selectedRosterArmID = overrideRosterID
	}
	if routerArmScoresByGroup != nil {
		res.ArmScores = routerArmScoresByGroup[res.PolicyGroup]
	}
	if selectionTrace.SelectedArm != "" {
		selectionTrace.SelectedGroup = res.PolicyGroup
		selectionTrace.SelectedArm = overrideRosterID
		selectionTrace.OverrideReason = strings.TrimPrefix(overrideReasonSuffix, ":")
		selectionTrace.ResolverExclusions = make([]router.SelectionExclusion, 0, len(resolved.Diagnostics))
		for _, diagnostic := range resolved.Diagnostics {
			selectionTrace.ResolverExclusions = append(selectionTrace.ResolverExclusions, router.SelectionExclusion{
				CatalogID: diagnostic.CatalogID,
				RosterID:  diagnostic.RosterID,
				Reason:    string(diagnostic.Reason),
			})
		}
	}

	binding, ok := resolved.BindingForSelection(overrideArmID, overrideRosterID)
	if !ok {
		return router.Decision{}, fmt.Errorf("%s: sidecar returned unknown arm %q or model %q: %w", strategy, overrideArmID, overrideRosterID, r.config.Unavailable)
	}
	if res.Provider != "" && !reselected && res.Provider != binding.Provider {
		return router.Decision{}, fmt.Errorf("%s: sidecar returned provider %q for %q, expected %q: %w", strategy, res.Provider, res.Model, binding.Provider, r.config.Unavailable)
	}

	propensity := float32(res.Propensity)
	if propensity <= 0 {
		propensity = 1
	}
	routeID := res.RouteID
	if routeID == "" {
		routeID = requestRouteID
	}
	debugRef := ""
	if req.DebugEnabled {
		debugRef = res.DebugRef
	}
	reason := string(strategy) + "_policy"
	if r.config.Reason != nil {
		reason = r.config.Reason(res)
	} else if res.Reason != "" {
		reason += "(" + res.Reason + ")"
	}
	if selectionTrace.SelectedArm != "" {
		reason = fmt.Sprintf("%s(group=%s,arm=%s,fallback_depth=%d)", reason, selectionTrace.SelectedGroup, selectionTrace.SelectedArm, selectionTrace.FallbackDepth)
	}
	reason += overrideReasonSuffix

	// The sidecar builds DisplayMarker from its own pre-override pick; once the
	// router reselects a different arm, that string names the wrong model.
	// Drop it so routingMarkerFor() falls through to the generic path, which
	// renders the actually-served binding.CatalogID instead.
	displayMarker := res.DisplayMarker
	if reselected {
		displayMarker = ""
	}
	selectionPolicyReleaseID := r.config.SelectionPolicyReleaseID
	if selectionPolicyReleaseID == "" {
		selectionPolicyReleaseID = res.PolicyArtifactID
	}
	selectionPolicySHA256 := r.config.SelectionPolicySHA256
	if selectionPolicySHA256 == "" {
		selectionPolicySHA256 = res.PolicyArtifactSHA256
	}
	rosterVersion := res.RosterVersion
	if r.config.SelectionPolicySHA256 != "" {
		rosterVersion = r.config.SelectionPolicySHA256
	}
	if pinned {
		selectionPolicySHA256 = pin.ArtifactSHA256
		rosterVersion = servedRosterSHA256
	}
	var replayTrace *router.SelectionTrace
	if selectionTrace.SelectedArm != "" {
		traceCopy := selectionTrace
		replayTrace = &traceCopy
	}

	observability.FromContext(ctx).Info("Policy router decided",
		"strategy", strategy,
		"execution_mode", executionMode,
		"route_id", routeID,
		"model", binding.CatalogID,
		"provider", binding.Provider,
		"arm_id", binding.ArmID,
		"roster_model", overrideRosterID,
		"score", res.Score,
	)
	return router.Decision{
		Provider: binding.Provider,
		Model:    binding.CatalogID,
		Effort:   binding.Effort,
		Reason:   reason,
		Metadata: &router.RoutingMetadata{
			Escalation:                    escalationDecision,
			CandidateModels:               resolved.CandidateModels(),
			CandidateProviders:            resolved.CandidateProviders(),
			CandidateScores:               resolved.CatalogCandidateScores(res.CandidateScores),
			CandidateArmProviders:         resolved.CandidateArmProviders(),
			CandidateArmScores:            resolved.ArmCandidateScores(res.CandidateScores),
			ChosenScore:                   float32(res.Score),
			Propensity:                    propensity,
			DisplayMarker:                 displayMarker,
			RouteID:                       routeID,
			Strategy:                      string(strategy),
			PolicyRouteKey:                res.PolicyRouteKey,
			PolicyGroup:                   res.PolicyGroup,
			PolicyArtifactID:              selectionPolicyReleaseID,
			PolicyArtifactSHA256:          selectionPolicySHA256,
			RosterVersion:                 rosterVersion,
			PolicyPinHonoured:             pinned,
			ClassifierArtifactID:          res.PolicyArtifactID,
			ClassifierArtifactSHA256:      res.PolicyArtifactSHA256,
			ClassifierPredictedLabel:      res.PredictedLabel,
			ClassifierClassOrder:          append([]string(nil), res.ClassOrder...),
			ClassifierProbabilities:       cloneProbabilities(res.ClassProbabilities),
			SelectionPolicyReleaseID:      selectionPolicyReleaseID,
			SelectionPolicySHA256:         selectionPolicySHA256,
			SelectionHeadGeneration:       r.config.SelectionHeadGeneration,
			SelectionTrace:                replayTrace,
			SidecarTimings:                res.Timings,
			SidecarStats:                  res.ServingStats,
			SelectedArmID:                 binding.ArmID,
			SelectedRosterArmID:           selectedRosterArmID,
			SidecarSchemaVersion:          res.SchemaVersion,
			DebugRef:                      debugRef,
			AuthoritativePerTurnSelection: capabilities.AuthoritativePerTurnSelection,
			SelectedUpstreamID:            binding.UpstreamID,
			BindingIndex:                  binding.BindingIndex,
			CandidateArmIDs:               resolved.CandidateArmIDs(),
			ArmScores:                     res.ArmScores,
		},
	}, nil
}

func (r *SidecarRouter) validateClassifierIdentity(artifactID, artifactSHA256 string) error {
	if r.config.ClassifierArtifactID != "" && artifactID != r.config.ClassifierArtifactID {
		return fmt.Errorf("classifier artifact %q does not match release %q", artifactID, r.config.ClassifierArtifactID)
	}
	if r.config.ClassifierArtifactSHA256 != "" && artifactSHA256 != r.config.ClassifierArtifactSHA256 {
		return fmt.Errorf("classifier digest %q does not match release %q", artifactSHA256, r.config.ClassifierArtifactSHA256)
	}
	return nil
}

var _ RoutePreviewer = (*SidecarRouter)(nil)

var _ router.Router = (*SidecarRouter)(nil)
var _ OutcomeReporter = (*SidecarRouter)(nil)
var _ FeedbackReporter = (*SidecarRouter)(nil)
var _ escalation.Observer = (*SidecarRouter)(nil)
